package arkdsource

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	arkv1 "github.com/arkade-os/arkd/api-spec/protobuf/gen/ark/v1"
	"github.com/arkade-os/arkd/pkg/client-lib/client"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/wire"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// An empty filter is the documented "full stream", and must keep using the
// tx stream even when a subscription client is available — a subscription
// carrying no expressions and no scripts matches nothing, so routing an empty
// filter there would silently starve every plugin that does not set one.
func TestSubscribe_EmptyFilterUsesTxStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	arkd := newStubArkClient()
	subs := newStubSubscriptions()
	src := New(arkd, quietLog()).WithSubscriptions(subs)

	out, err := src.Subscribe(ctx, "")
	require.NoError(t, err)

	arkd.events <- arkTxEvent(t)
	require.NotNil(t, recvPacket(t, out))

	assert.Equal(t, 1, arkd.calls, "should have opened the tx stream")
	assert.Empty(t, subs.reqs(), "should not have opened a subscription")
}

// The point of the change: a non-empty filter reaches arkd as a subscription
// expression instead of being dropped.
func TestSubscribe_FilterReachesArkd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const filter = "has(tx.extension) && hasPacket(tx.extension, 4)"

	arkd := newStubArkClient()
	subs := newStubSubscriptions()
	src := New(arkd, quietLog()).WithSubscriptions(subs)

	out, err := src.Subscribe(ctx, filter)
	require.NoError(t, err)

	reqs := subs.reqs()
	require.Len(t, reqs, 1)
	req := reqs[0]
	assert.Equal(t, []string{filter}, req.GetFilter().GetExpressions())
	// arkd applies the filter only while creating the subscription, and
	// ignores it when an id is already set.
	assert.Empty(t, req.GetSubscriptionId())
	assert.Equal(t, 0, arkd.calls, "should not have fallen back to the tx stream")

	subs.nextStream(t).msgs <- txEvent(t, "aa")
	assert.NotNil(t, recvPacket(t, out))
}

// Heartbeats and the subscription-started event share the stream with tx
// events and carry no tx; they must not reach the plugin or stall it.
func TestSubscribe_SkipsNonTxMessages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	arkd := newStubArkClient()
	subs := newStubSubscriptions()
	src := New(arkd, quietLog()).WithSubscriptions(subs)

	out, err := src.Subscribe(ctx, "has(tx.extension)")
	require.NoError(t, err)

	stream := subs.nextStream(t)
	stream.msgs <- &arkv1.GetSubscriptionResponse{
		Data: &arkv1.GetSubscriptionResponse_SubscriptionStarted{
			SubscriptionStarted: &arkv1.SubscriptionStartedEvent{SubscriptionId: "s1"},
		},
	}
	stream.msgs <- &arkv1.GetSubscriptionResponse{
		Data: &arkv1.GetSubscriptionResponse_Heartbeat{Heartbeat: &arkv1.IndexerHeartbeat{}},
	}
	// A sweep tx is hex, not a PSBT: skipped, not fatal.
	stream.msgs <- &arkv1.GetSubscriptionResponse{
		Data: &arkv1.GetSubscriptionResponse_Event{
			Event: &arkv1.IndexerSubscriptionEvent{Txid: "bb", Tx: "00112233"},
		},
	}
	stream.msgs <- txEvent(t, "cc")

	assert.NotNil(t, recvPacket(t, out), "the psbt should arrive despite the three before it")
}

// Without a subscription client the filter cannot be honored. Falling back to
// the full stream is right — the plugin still filters in Match — but doing it
// silently is what made Filter() look wired when it was not.
func TestSubscribe_FallsBackWhenNoSubscriptionClient(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	arkd := newStubArkClient()
	log, hook := recordingLog()
	src := New(arkd, log)

	out, err := src.Subscribe(ctx, "has(tx.extension)")
	require.NoError(t, err)

	assert.Equal(t, 1, arkd.calls, "should have fallen back to the tx stream")
	arkd.events <- arkTxEvent(t)
	assert.NotNil(t, recvPacket(t, out))

	entries := hook.snapshot()
	require.NotEmpty(t, entries)
	assert.Equal(t, logrus.WarnLevel, entries[0].Level)
	assert.Contains(t, entries[0].Message, "falling back to the unfiltered tx stream")
}

