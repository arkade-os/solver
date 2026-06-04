package executor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/stretchr/testify/require"
)

func TestRun_ReturnsErrAllStreamsClosedOnChannelClose(t *testing.T) {
	plugin := &fakePlugin{}
	s := New(plugin)
	ctx := context.Background()
	ch := make(chan *psbt.Packet)

	done, errp := runEngine(t, s, ctx, singleSource(ch))
	close(ch)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return within 1s of channel close")
	}
	require.ErrorIs(t, *errp, ErrAllStreamsClosed)
}

func TestRun_ReturnsCtxErrOnCancel(t *testing.T) {
	plugin := &fakePlugin{}
	s := New(plugin)
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan *psbt.Packet)

	done, errp := runEngine(t, s, ctx, singleSource(ch))
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return within 1s of ctx cancel")
	}
	require.ErrorIs(t, *errp, context.Canceled)
}

func TestRun_MatchOkFalseDoesNotCallSolve(t *testing.T) {
	plugin := &fakePlugin{
		matchFn: func(context.Context, *psbt.Packet) (any, bool) { return nil, false },
	}
	s := New(plugin)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan *psbt.Packet, 1)
	ch <- &psbt.Packet{}
	close(ch)

	done, errp := runEngine(t, s, ctx, singleSource(ch))
	<-done
	require.ErrorIs(t, *errp, ErrAllStreamsClosed)
	require.Empty(t, plugin.solved)
}

func TestRun_MatchOkTrueCallsSolve(t *testing.T) {
	plugin := &fakePlugin{
		matchFn: func(context.Context, *psbt.Packet) (any, bool) { return "intent-1", true },
	}
	plugin.expectSolves(1)
	s := New(plugin)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan *psbt.Packet, 1)
	ch <- &psbt.Packet{}
	close(ch)

	done, errp := runEngine(t, s, ctx, singleSource(ch))
	<-done
	plugin.waitSolves(t)
	require.ErrorIs(t, *errp, ErrAllStreamsClosed)
	require.Equal(t, []any{"intent-1"}, plugin.solved)
}

func TestRun_NilTxIsSkipped(t *testing.T) {
	plugin := &fakePlugin{}
	s := New(plugin)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan *psbt.Packet, 2)
	ch <- nil
	ch <- &psbt.Packet{}
	close(ch)

	done, errp := runEngine(t, s, ctx, singleSource(ch))
	<-done
	require.ErrorIs(t, *errp, ErrAllStreamsClosed)
	// Only the non-nil tx should have been Matched.
	require.Equal(t, 1, plugin.matched)
}

