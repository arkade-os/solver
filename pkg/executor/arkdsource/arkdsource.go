// Package arkdsource provides an arkd-backed executor.Source. It is split
// from pkg/executor to keep the executor package free of any arkd/go-sdk
// dependency.
package arkdsource

import (
	"context"
	"math/rand/v2"
	"strings"
	"time"

	arkv1 "github.com/arkade-os/arkd/api-spec/protobuf/gen/ark/v1"
	"github.com/arkade-os/arkd/pkg/client-lib/client"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SubscriptionClient is the part of arkd's indexer service a filtered
// subscription needs.
//
// This is the generated stub rather than client-lib's indexer.Indexer because
// that wrapper cannot carry an expression: NewSubscription takes a script list
// and nothing else, and it opens the stream with a subscription id already
// assigned — which is precisely the case where arkd ignores the filter.
type SubscriptionClient interface {
	GetSubscription(
		ctx context.Context, in *arkv1.GetSubscriptionRequest, opts ...grpc.CallOption,
	) (arkv1.IndexerService_GetSubscriptionClient, error)
}

// Reconnect backoff for the filtered subscription. These match client-lib's
// own GrpcReconnectConfig, which is what the unfiltered path already gets from
// utils.StartReconnectingStream: two streams against the same arkd have no
// reason to flap on different schedules.
//
// The ceiling is the number that matters. It bounds how long the subscription
// stays down once arkd is reachable again — and so how long a claimable tx can
// go unseen — while still being slow enough that retrying against an arkd that
// is down costs one dial every ten seconds rather than a spin.
const (
	reconnectInitialDelay = time.Second
	reconnectMaxDelay     = 10 * time.Second
	reconnectMultiplier   = 2
	// Jitter keeps a fleet of solvers pointed at one arkd from reconnecting in
	// lockstep after it restarts.
	reconnectJitter = 0.2
)

// permanentCodes are the codes that will not come good on a retry: an
// expression arkd cannot compile, a credential it will not accept, an RPC it
// does not serve. Reconnecting through one of those burns a dial every backoff
// interval while the consumer waits on a channel that stays open and empty,
// which hides the fault better than closing the stream does — so these end the
// subscription and let the executor surface it.
//
// The list is deliberately short. Everything not named here, io.EOF included,
// is treated as transient and reconnected: misjudging a recoverable error as
// permanent brings back the silent outage this whole path exists to avoid, so
// the ambiguous cases err towards retrying.
//
// These arrive on the first Recv, not from GetSubscription. gRPC hands back a
// client stream before the server's headers do, so arkd's rejection cannot
// reach Subscribe's caller synchronously — only an unreachable arkd can.
var permanentCodes = map[codes.Code]bool{
	codes.InvalidArgument:  true,
	codes.Unauthenticated:  true,
	codes.PermissionDenied: true,
	codes.Unimplemented:    true,
}

// isPermanent reports whether err ends the subscription instead of reopening
// it. Errors carrying no gRPC status — io.EOF, transport faults — are Unknown
// to status.Code and so are retried.
func isPermanent(err error) bool {
	return permanentCodes[status.Code(err)]
}

// Source subscribes to arkd's transaction stream and exposes it as a
// executor.Source. Each call to Subscribe opens a fresh upstream stream so
// per-plugin server-side filtering stays isolated.
type Source struct {
	c    client.Client
	subs SubscriptionClient
	log  logrus.FieldLogger

	// Backoff bounds for re-establishing a filtered subscription. Set by New;
	// tests shorten them.
	reconnectDelay    time.Duration
	reconnectMaxDelay time.Duration
}

// New returns a Source backed by the given arkd client. Without a subscription
// client (see WithSubscriptions) it can only serve the unfiltered stream.
func New(c client.Client, log logrus.FieldLogger) *Source {
	if log == nil {
		log = logrus.StandardLogger()
	}
	return &Source{
		c:                 c,
		log:               log,
		reconnectDelay:    reconnectInitialDelay,
		reconnectMaxDelay: reconnectMaxDelay,
	}
}

// WithSubscriptions returns a copy of s that honors plugin filters by opening
// an arkd indexer subscription carrying the CEL expression, instead of reading
// the whole tx stream.
func (s *Source) WithSubscriptions(subs SubscriptionClient) *Source {
	cp := *s
	cp.subs = subs
	return &cp
}

// Subscribe opens an arkd transaction stream and forwards each tx as a decoded
// *psbt.Packet.
//
// An empty filter means the full stream, per the executor.Source contract. A
// non-empty one is sent to arkd as a subscription expression so unrelated txs
// are dropped before they reach us; if no subscription client is configured the
// filter cannot be honored, and Subscribe says so and falls back to the full
// stream rather than dropping the plugin's txs on the floor.
//
// A subscription that cannot be established at all is reported as an error,
// with no channel. Once a channel exists it is closed when ctx is canceled, and
// for the unfiltered stream also when client-lib gives up reconnecting; the
// filtered stream re-establishes itself instead (see subscribeFiltered).
//
// Decoding errors on individual events are logged and skipped — the
// consumer receives only successfully-parsed packets.
func (s *Source) Subscribe(ctx context.Context, filter string) (<-chan *psbt.Packet, error) {
	if filter == "" {
		return s.subscribeAll(ctx)
	}
	if s.subs == nil {
		s.log.WithField("filter", filter).Warn(
			"plugin asked for a server-side filter but no arkd subscription client is " +
				"configured; falling back to the unfiltered tx stream",
		)
		return s.subscribeAll(ctx)
	}
	return s.subscribeFiltered(ctx, filter)
}

// subscribeAll reads arkd's unfiltered ark tx stream.
func (s *Source) subscribeAll(ctx context.Context) (<-chan *psbt.Packet, error) {
	eventsCh, stop, err := s.c.GetTransactionsStream(ctx)
	if err != nil {
		return nil, err
	}

	out := make(chan *psbt.Packet, 1)
	go func() {
		defer close(out)
		defer func() {
			if stop != nil {
				stop()
			}
		}()

		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-eventsCh:
				if !ok {
					s.log.Debug("arkd transaction stream closed")
					return
				}
				if ev.Err != nil {
					s.log.WithError(ev.Err).Warn("arkd stream error")
					continue
				}
				if ev.ArkTx == nil {
					continue
				}
				pkt, perr := psbt.NewFromRawBytes(strings.NewReader(ev.ArkTx.Tx), true)
				if perr != nil {
					s.log.WithError(perr).WithField("txid", ev.ArkTx.Txid).
						Warn("failed to decode arkd tx as psbt")
					continue
				}
				if !s.send(ctx, out, pkt) {
					return
				}
			}
		}
	}()
	return out, nil
}