func TestSubscribe_FilteredSubscribeErrorIsReturned(t *testing.T) {
	subs := newStubSubscriptions()
	subs.err = errors.New("boom")
	src := New(newStubArkClient(), quietLog()).WithSubscriptions(subs)

	_, err := src.Subscribe(context.Background(), "has(tx.extension)")
	assert.ErrorContains(t, err, "boom")
}

// The defect this guards: every Recv error used to end the subscription. A
// dropped connection, an arkd restart or a GOAWAY closed the channel for good,
// the executor read that as end-of-stream, and the solver kept running while
// claiming nothing.
func TestSubscribe_ReconnectsAfterStreamFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const filter = "has(tx.extension) && hasPacket(tx.extension, 4)"

	subs := newStubSubscriptions()
	src := filteredSource(subs, quietLog())

	out, err := src.Subscribe(ctx, filter)
	require.NoError(t, err)

	// A packet, then the stream dies the way a reset connection does.
	first := subs.nextStream(t)
	first.msgs <- txEvent(t, "aa")
	require.NotNil(t, recvPacket(t, out))
	first.recvErr <- status.Error(codes.Unavailable, "connection reset")

	// The consumer must keep receiving across the break, never seeing a close.
	second := subs.nextStream(t)
	second.msgs <- txEvent(t, "bb")
	require.NotNil(t, recvPacket(t, out), "no packet after the reconnect")

	// Once more, so this is a loop and not a single retry.
	second.recvErr <- errors.New("boom")
	third := subs.nextStream(t)
	third.msgs <- txEvent(t, "cc")
	require.NotNil(t, recvPacket(t, out), "no packet after the second reconnect")

	// The expression has to survive every re-subscribe: a reconnected stream
	// that lost its filter delivers the wrong txs, or nothing at all.
	reqs := subs.reqs()
	require.Len(t, reqs, 3)
	for i, req := range reqs {
		assert.Equal(t, []string{filter}, req.GetFilter().GetExpressions(),
			"subscribe #%d dropped the expression", i+1)
		assert.Empty(t, req.GetSubscriptionId(),
			"subscribe #%d sent an id, which makes arkd ignore the filter", i+1)
	}
}

// EOF is how a server-streaming RPC reports that the server closed the stream:
// an arkd restart, a deploy, a proxy reaping an idle connection. arkd has no
// "subscription finished" event, so EOF means the server went away, not that
// there will never be another tx.
func TestSubscribe_ReconnectsOnServerEOF(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	subs := newStubSubscriptions()
	src := filteredSource(subs, quietLog())

	out, err := src.Subscribe(ctx, "has(tx.extension)")
	require.NoError(t, err)

	subs.nextStream(t).recvErr <- io.EOF

	second := subs.nextStream(t)
	second.msgs <- txEvent(t, "aa")
	require.NotNil(t, recvPacket(t, out), "EOF ended the subscription instead of reconnecting")
}

// The counterpart to reconnecting: an error that will never clear must not be
// retried. arkd reports a rejected expression on the first Recv, not from
// GetSubscription — gRPC hands back a client stream before the server's headers
// arrive — so Subscribe has already returned nil by then. Retrying it would
// leave the consumer on a channel that never closes and never delivers, which
// hides the fault more thoroughly than the bug this file set out to fix.
func TestSubscribe_PermanentErrorClosesChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	subs := newStubSubscriptions()
	log, hook := recordingLog()
	src := filteredSource(subs, log)

	out, err := src.Subscribe(ctx, "bogus(")
	require.NoError(t, err, "the rejection cannot surface here; it arrives on Recv")

	subs.nextStream(t).recvErr <- status.Error(codes.InvalidArgument, "invalid CEL expression")

	select {
	case _, ok := <-out:
		assert.False(t, ok, "channel should be closed, not carrying a packet")
	case <-time.After(2 * time.Second):
		t.Fatal("channel stayed open on a permanent error: the consumer is stranded")
	}

	// The give-away for a retry loop is a second subscribe. Backoff here is 5ms,
	// so anything cycling would have gone round many times by now.
	assert.Len(t, subs.reqs(), 1, "re-subscribed on an error that will not clear")

	var loggedError bool
	for _, e := range hook.snapshot() {
		if e.Level == logrus.ErrorLevel {
			loggedError = true
		}
	}
	assert.True(t, loggedError, "a permanent rejection should log at Error, not Warn")
}