// TestRun_PerPluginFilteredSubscription verifies each Plugin gets its own
// subscription keyed by its Filter(), and only its own stream's txs reach
// its Match.
func TestRun_PerPluginFilteredSubscription(t *testing.T) {
	p1 := &fakePlugin{
		filter:  "tag == 'a'",
		matchFn: func(context.Context, *psbt.Packet) (any, bool) { return "p1", true },
	}
	p2 := &fakePlugin{
		filter:  "tag == 'b'",
		matchFn: func(context.Context, *psbt.Packet) (any, bool) { return "p2", true },
	}
	p1.expectSolves(1)
	p2.expectSolves(1)

	ch1 := make(chan *psbt.Packet, 1)
	ch1 <- &psbt.Packet{}
	close(ch1)
	ch2 := make(chan *psbt.Packet, 1)
	ch2 <- &psbt.Packet{}
	close(ch2)

	src := newFakeSource().
		withStream("tag == 'a'", ch1).
		withStream("tag == 'b'", ch2)

	s := New(p1, p2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done, errp := runEngine(t, s, ctx, src)
	<-done
	p1.waitSolves(t)
	p2.waitSolves(t)
	require.ErrorIs(t, *errp, ErrAllStreamsClosed)
	require.ElementsMatch(t, []string{"tag == 'a'", "tag == 'b'"}, src.seenFilters())
	// Each plugin saw exactly its own tx, not the other's.
	require.Equal(t, 1, p1.matched)
	require.Equal(t, 1, p2.matched)
	require.Equal(t, []any{"p1"}, p1.solved)
	require.Equal(t, []any{"p2"}, p2.solved)
}

// TestRun_SubscribeErrorSkipsPlugin verifies a Subscribe failure for one
// plugin doesn't prevent the others from running.
func TestRun_SubscribeErrorSkipsPlugin(t *testing.T) {
	failing := &fakePlugin{filter: "bad"}
	working := &fakePlugin{
		filter:  "good",
		matchFn: func(context.Context, *psbt.Packet) (any, bool) { return "ok", true },
	}
	working.expectSolves(1)

	ch := make(chan *psbt.Packet, 1)
	ch <- &psbt.Packet{}
	close(ch)

	src := newFakeSource().
		withSubErr("bad", errors.New("nope")).
		withStream("good", ch)

	s := New(failing, working).WithLogger(nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done, errp := runEngine(t, s, ctx, src)
	<-done
	working.waitSolves(t)
	require.ErrorIs(t, *errp, ErrAllStreamsClosed)
	require.Equal(t, 0, failing.matched)
	require.Equal(t, []any{"ok"}, working.solved)
}

// TestRun_WaitsForInflightSolvesOnChannelClose ensures Run does not return
// until concurrently-launched Solves complete, so callers see clean shutdown.
func TestRun_WaitsForInflightSolvesOnChannelClose(t *testing.T) {
	release := make(chan struct{})
	var solveDone int32
	plugin := &fakePlugin{
		matchFn: func(context.Context, *psbt.Packet) (any, bool) { return "intent", true },
		solveFn: func(context.Context, any) {
			<-release
			atomic.StoreInt32(&solveDone, 1)
		},
	}
	plugin.expectSolves(1)
	s := New(plugin)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan *psbt.Packet, 1)
	ch <- &psbt.Packet{}
	close(ch)

	done, errp := runEngine(t, s, ctx, singleSource(ch))

	// Run must NOT return while Solve is still blocked.
	select {
	case <-done:
		t.Fatal("Run returned before in-flight Solve completed")
	case <-time.After(50 * time.Millisecond):
	}
	require.Equal(t, int32(0), atomic.LoadInt32(&solveDone))

	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return within 1s after Solve unblocked")
	}
	plugin.waitSolves(t)
	require.ErrorIs(t, *errp, ErrAllStreamsClosed)
	require.Equal(t, int32(1), atomic.LoadInt32(&solveDone))
}

// TestRun_WaitsForInflightSolvesOnCtxCancel ensures cancellation also waits
// for in-flight Solves to drain before Run returns.
func TestRun_WaitsForInflightSolvesOnCtxCancel(t *testing.T) {
	release := make(chan struct{})
	plugin := &fakePlugin{
		matchFn: func(context.Context, *psbt.Packet) (any, bool) { return "intent", true },
		solveFn: func(context.Context, any) { <-release },
	}
	plugin.expectSolves(1)
	s := New(plugin)
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan *psbt.Packet, 1)
	ch <- &psbt.Packet{}

	done, errp := runEngine(t, s, ctx, singleSource(ch))

	// Wait for Match to dispatch the Solve, then cancel.
	require.Eventually(t, func() bool {
		plugin.mu.Lock()
		defer plugin.mu.Unlock()
		return plugin.matched == 1
	}, time.Second, 5*time.Millisecond)
	cancel()

	select {
	case <-done:
		t.Fatal("Run returned before in-flight Solve completed on cancel")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return within 1s after Solve unblocked")
	}
	plugin.waitSolves(t)
	require.ErrorIs(t, *errp, context.Canceled)
}

// TestRun_RecoversMatchPanic ensures a panicking Match doesn't crash Run
// and the tx is treated as unmatched.
func TestRun_RecoversMatchPanic(t *testing.T) {
	plugin := &fakePlugin{
		matchFn: func(context.Context, *psbt.Packet) (any, bool) { panic("boom") },
	}
	s := New(plugin).WithLogger(nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan *psbt.Packet, 1)
	ch <- &psbt.Packet{}
	close(ch)

	done, errp := runEngine(t, s, ctx, singleSource(ch))
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return within 1s after Match panic")
	}
	require.ErrorIs(t, *errp, ErrAllStreamsClosed)
	require.Empty(t, plugin.solved)
}