// subscribeFiltered opens an indexer subscription evaluated server-side and
// keeps one open for the lifetime of ctx.
//
// The first subscribe is synchronous, so an arkd that is not there surfaces to
// the caller. An expression arkd rejects does not: gRPC returns a client stream
// before the server's headers arrive, so the InvalidArgument lands on the first
// Recv, inside the goroutine, after Subscribe has already returned nil.
//
// So the receive loop sorts its errors. A transient one reconnects, because a
// closed channel is not a signal the executor can act on — it reads a closed
// source as end-of-stream, and once the last plugin's stream goes Run returns
// ErrAllStreamsClosed. That includes io.EOF: a server-streaming RPC ends in EOF
// whenever the server closes it — arkd restarting, a deploy, a proxy reaping an
// idle connection — and arkd has no "subscription finished" event, so EOF says
// the server went away, not that there will never be more txs.
//
// A permanent one (see permanentCodes) ends the subscription instead. Retrying
// a rejected expression forever would leave the consumer holding a channel that
// never closes and never delivers, which is a quieter failure than the one this
// path was written to fix. Closing lets the executor report it.
//
// Reconnecting restores the stream. It does not replay what the stream missed:
// arkd creates a fresh subscription each time and GetSubscriptionRequest carries
// only a subscription id and a filter, with nothing to resume from. Txs that
// arrive while the subscription is down are not delivered afterwards.
//
// The subscription id is left empty on purpose: arkd only applies the filter
// when it is creating the subscription itself, and ignores it otherwise. The
// stream then carries heartbeats and a subscription-started event alongside the
// tx events, all of which are skipped here.
func (s *Source) subscribeFiltered(
	ctx context.Context, filter string,
) (<-chan *psbt.Packet, error) {
	stream, err := s.openSubscription(ctx, filter)
	if err != nil {
		return nil, err
	}

	out := make(chan *psbt.Packet, 1)
	go func() {
		defer close(out)

		for {
			rerr := s.readStream(ctx, stream, out)
			if rerr == nil {
				s.log.Debug("arkd subscription stream closed")
				return
			}
			if isPermanent(rerr) {
				s.log.WithError(rerr).Error(
					"arkd rejected the subscription; closing the stream instead of " +
						"retrying an error that will not clear",
				)
				return
			}
			s.log.WithError(rerr).Warn("arkd subscription stream broke, reconnecting")

			var oerr error
			stream, oerr = s.reopenSubscription(ctx, filter)
			if oerr != nil {
				s.log.WithError(oerr).Error(
					"arkd rejected the re-subscription; closing the stream",
				)
				return
			}
			if stream == nil {
				return
			}
		}
	}()
	return out, nil
}