// The neighbouring case, to pin the boundary: Unavailable is what a restarting
// arkd looks like, and it must still reconnect rather than be swept up by the
// permanent-error check.
func TestSubscribe_UnavailableStillReconnects(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	subs := newStubSubscriptions()
	src := filteredSource(subs, quietLog())

	out, err := src.Subscribe(ctx, "has(tx.extension)")
	require.NoError(t, err)

	subs.nextStream(t).recvErr <- status.Error(codes.Unavailable, "arkd restarting")

	second := subs.nextStream(t)
	second.msgs <- txEvent(t, "aa")
	require.NotNil(t, recvPacket(t, out), "Unavailable was treated as permanent")
}

// Canceling ctx is the only thing that ends the subscription, and it has to
// land even while a reconnect is pending — an hour of backoff must not
// outlive the consumer.
func TestSubscribe_CancelClosesChannelDuringReconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	subs := newStubSubscriptions()
	src := filteredSource(subs, quietLog())
	src.reconnectDelay = time.Hour
	src.reconnectMaxDelay = time.Hour

	out, err := src.Subscribe(ctx, "has(tx.extension)")
	require.NoError(t, err)

	// Break the stream so the source is sitting in its backoff, then cancel.
	subs.nextStream(t).recvErr <- errors.New("boom")
	cancel()

	select {
	case _, ok := <-out:
		assert.False(t, ok, "channel should be closed, not carrying a packet")
	case <-time.After(2 * time.Second):
		t.Fatal("channel was not closed after ctx was canceled")
	}
}

// Backoff has to actually back off: an arkd that stays down should cost one
// dial per interval, not a spin that pins a core and floods the log.
func TestSubscribe_ReconnectBacksOff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	subs := newStubSubscriptions()
	subs.reopenErr = errors.New("arkd is down")
	src := New(newStubArkClient(), quietLog()).WithSubscriptions(subs)
	src.reconnectDelay = 40 * time.Millisecond
	src.reconnectMaxDelay = 40 * time.Millisecond

	out, err := src.Subscribe(ctx, "has(tx.extension)")
	require.NoError(t, err)

	subs.nextStream(t).recvErr <- errors.New("boom")
	time.Sleep(300 * time.Millisecond)

	// ~8 attempts at 40ms; a spin would be in the thousands.
	attempts := len(subs.reqs())
	assert.Greater(t, attempts, 1, "did not retry at all")
	assert.Less(t, attempts, 40, "reconnect is spinning instead of backing off")

	cancel()
	select {
	case _, ok := <-out:
		assert.False(t, ok, "channel should be closed, not carrying a packet")
	case <-time.After(2 * time.Second):
		t.Fatal("channel was not closed after ctx was canceled")
	}
}

// A flapping subscription has to be visible. The old loop logged one line and
// gave up; an operator looking at a solver that stopped claiming needs to see
// both the break and the recovery.
func TestSubscribe_LogsReconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	subs := newStubSubscriptions()
	log, hook := recordingLog()
	src := filteredSource(subs, log)

	out, err := src.Subscribe(ctx, "has(tx.extension)")
	require.NoError(t, err)

	subs.nextStream(t).recvErr <- errors.New("connection reset")
	subs.nextStream(t).msgs <- txEvent(t, "aa")
	require.NotNil(t, recvPacket(t, out))

	var loggedBreak, loggedRecovery bool
	for _, e := range hook.snapshot() {
		if e.Level == logrus.WarnLevel && strings.Contains(e.Message, "reconnecting") {
			loggedBreak = true
		}
		if e.Level == logrus.InfoLevel && strings.Contains(e.Message, "re-established") {
			loggedRecovery = true
		}
	}
	assert.True(t, loggedBreak, "the break was not logged")
	assert.True(t, loggedRecovery, "the recovery was not logged")
}

// --- helpers ---

// filteredSource builds a Source with a reconnect backoff short enough for a
// test to sit through. The production bounds are exercised by
// TestSubscribe_ReconnectBacksOff.
func filteredSource(subs SubscriptionClient, log logrus.FieldLogger) *Source {
	src := New(newStubArkClient(), log).WithSubscriptions(subs)
	src.reconnectDelay = 5 * time.Millisecond
	src.reconnectMaxDelay = 20 * time.Millisecond
	return src
}

func txEvent(t *testing.T, txid string) *arkv1.GetSubscriptionResponse {
	t.Helper()
	return &arkv1.GetSubscriptionResponse{
		Data: &arkv1.GetSubscriptionResponse_Event{
			Event: &arkv1.IndexerSubscriptionEvent{Txid: txid, Tx: psbtB64(t)},
		},
	}
}