// TestRun_RecoversSolvePanic ensures a panicking Solve doesn't crash Run
// and the runtime still drains cleanly.
func TestRun_RecoversSolvePanic(t *testing.T) {
	plugin := &fakePlugin{
		matchFn: func(context.Context, *psbt.Packet) (any, bool) { return "intent", true },
		solveFn: func(context.Context, any) { panic("boom") },
	}
	plugin.expectSolves(1)
	s := New(plugin).WithLogger(nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan *psbt.Packet, 1)
	ch <- &psbt.Packet{}
	close(ch)

	done, errp := runEngine(t, s, ctx, singleSource(ch))
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return within 1s after Solve panic")
	}
	plugin.waitSolves(t)
	require.ErrorIs(t, *errp, ErrAllStreamsClosed)
}

// fakePlugin drives engine tests.
type fakePlugin struct {
	mu      sync.Mutex
	filter  string
	matchFn func(context.Context, *psbt.Packet) (any, bool)
	solveFn func(context.Context, any)
	matched int
	solved  []any
	solveWg sync.WaitGroup
}

func (f *fakePlugin) Filter() string { return f.filter }

func (f *fakePlugin) Match(ctx context.Context, tx *psbt.Packet) (any, bool) {
	f.mu.Lock()
	f.matched++
	f.mu.Unlock()
	if f.matchFn != nil {
		return f.matchFn(ctx, tx)
	}
	return nil, false
}

func (f *fakePlugin) Solve(ctx context.Context, intent any) {
	defer f.solveWg.Done()
	f.mu.Lock()
	f.solved = append(f.solved, intent)
	f.mu.Unlock()
	if f.solveFn != nil {
		f.solveFn(ctx, intent)
	}
}

// expectSolves marks how many Solve calls the test expects, so it can wait
// for the (concurrent) Solve goroutines to finish before asserting.
func (f *fakePlugin) expectSolves(n int) { f.solveWg.Add(n) }

func (f *fakePlugin) waitSolves(t *testing.T) {
	t.Helper()
	done := make(chan struct{})
	go func() { f.solveWg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Solve goroutines did not finish within 1s")
	}
}

// fakeSource hands each Subscribe call a pre-loaded channel, indexed by
// the filter string the plugin asked for. This lets tests assert that the
// executor subscribed with the right filter and lets each plugin receive
// its own tagged stream.
type fakeSource struct {
	mu       sync.Mutex
	streams  map[string]chan *psbt.Packet
	subErr   map[string]error
	seen     []string
	fallback chan *psbt.Packet // used when filter has no preconfigured stream
}

func newFakeSource() *fakeSource {
	return &fakeSource{streams: map[string]chan *psbt.Packet{}, subErr: map[string]error{}}
}

func (s *fakeSource) withStream(filter string, ch chan *psbt.Packet) *fakeSource {
	s.streams[filter] = ch
	return s
}

func (s *fakeSource) withSubErr(filter string, err error) *fakeSource {
	s.subErr[filter] = err
	return s
}

func (s *fakeSource) withFallback(ch chan *psbt.Packet) *fakeSource {
	s.fallback = ch
	return s
}

func (s *fakeSource) Subscribe(_ context.Context, filter string) (<-chan *psbt.Packet, error) {
	s.mu.Lock()
	s.seen = append(s.seen, filter)
	s.mu.Unlock()
	if err, ok := s.subErr[filter]; ok {
		return nil, err
	}
	if ch, ok := s.streams[filter]; ok {
		return ch, nil
	}
	if s.fallback != nil {
		return s.fallback, nil
	}
	// Default: empty stream that the caller closes externally.
	ch := make(chan *psbt.Packet)
	close(ch)
	return ch, nil
}

func (s *fakeSource) seenFilters() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]string(nil), s.seen...)
	return out
}

// runEngine spawns Run in a goroutine, returns done channel + result holder.
func runEngine(t *testing.T, s *Executor, ctx context.Context, src Source) (chan struct{}, *error) {
	t.Helper()
	done := make(chan struct{})
	var runErr error
	go func() {
		defer close(done)
		runErr = s.Run(ctx, src)
	}()
	return done, &runErr
}

// singleSource wraps a single channel as a Source for one-plugin tests.
func singleSource(ch chan *psbt.Packet) Source {
	return newFakeSource().withFallback(ch)
}