// openSubscription starts a subscription carrying filter.
func (s *Source) openSubscription(
	ctx context.Context, filter string,
) (arkv1.IndexerService_GetSubscriptionClient, error) {
	return s.subs.GetSubscription(ctx, &arkv1.GetSubscriptionRequest{
		Filter: &arkv1.SubscriptionFilter{Expressions: []string{filter}},
	})
}

// readStream forwards packets from stream until it fails, returning the receive
// error that ended it. A nil error means ctx ended and the subscription should
// not be re-established.
func (s *Source) readStream(
	ctx context.Context,
	stream arkv1.IndexerService_GetSubscriptionClient,
	out chan<- *psbt.Packet,
) error {
	for {
		resp, rerr := stream.Recv()
		if rerr != nil {
			if ctx.Err() != nil {
				return nil
			}
			return rerr
		}
		ev := resp.GetEvent()
		if ev == nil || ev.GetTx() == "" {
			continue
		}
		// arkd puts a base64 PSBT here for ark and checkpoint txs, and a
		// hex raw tx for sweeps. Only the former can reach a plugin, so a
		// sweep that satisfies the expression is a skip, not an error.
		pkt, perr := psbt.NewFromRawBytes(strings.NewReader(ev.GetTx()), true)
		if perr != nil {
			s.log.WithError(perr).WithField("txid", ev.GetTxid()).
				Debug("subscription tx is not a psbt, skipping")
			continue
		}
		if !s.send(ctx, out, pkt) {
			return nil
		}
	}
}

// reopenSubscription re-establishes the subscription with the same expression,
// backing off between attempts, until it succeeds or there is nothing left to
// try. It returns either a stream, or a permanent error, or nil for both when
// ctx ended.
func (s *Source) reopenSubscription(
	ctx context.Context, filter string,
) (arkv1.IndexerService_GetSubscriptionClient, error) {
	delay := s.reconnectDelay
	for attempt := 1; ; attempt++ {
		select {
		case <-ctx.Done():
			return nil, nil
		case <-time.After(jittered(delay)):
		}

		stream, err := s.openSubscription(ctx, filter)
		if err == nil {
			s.log.WithField("attempts", attempt).Info("arkd subscription re-established")
			return stream, nil
		}
		if ctx.Err() != nil {
			return nil, nil
		}
		if isPermanent(err) {
			return nil, err
		}
		s.log.WithError(err).WithField("attempts", attempt).
			Warn("failed to reopen arkd subscription")

		delay = min(delay*reconnectMultiplier, s.reconnectMaxDelay)
	}
}

// jittered spreads d by ±reconnectJitter.
func jittered(d time.Duration) time.Duration {
	spread := float64(d) * reconnectJitter
	return time.Duration(float64(d) - spread + rand.Float64()*2*spread)
}

// send hands pkt to the consumer, reporting false when ctx ended first.
func (s *Source) send(ctx context.Context, out chan<- *psbt.Packet, pkt *psbt.Packet) bool {
	select {
	case out <- pkt:
		return true
	case <-ctx.Done():
		return false
	}
}
