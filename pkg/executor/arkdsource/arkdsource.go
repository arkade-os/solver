// Package arkdsource provides an arkd-backed executor.Source. It is split
// from pkg/executor to keep the executor package free of any arkd/go-sdk
// dependency.
package arkdsource

import (
	"context"
	"errors"
	"io"
	"strings"

	arkv1 "github.com/arkade-os/arkd/api-spec/protobuf/gen/ark/v1"
	"github.com/arkade-os/arkd/pkg/client-lib/client"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
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

// Source subscribes to arkd's transaction stream and exposes it as a
// executor.Source. Each call to Subscribe opens a fresh upstream stream so
// per-plugin server-side filtering stays isolated.
type Source struct {
	c    client.Client
	subs SubscriptionClient
	log  logrus.FieldLogger
}

// New returns a Source backed by the given arkd client. Without a subscription
// client (see WithSubscriptions) it can only serve the unfiltered stream.
func New(c client.Client, log logrus.FieldLogger) *Source {
	if log == nil {
		log = logrus.StandardLogger()
	}
	return &Source{c: c, log: log}
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
// The returned channel is closed when:
//   - ctx is canceled
//   - the upstream stream errors out at subscribe time
//   - the upstream stream is closed
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

// subscribeFiltered opens an indexer subscription evaluated server-side.
//
// The subscription id is left empty on purpose: arkd only applies the filter
// when it is creating the subscription itself, and ignores it otherwise. The
// stream then carries heartbeats and a subscription-started event alongside the
// tx events, all of which are skipped here.
func (s *Source) subscribeFiltered(
	ctx context.Context, filter string,
) (<-chan *psbt.Packet, error) {
	stream, err := s.subs.GetSubscription(ctx, &arkv1.GetSubscriptionRequest{
		Filter: &arkv1.SubscriptionFilter{Expressions: []string{filter}},
	})
	if err != nil {
		return nil, err
	}

	out := make(chan *psbt.Packet, 1)
	go func() {
		defer close(out)

		for {
			resp, rerr := stream.Recv()
			if rerr != nil {
				if ctx.Err() != nil || errors.Is(rerr, io.EOF) {
					s.log.Debug("arkd subscription stream closed")
					return
				}
				s.log.WithError(rerr).Warn("arkd subscription stream error")
				return
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
				return
			}
		}
	}()
	return out, nil
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
