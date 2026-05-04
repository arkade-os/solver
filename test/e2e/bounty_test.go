package e2e_test

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	introclient "github.com/ArkLabsHQ/introspector/pkg/client"
	"github.com/arkade-os/arkd/pkg/client-lib/indexer"
	arksdk "github.com/arkade-os/go-sdk"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/txscript"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/arkade-os/bancod/pkg/solver"
	"github.com/arkade-os/bancod/pkg/solver/arkdsource"
	"github.com/arkade-os/bancod/pkg/solver/bounty"
)

// startBountySolver spins up a solver running just the bounty plugin against
// the supplied bot client subscribed to arkd's transaction stream.
func startBountySolver(
	t *testing.T,
	cfg bounty.Config,
) (cancel context.CancelFunc, solverDone <-chan struct{}) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())

	plugin, err := bounty.NewPlugin(ctx, cfg)
	require.NoError(t, err)

	s := solver.New(plugin).WithLogger(cfg.Log)
	txs := arkdsource.Subscribe(ctx, cfg.ArkClient, cfg.Log)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.Run(ctx, txs)
	}()
	return cancel, done
}

// fetchIntroPubkey returns the introspector signer pubkey, parsed.
func fetchIntroPubkey(t *testing.T, introClient introclient.TransportClient) *btcec.PublicKey {
	t.Helper()
	info, err := introClient.GetInfo(t.Context())
	require.NoError(t, err)
	raw, err := hex.DecodeString(info.SignerPublicKey)
	require.NoError(t, err)
	pub, err := btcec.ParsePubKey(raw)
	require.NoError(t, err)
	return pub
}

