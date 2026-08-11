package arkdsource

import (
	"context"
	"errors"
	"io"
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
)

// An empty filter is the documented "full stream", and must keep using the
// tx stream even when a subscription client is available — a subscription
// carrying no expressions and no scripts matches nothing, so routing an empty
// filter there would silently starve every plugin that does not set one.
func TestSubscribe_EmptyFilterUsesTxStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	arkd := newStubArkClient()
	subs := &stubSubscriptions{}
	src := New(arkd, quietLog()).WithSubscriptions(subs)

	out, err := src.Subscribe(ctx, "")
	require.NoError(t, err)

	arkd.events <- arkTxEvent(t)
	require.NotNil(t, recvPacket(t, out))

	assert.Equal(t, 1, arkd.calls, "should have opened the tx stream")
	assert.Empty(t, subs.requests, "should not have opened a subscription")
}

// The point of the change: a non-empty filter reaches arkd as a subscription
// expression instead of being dropped.
func TestSubscribe_FilterReachesArkd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const filter = "has(tx.extension) && hasPacket(tx.extension, 4)"

	arkd := newStubArkClient()
	subs := &stubSubscriptions{}
	src := New(arkd, quietLog()).WithSubscriptions(subs)

	out, err := src.Subscribe(ctx, filter)
	require.NoError(t, err)

	require.Len(t, subs.requests, 1)
	req := subs.requests[0]
	assert.Equal(t, []string{filter}, req.GetFilter().GetExpressions())
	// arkd applies the filter only while creating the subscription, and
	// ignores it when an id is already set.
	assert.Empty(t, req.GetSubscriptionId())
	assert.Equal(t, 0, arkd.calls, "should not have fallen back to the tx stream")

	subs.stream.msgs <- &arkv1.GetSubscriptionResponse{
		Data: &arkv1.GetSubscriptionResponse_Event{
			Event: &arkv1.IndexerSubscriptionEvent{Txid: "aa", Tx: psbtB64(t)},
		},
	}
	assert.NotNil(t, recvPacket(t, out))
}

// Heartbeats and the subscription-started event share the stream with tx
// events and carry no tx; they must not reach the plugin or stall it.
func TestSubscribe_SkipsNonTxMessages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	arkd := newStubArkClient()
	subs := &stubSubscriptions{}
	src := New(arkd, quietLog()).WithSubscriptions(subs)

	out, err := src.Subscribe(ctx, "has(tx.extension)")
	require.NoError(t, err)

	subs.stream.msgs <- &arkv1.GetSubscriptionResponse{
		Data: &arkv1.GetSubscriptionResponse_SubscriptionStarted{
			SubscriptionStarted: &arkv1.SubscriptionStartedEvent{SubscriptionId: "s1"},
		},
	}
	subs.stream.msgs <- &arkv1.GetSubscriptionResponse{
		Data: &arkv1.GetSubscriptionResponse_Heartbeat{Heartbeat: &arkv1.IndexerHeartbeat{}},
	}
	// A sweep tx is hex, not a PSBT: skipped, not fatal.
	subs.stream.msgs <- &arkv1.GetSubscriptionResponse{
		Data: &arkv1.GetSubscriptionResponse_Event{
			Event: &arkv1.IndexerSubscriptionEvent{Txid: "bb", Tx: "00112233"},
		},
	}
	subs.stream.msgs <- &arkv1.GetSubscriptionResponse{
		Data: &arkv1.GetSubscriptionResponse_Event{
			Event: &arkv1.IndexerSubscriptionEvent{Txid: "cc", Tx: psbtB64(t)},
		},
	}

	assert.NotNil(t, recvPacket(t, out), "the psbt should arrive despite the three before it")
}

// Without a subscription client the filter cannot be honored. Falling back to
// the full stream is right — the plugin still filters in Match — but doing it
// silently is what made Filter() look wired when it was not.
func TestSubscribe_FallsBackWhenNoSubscriptionClient(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	arkd := newStubArkClient()
	log, entries := recordingLog()
	src := New(arkd, log)

	out, err := src.Subscribe(ctx, "has(tx.extension)")
	require.NoError(t, err)

	assert.Equal(t, 1, arkd.calls, "should have fallen back to the tx stream")
	arkd.events <- arkTxEvent(t)
	assert.NotNil(t, recvPacket(t, out))

	require.NotEmpty(t, *entries)
	assert.Equal(t, logrus.WarnLevel, (*entries)[0].Level)
	assert.Contains(t, (*entries)[0].Message, "falling back to the unfiltered tx stream")
}

func TestSubscribe_FilteredSubscribeErrorIsReturned(t *testing.T) {
	subs := &stubSubscriptions{err: errors.New("boom")}
	src := New(newStubArkClient(), quietLog()).WithSubscriptions(subs)

	_, err := src.Subscribe(context.Background(), "has(tx.extension)")
	assert.ErrorContains(t, err, "boom")
}

func TestSubscribe_ClosesChannelWhenStreamEnds(t *testing.T) {
	subs := &stubSubscriptions{}
	src := New(newStubArkClient(), quietLog()).WithSubscriptions(subs)

	out, err := src.Subscribe(context.Background(), "has(tx.extension)")
	require.NoError(t, err)

	subs.stream.recvErr <- io.EOF
	select {
	case _, ok := <-out:
		assert.False(t, ok, "channel should be closed, not carrying a packet")
	case <-time.After(2 * time.Second):
		t.Fatal("channel was not closed after the stream ended")
	}
}

// --- helpers ---

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

func recordingLog() (logrus.FieldLogger, *[]*logrus.Entry) {
	l := logrus.New()
	l.SetOutput(io.Discard)
	h := &captureHook{}
	l.AddHook(h)
	return l, &h.entries
}

type captureHook struct{ entries []*logrus.Entry }

func (h *captureHook) Levels() []logrus.Level { return logrus.AllLevels }

func (h *captureHook) Fire(e *logrus.Entry) error {
	h.entries = append(h.entries, e)
	return nil
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

type stubSubscriptions struct {
	requests []*arkv1.GetSubscriptionRequest
	stream   *stubStream
	err      error
}

func (s *stubSubscriptions) GetSubscription(
	_ context.Context, in *arkv1.GetSubscriptionRequest, _ ...grpc.CallOption,
) (arkv1.IndexerService_GetSubscriptionClient, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.requests = append(s.requests, in)
	s.stream = &stubStream{
		msgs:    make(chan *arkv1.GetSubscriptionResponse, 8),
		recvErr: make(chan error, 1),
	}
	return s.stream, nil
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
