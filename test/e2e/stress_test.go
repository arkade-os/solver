//go:build stress

// Smoke stress test for solverd. Excluded from every untagged build, so
// `make test` and `make integrationtest` (what CI runs) never see it.
// Run it by hand with `make stresstest` against a live stack.
//
// It reports rather than asserts: the numbers are for a human to read. The
// only hard failures are a dead solverd and a broken harness.
package e2e_test

import (
	"context"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arkade-os/arkd/pkg/ark-lib/asset"
	clientTypes "github.com/arkade-os/arkd/pkg/client-lib/types"
	arksdk "github.com/arkade-os/go-sdk"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	swapv1 "github.com/arkade-os/solver/api-spec/protobuf/gen/go/solverd/v1"
	"github.com/arkade-os/solver/pkg/swap"
	"github.com/arkade-os/solver/pkg/swap/contract"
)

const (
	// BTC deposited per offer. The mock btc-asset feed prices 1 sat at 1
	// asset unit, so the maker wants the same number of asset units back.
	stressOfferSats = 500
	// Enough for one burst offer plus a long tail of sustained ones.
	stressMakerFunds = 100000
	// How many maker wallets are built at once. Each Init syncs against arkd.
	stressSetupParallelism = 8
)

// submission is one offer we pushed at the solver.
type submission struct {
	phase  string
	txid   string
	sentAt time.Time
	err    error
}

// TestStressSwaps drives a burst of simultaneous offers followed by a
// sustained drip, then reports how the solver coped.
func TestStressSwaps(t *testing.T) {
	ctx := t.Context()

	burst := envInt(t, "STRESS_BURST", 100)
	sustained := envInt(t, "STRESS_SUSTAINED", 30)
	rate := envDur(t, "STRESS_RATE", 2*time.Second)
	settle := envDur(t, "STRESS_SETTLE", 3*time.Minute)
	require.Positive(t, burst, "STRESS_BURST must be > 0")
	total := burst + sustained

	t.Logf("stress config: burst=%d sustained=%d rate=%s settle=%s", burst, sustained, rate, settle)

	// --- asset, funding, market -----------------------------------------

	issuer := setupArkClient(t)
	faucetOffchain(t, issuer, 0.01)
	supply := uint64(total) * stressOfferSats * 4
	assetID := issueAsset(t, issuer, supply)

	// A single transfer, so the solver's asset balance is one VTXO.
	fundSolverAsset(t, ctx, issuer, assetID, supply/2)
	// Each fulfillment costs the solver a dust BTC carrier for the asset
	// payout and credits it the 500 sat deposit. 5k/offer is generous
	// headroom so an underfunded harness can't look like a solver failure.
	ensureSolverBTC(t, ctx, uint64(total)*5000)

	addMarket(t, swap.Market{
		BaseAsset:      "BTC",
		QuoteAsset:     assetID,
		MinQuoteAmount: 1,
		MaxQuoteAmount: 100000000,
		MinBaseAmount:  1,
		MaxBaseAmount:  100000000,
		PriceFeed:      mockBTCAssetPriceFeed,
		PricePath:      mockBTCAssetPricePath,
	})

	before := solverBalance(t, ctx)
	restartsBefore := solverRestartCount(ctx)

	// --- makers ----------------------------------------------------------

	wantAssetID, err := asset.NewAssetIdFromString(assetID)
	require.NoError(t, err)
	offerParams := contract.CreateOfferParams{WantAmount: stressOfferSats, WantAsset: wantAssetID}
	emulator := newEmulatorClient(t)

	t.Logf("building %d maker wallets...", burst)
	makers := newMakerWallets(t, ctx, burst)

	// Poll for trade rows throughout, so latencies are measured against the
	// moment each row appears rather than the end of the last phase.
	trades := startCollector(t, ctx, int32(total*2+100))

	// --- phase 1: burst ---------------------------------------------------

	// Offer packets are built up front so the burst measures fulfillment,
	// not offer construction.
	offers := make([]*contract.CreateOfferResult, burst)
	offerErrs := make([]error, burst)
	runParallel(burst, stressSetupParallelism, func(i int) {
		offers[i], offerErrs[i] = contract.CreateOffer(ctx, offerParams, makers[i], emulator)
	})
	for i, err := range offerErrs {
		require.NoErrorf(t, err, "create burst offer %d", i)
	}

	t.Logf("firing %d simultaneous offers...", burst)
	subs := make([]submission, 0, total)
	burstSubs := make([]submission, burst)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range burst {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			burstSubs[i] = sendOffer(ctx, makers[i], offers[i], "burst")
		}()
	}
	close(start)
	wg.Wait()
	subs = append(subs, burstSubs...)

	// --- phase 2: sustained drip ------------------------------------------

	if sustained > 0 {
		t.Logf("dripping %d offers at 1 per %s...", sustained, rate)
		dripSubs := make([]submission, sustained)
		ticker := time.NewTicker(rate)
		var dripWg sync.WaitGroup
		for i := range sustained {
			<-ticker.C
			dripWg.Add(1)
			go func() {
				defer dripWg.Done()
				w := makers[i%len(makers)]
				offer, err := contract.CreateOffer(ctx, offerParams, w, emulator)
				if err != nil {
					dripSubs[i] = submission{phase: "sustained", err: err}
					return
				}
				dripSubs[i] = sendOffer(ctx, w, offer, "sustained")
			}()
		}
		dripWg.Wait()
		ticker.Stop()
		subs = append(subs, dripSubs...)
	}

	// --- collect and report -----------------------------------------------

	rows := trades.wait(t, subs, settle)
	report(t, subs, rows, before, solverBalance(t, ctx), assetID,
		restartsBefore, solverRestartCount(ctx))

	// The only hard assertion: the solver is still answering.
	status, err := dialSwapClient(t).GetStatus(ctx, &swapv1.GetStatusRequest{})
	require.NoError(t, err, "solverd unreachable after stress run")
	require.True(t, status.GetRunning(), "solverd is not running after stress run")
}