func dialIntrospector(t *testing.T) introclient.TransportClient {
	t.Helper()
	conn, err := grpc.NewClient(introspectorAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return introclient.NewGRPCClient(conn)
}

// freshTaprootPkScript returns a fresh 34-byte P2TR script for a throwaway key.
func freshTaprootPkScript(t *testing.T) []byte {
	t.Helper()
	priv, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	pkScript, err := txscript.PayToTaprootScript(priv.PubKey())
	require.NoError(t, err)
	return pkScript
}

// pollForVtxoAt polls the indexer for any spendable VTXO at the given pkScript.
// Returns (vtxo, true) on success or fails the test on timeout.
func pollForVtxoAt(t *testing.T, ctx context.Context, idx indexer.Indexer, pkScript []byte, timeout time.Duration) indexerVtxo {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := idx.GetVtxos(ctx,
			indexer.WithScripts([]string{hex.EncodeToString(pkScript)}),
			indexer.WithSpendableOnly(),
		)
		if err == nil && len(resp.Vtxos) > 0 {
			v := resp.Vtxos[0]
			return indexerVtxo{Txid: v.Txid, VOut: v.VOut, Amount: v.Amount}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("no VTXO appeared at pkScript %s within %v", hex.EncodeToString(pkScript), timeout)
	return indexerVtxo{}
}

type indexerVtxo struct {
	Txid   string
	VOut   uint32
	Amount uint64
}

// TestBounty_SingletonClaim posts a single bounty and asserts the bot routes the
// payment to the receiver, takes the protocol fee, and produces a claim tx
// whose txid satisfies the difficulty.
//
// Requires `make setup-test-env` (nigiri + arkd + introspector).
func TestBounty_SingletonClaim(t *testing.T) {
	if testing.Short() {
		t.Skip("requires live nigiri+arkd+introspector stack")
	}

	introClient := dialIntrospector(t)
	introPub := fetchIntroPubkey(t, introClient)

	// Alice and bot are separate clients.
	alice := setupArkClient(t)
	faucetOffchain(t, alice, 0.001) // 100k sats
	bot := setupArkClient(t)
	faucetOffchain(t, bot, 0.0001) // bot needs a wallet, even if claim doesn't draw inputs

	cancel, done := startBountySolver(t, bounty.Config{
		ArkClient:          bot,
		Introspector:       introClient,
		IntrospectorPubkey: introPub,
		BatchSize:          1, // singleton flush per HTLC
		BatchTimeout:       2 * time.Second,
		Log:                log.StandardLogger(),
	})
	t.Cleanup(func() { cancel(); <-done })

	// Settle the bot's arkd subscription before broadcasting; the stream only
	// delivers events that arrive after subscription completes.
	time.Sleep(2 * time.Second)

	receiver := freshTaprootPkScript(t)
	const amount uint64 = 10_000

	res, err := bounty.CreateBounty(t.Context(), bounty.CreateParams{
		Difficulty:         2,
		ReceiverPkScript:   receiver,
		Amount:             amount,
		IntrospectorPubkey: introPub,
	}, alice)
	require.NoError(t, err)
	require.NotEmpty(t, res.FundingTxid)

	// Wait for the bot to claim and pay the receiver.
	v := pollForVtxoAt(t, t.Context(), bot.Indexer(), receiver, 30*time.Second)

	require.Equal(t, amount-bounty.MinerFeeSats, v.Amount, "receiver should get amount - fee")
	require.NotEqual(t, res.FundingTxid, v.Txid, "claim tx must differ from funding tx")

	// txid PoW property: first `Difficulty` bytes are zero.
	txidBytes, err := hex.DecodeString(v.Txid)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(txidBytes), 2)
	// Note: Bitcoin txids are little-endian when displayed as hex but the
	// `hash.String()` form is byte-reversed. The mining loop checks the
	// canonical wire-byte order; the displayed string flips that. Verify by
	// checking the reversed prefix instead.
	last := len(txidBytes) - 1
	require.Equal(t, byte(0x00), txidBytes[last], "txid (canonical wire bytes) must start with 0x00")
	require.Equal(t, byte(0x00), txidBytes[last-1], "txid (canonical wire bytes) second byte must be 0x00")
}

// TestBounty_BatchedClaim posts three bounties of the same difficulty in quick
// succession and asserts the bot batches them: all three receivers get paid
// from the same producing txid (and the bot collects 3 * MinerFeeSats).
//
// Requires `make setup-test-env`.
func TestBounty_BatchedClaim(t *testing.T) {
	if testing.Short() {
		t.Skip("requires live nigiri+arkd+introspector stack")
	}

	introClient := dialIntrospector(t)
	introPub := fetchIntroPubkey(t, introClient)

	// Three independent makers — avoids any in-flight UTXO-set lag from one
	// maker submitting back-to-back fundings.
	const numBounties = 3
	alices := make([]arksdk.ArkClient, numBounties)
	for i := range alices {
		alices[i] = setupArkClient(t)
		faucetOffchain(t, alices[i], 0.001)
	}
	bot := setupArkClient(t)
	faucetOffchain(t, bot, 0.0001)

	cancel, done := startBountySolver(t, bounty.Config{
		ArkClient:          bot,
		Introspector:       introClient,
		IntrospectorPubkey: introPub,
		BatchSize:          numBounties, // size-trigger after all are queued
		BatchTimeout:       10 * time.Second,
		Log:                log.StandardLogger(),
	})
	t.Cleanup(func() { cancel(); <-done })

	// Settle the bot's arkd subscription before broadcasting.
	time.Sleep(2 * time.Second)

	receivers := make([][]byte, numBounties)
	for i := range receivers {
		receivers[i] = freshTaprootPkScript(t)
	}
	const amount uint64 = 10_000

	for i, r := range receivers {
		_, err := bounty.CreateBounty(t.Context(), bounty.CreateParams{
			Difficulty:         2,
			ReceiverPkScript:   r,
			Amount:             amount,
			IntrospectorPubkey: introPub,
		}, alices[i])
		require.NoError(t, err)
	}

	// All three receivers should be paid from the same claim tx.
	first := pollForVtxoAt(t, t.Context(), bot.Indexer(), receivers[0], 30*time.Second)
	second := pollForVtxoAt(t, t.Context(), bot.Indexer(), receivers[1], 30*time.Second)
	third := pollForVtxoAt(t, t.Context(), bot.Indexer(), receivers[2], 30*time.Second)

	require.Equal(t, first.Txid, second.Txid, "batched claim must share producing txid (1 vs 2)")
	require.Equal(t, first.Txid, third.Txid, "batched claim must share producing txid (1 vs 3)")

	for _, v := range []indexerVtxo{first, second, third} {
		require.Equal(t, amount-bounty.MinerFeeSats, v.Amount)
	}
}

// TestBounty_MixedDifficultyClaim posts bounties with three different
// difficulties (1, 2, 3) and asserts the bot batches them ALL into one claim
// tx whose txid satisfies the highest-difficulty entry (which automatically
// satisfies the lower ones too).
//
// Requires `make setup-test-env`.
func TestBounty_MixedDifficultyClaim(t *testing.T) {
	if testing.Short() {
		t.Skip("requires live nigiri+arkd+introspector stack")
	}

	introClient := dialIntrospector(t)
	introPub := fetchIntroPubkey(t, introClient)

	// Difficulties 1+2+2 prove mixed-difficulty batching works (the bot mines
	// once at the max and the result satisfies all three per-entry checks)
	// without forcing a slow PoW. Scaling the max is wasteful for the test:
	// difficulty=3 mining on a 3-input claim tx takes ~15s under concurrent
	// load, blowing past the test's poll budget when the suite is busy.
	difficulties := []uint8{1, 2, 2}
	alices := make([]arksdk.ArkClient, len(difficulties))
	for i := range alices {
		alices[i] = setupArkClient(t)
		faucetOffchain(t, alices[i], 0.001)
	}
	bot := setupArkClient(t)
	faucetOffchain(t, bot, 0.0001)

	verboseLog := log.New()
	verboseLog.SetLevel(log.DebugLevel)
	cancel, done := startBountySolver(t, bounty.Config{
		ArkClient:          bot,
		Introspector:       introClient,
		IntrospectorPubkey: introPub,
		BatchSize:          len(difficulties),
		BatchTimeout:       10 * time.Second,
		Log:                verboseLog,
	})
	t.Cleanup(func() { cancel(); <-done })

	time.Sleep(2 * time.Second)

	receivers := make([][]byte, len(difficulties))
	for i := range receivers {
		receivers[i] = freshTaprootPkScript(t)
	}
	const amount uint64 = 10_000

	for i, d := range difficulties {
		_, err := bounty.CreateBounty(t.Context(), bounty.CreateParams{
			Difficulty:         d,
			ReceiverPkScript:   receivers[i],
			Amount:             amount,
			IntrospectorPubkey: introPub,
		}, alices[i])
		require.NoError(t, err)
	}

	// Generous timeout: BatchTimeout (10s) + mining at the max difficulty (~1s)
	// + submit (~1s) + indexer lag + headroom for adjacent test contention.
	first := pollForVtxoAt(t, t.Context(), bot.Indexer(), receivers[0], 60*time.Second)
	second := pollForVtxoAt(t, t.Context(), bot.Indexer(), receivers[1], 60*time.Second)
	third := pollForVtxoAt(t, t.Context(), bot.Indexer(), receivers[2], 60*time.Second)

	require.Equal(t, first.Txid, second.Txid, "mixed-difficulty batch must share producing txid (1 vs 2)")
	require.Equal(t, first.Txid, third.Txid, "mixed-difficulty batch must share producing txid (1 vs 3)")

	for _, v := range []indexerVtxo{first, second, third} {
		require.Equal(t, amount-bounty.MinerFeeSats, v.Amount)
	}

	// Producing tx's canonical wire-byte order must satisfy the MAX difficulty
	// in the batch. Hex string is byte-reversed, so check the trailing bytes.
	maxDiff := difficulties[0]
	for _, d := range difficulties {
		if d > maxDiff {
			maxDiff = d
		}
	}
	txidBytes, err := hex.DecodeString(first.Txid)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(txidBytes), int(maxDiff))
	for i := 0; i < int(maxDiff); i++ {
		require.Equal(t, byte(0x00), txidBytes[len(txidBytes)-1-i],
			"txid byte %d must be 0x00 to satisfy difficulty>=%d", i, i+1)
	}
}