func recvPacket(t *testing.T, out <-chan *psbt.Packet) *psbt.Packet {
	t.Helper()
	select {
	case pkt, ok := <-out:
		require.True(t, ok, "stream closed instead of delivering a packet")
		return pkt
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a packet")
		return nil
	}
}

func psbtB64(t *testing.T) string {
	t.Helper()
	tx := wire.NewMsgTx(2)
	tx.AddTxIn(wire.NewTxIn(&wire.OutPoint{Index: 0}, nil, nil))
	tx.AddTxOut(wire.NewTxOut(1000, []byte{0x51, 0x20}))
	p, err := psbt.NewFromUnsignedTx(tx)
	require.NoError(t, err)
	b64, err := p.B64Encode()
	require.NoError(t, err)
	return b64
}

func quietLog() logrus.FieldLogger {
	l := logrus.New()
	l.SetOutput(io.Discard)
	return l
}

func recordingLog() (logrus.FieldLogger, *captureHook) {
	l := logrus.New()
	l.SetOutput(io.Discard)
	h := &captureHook{}
	l.AddHook(h)
	return l, h
}

// captureHook is mutex-guarded: reconnect logging comes from the Source's own
// goroutine while the test reads the entries.
type captureHook struct {
	mu      sync.Mutex
	entries []*logrus.Entry
}

func (h *captureHook) Levels() []logrus.Level { return logrus.AllLevels }

func (h *captureHook) Fire(e *logrus.Entry) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries = append(h.entries, e)
	return nil
}

func (h *captureHook) snapshot() []*logrus.Entry {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*logrus.Entry(nil), h.entries...)
}

// stubArkClient implements only GetTransactionsStream; every other method of
// client.Client would panic on the nil embedded interface, which is the
// assertion that nothing else is reached.
type stubArkClient struct {
	client.Client
	events chan client.TransactionEvent
	calls  int
}

func newStubArkClient() *stubArkClient {
	return &stubArkClient{events: make(chan client.TransactionEvent, 4)}
}

func (c *stubArkClient) GetTransactionsStream(
	_ context.Context,
) (<-chan client.TransactionEvent, func(), error) {
	c.calls++
	return c.events, func() {}, nil
}

// stubSubscriptions hands out a fresh stubStream per call, so a test can fail
// the stream the source is reading and then watch it open the next one. It is
// mutex-guarded because reconnects are driven from the Source's own goroutine.
type stubSubscriptions struct {
	mu       sync.Mutex
	requests []*arkv1.GetSubscriptionRequest
	opened   chan *stubStream
	// err fails every call; reopenErr fails every call but the first, standing
	// in for an arkd that goes away and stays away.
	err       error
	reopenErr error
}

func newStubSubscriptions() *stubSubscriptions {
	return &stubSubscriptions{opened: make(chan *stubStream, 16)}
}

func (s *stubSubscriptions) GetSubscription(
	_ context.Context, in *arkv1.GetSubscriptionRequest, _ ...grpc.CallOption,
) (arkv1.IndexerService_GetSubscriptionClient, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	s.requests = append(s.requests, in)
	if s.reopenErr != nil && len(s.requests) > 1 {
		return nil, s.reopenErr
	}
	stream := &stubStream{
		msgs:    make(chan *arkv1.GetSubscriptionResponse, 8),
		recvErr: make(chan error, 1),
	}
	s.opened <- stream
	return stream, nil
}

// reqs snapshots the requests seen so far.
func (s *stubSubscriptions) reqs() []*arkv1.GetSubscriptionRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*arkv1.GetSubscriptionRequest(nil), s.requests...)
}

// nextStream waits for the source to open another subscription.
func (s *stubSubscriptions) nextStream(t *testing.T) *stubStream {
	t.Helper()
	select {
	case stream := <-s.opened:
		return stream
	case <-time.After(2 * time.Second):
		t.Fatal("source did not open a subscription")
		return nil
	}
}

type stubStream struct {
	grpc.ClientStream
	msgs    chan *arkv1.GetSubscriptionResponse
	recvErr chan error
}

func (s *stubStream) Recv() (*arkv1.GetSubscriptionResponse, error) {
	select {
	case m := <-s.msgs:
		return m, nil
	case err := <-s.recvErr:
		return nil, err
	}
}

func arkTxEvent(t *testing.T) client.TransactionEvent {
	t.Helper()
	return client.TransactionEvent{
		ArkTx: &client.TxNotification{TxData: client.TxData{Tx: psbtB64(t)}},
	}
}