// sendOffer funds an offer's swap address and timestamps the submission.
func sendOffer(
	ctx context.Context, w arksdk.Wallet, offer *contract.CreateOfferResult, phase string,
) submission {
	sentAt := time.Now()
	txid, err := w.SendOffChain(ctx,
		[]clientTypes.Receiver{{To: offer.SwapAddress, Amount: stressOfferSats}},
		arksdk.WithExtraPacket(offer.Packet),
	)
	return submission{phase: phase, txid: txid, sentAt: sentAt, err: err}
}

// tradeRow is a trade plus when the poller first observed it.
type tradeRow struct {
	trade *swapv1.TradeInfo
	seen  time.Time
}

// collector polls ListTrades in the background for the whole run, so a trade
// row is timestamped when it appears — not when the last phase happens to
// finish. Without this every burst latency would include the drip duration.
type collector struct {
	mu   sync.Mutex
	rows map[string]tradeRow
	stop chan struct{}
	done chan struct{}
}

func startCollector(t *testing.T, ctx context.Context, limit int32) *collector {
	t.Helper()
	client := dialSwapClient(t)
	c := &collector{
		rows: make(map[string]tradeRow),
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	ticker := time.NewTicker(time.Second)
	go func() {
		defer close(c.done)
		defer ticker.Stop()
		for {
			resp, err := client.ListTrades(ctx, &swapv1.ListTradesRequest{Limit: limit})
			if err == nil {
				now := time.Now()
				c.mu.Lock()
				for _, tr := range resp.GetTrades() {
					seen, ok := c.rows[tr.GetOfferTxid()]
					if !ok {
						seen.seen = now
					}
					seen.trade = tr
					c.rows[tr.GetOfferTxid()] = seen
				}
				c.mu.Unlock()
			}
			select {
			case <-c.stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return c
}

// wait blocks until every submitted offer has a trade row or settle expires,
// then shuts the poller down and returns what it saw.
func (c *collector) wait(
	t *testing.T, subs []submission, settle time.Duration,
) map[string]tradeRow {
	t.Helper()
	deadline := time.Now().Add(settle)
	for {
		c.mu.Lock()
		missing := 0
		for _, s := range subs {
			if s.err != nil || s.txid == "" {
				continue
			}
			if _, ok := c.rows[s.txid]; !ok {
				missing++
			}
		}
		c.mu.Unlock()

		if missing == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Logf("settle timeout: %d offers still have no trade row", missing)
			break
		}
		time.Sleep(time.Second)
	}

	close(c.stop)
	<-c.done

	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rows
}

func report(
	t *testing.T,
	subs []submission,
	rows map[string]tradeRow,
	before, after *swapv1.GetBalanceResponse,
	assetID string,
	restartsBefore, restartsAfter string,
) {
	t.Helper()

	var sendErrs, fulfilled, failed, orphan int
	latencies := make([]time.Duration, 0, len(subs))
	errCounts := make(map[string]int)

	// sent, fulfilled, failed, send errors, orphans — per phase, because
	// "how did the burst do vs the drip" is the whole point of the run.
	const (
		pSent = iota
		pFulfilled
		pFailed
		pSendErr
		pOrphan
	)
	byPhase := make(map[string][5]int)

	for _, s := range subs {
		p := byPhase[s.phase]
		p[pSent]++
		switch {
		case s.err != nil:
			sendErrs++
			p[pSendErr]++
			errCounts["SEND: "+normalizeErr(s.err.Error())]++
		default:
			row, ok := rows[s.txid]
			if !ok {
				orphan++
				p[pOrphan]++
				break
			}
			latencies = append(latencies, row.seen.Sub(s.sentAt))
			if row.trade.GetError() == "" {
				fulfilled++
				p[pFulfilled]++
			} else {
				failed++
				p[pFailed]++
				errCounts[normalizeErr(row.trade.GetError())]++
			}
		}
		byPhase[s.phase] = p
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\n=== solverd stress report ===\n")
	fmt.Fprintf(&b, "submitted:      %d\n", len(subs))
	fmt.Fprintf(&b, "  send errors:  %d\n", sendErrs)
	fmt.Fprintf(&b, "trade rows:     %d (total in db, including earlier runs)\n", len(rows))
	fmt.Fprintf(&b, "fulfilled:      %d\n", fulfilled)
	fmt.Fprintf(&b, "failed:         %d\n", failed)
	fmt.Fprintf(&b, "no trade row:   %d  <- offer landed but solver never recorded an attempt\n", orphan)

	for _, phase := range []string{"burst", "sustained"} {
		if p, ok := byPhase[phase]; ok {
			fmt.Fprintf(&b, "  %-10s sent=%d fulfilled=%d failed=%d send-err=%d no-row=%d\n",
				phase, p[pSent], p[pFulfilled], p[pFailed], p[pSendErr], p[pOrphan])
		}
	}

	if len(latencies) > 0 {
		slices.Sort(latencies)
		fmt.Fprintf(&b, "latency (submit -> trade row, ~2s poll resolution):\n")
		fmt.Fprintf(&b, "  p50=%s p90=%s max=%s\n",
			pct(latencies, 50).Round(time.Millisecond),
			pct(latencies, 90).Round(time.Millisecond),
			latencies[len(latencies)-1].Round(time.Millisecond))
	}

	fmt.Fprintf(&b, "solver balance:\n")
	fmt.Fprintf(&b, "  btc settled   %d -> %d (%+d)\n",
		before.GetOffchainSettled(), after.GetOffchainSettled(),
		int64(after.GetOffchainSettled())-int64(before.GetOffchainSettled()))
	fmt.Fprintf(&b, "  btc pending   %d -> %d\n", before.GetOffchainPending(), after.GetOffchainPending())
	fmt.Fprintf(&b, "  asset         %d -> %d (%+d, expected %+d for %d fulfilled)\n",
		before.GetAssetBalances()[assetID], after.GetAssetBalances()[assetID],
		int64(after.GetAssetBalances()[assetID])-int64(before.GetAssetBalances()[assetID]),
		-int64(fulfilled)*stressOfferSats, fulfilled)
	fmt.Fprintf(&b, "solverd container restarts: %s -> %s\n", restartsBefore, restartsAfter)

	if len(errCounts) > 0 {
		fmt.Fprintf(&b, "errors (hex/digits normalized):\n")
		byCountDesc := func(a, c string) int { return errCounts[c] - errCounts[a] }
		for _, msg := range slices.SortedFunc(maps.Keys(errCounts), byCountDesc) {
			fmt.Fprintf(&b, "  %4dx  %s\n", errCounts[msg], msg)
		}
	}
	t.Log(b.String())
}

func pct(sorted []time.Duration, p int) time.Duration {
	i := len(sorted) * p / 100
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}

var (
	hexRe   = regexp.MustCompile(`[0-9a-f]{16,}`)
	digitRe = regexp.MustCompile(`\d+`)
)

// normalizeErr collapses txids and amounts so error variants group together.
func normalizeErr(s string) string {
	return digitRe.ReplaceAllString(hexRe.ReplaceAllString(s, "<hex>"), "<n>")
}

// newMakerWallets builds and funds n throwaway maker wallets concurrently.
// It uses the error-returning helpers so nothing calls t.FailNow off the test
// goroutine; cleanups are registered back on the test goroutine.
func newMakerWallets(t *testing.T, ctx context.Context, n int) []arksdk.Wallet {
	t.Helper()
	wallets := make([]arksdk.Wallet, n)
	cleanups := make([]func(), n)
	errs := make([]error, n)

	runParallel(n, stressSetupParallelism, func(i int) {
		w, cleanup, err := newFunderWallet(ctx)
		if err != nil {
			errs[i] = err
			return
		}
		if err := faucetOffchainCtx(ctx, w, stressMakerFunds); err != nil {
			cleanup()
			errs[i] = fmt.Errorf("faucet: %w", err)
			return
		}
		wallets[i], cleanups[i] = w, cleanup
	})

	for _, c := range cleanups {
		if c != nil {
			t.Cleanup(c)
		}
	}
	for i, err := range errs {
		require.NoErrorf(t, err, "build maker wallet %d", i)
	}
	return wallets
}

// runParallel runs fn(0..n-1) with at most `limit` in flight.
func runParallel(n, limit int, fn func(i int)) {
	var g errgroup.Group
	g.SetLimit(limit)
	for i := range n {
		g.Go(func() error { fn(i); return nil })
	}
	_ = g.Wait()
}

func solverBalance(t *testing.T, ctx context.Context) *swapv1.GetBalanceResponse {
	t.Helper()
	resp, err := dialWalletClient(t).GetBalance(ctx, &swapv1.GetBalanceRequest{})
	require.NoError(t, err)
	return resp
}

// ensureSolverBTC tops the solver up until it holds at least `want` sats
// offchain, so an underfunded harness can't masquerade as a solver failure.
func ensureSolverBTC(t *testing.T, ctx context.Context, want uint64) {
	t.Helper()
	for range 10 {
		bal := solverBalance(t, ctx)
		if bal.GetOffchainSettled()+bal.GetOffchainPending() >= want {
			return
		}
		require.NoError(t, fundSolver(ctx))
	}
	t.Fatalf("could not fund solver up to %d sats", want)
}

// solverRestartCount reads the container's restart counter; a bump means the
// solver crashed and docker brought it back. Returns "?" if docker isn't
// reachable, since the stack may be running outside compose.
func solverRestartCount(ctx context.Context) string {
	out, err := runCommand(ctx, "docker inspect -f '{{.RestartCount}}' solverd-solverd")
	if err != nil {
		return "?"
	}
	return strings.Trim(out, "'")
}

func envInt(t *testing.T, key string, def int) int {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	require.NoErrorf(t, err, "invalid %s", key)
	return n
}

func envDur(t *testing.T, key string, def time.Duration) time.Duration {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	require.NoErrorf(t, err, "invalid %s", key)
	return d
}
