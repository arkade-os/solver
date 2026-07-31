// Command banco is a test-only CLI for non-interactive swaps. Do not use with real funds.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/arkade-os/arkd/pkg/ark-lib/asset"
	"github.com/arkade-os/arkd/pkg/client-lib/indexer"
	clientTypes "github.com/arkade-os/arkd/pkg/client-lib/types"
	emulatorclient "github.com/arkade-os/emulator/pkg/client"
	arksdk "github.com/arkade-os/go-sdk"
	sdktypes "github.com/arkade-os/go-sdk/types"
	"github.com/mdp/qrterminal/v3"
	"github.com/urfave/cli/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/arkade-os/solver/pkg/swap/contract"
	"github.com/arkade-os/solver/pkg/swap/pricefeed"
)

var Version string

func main() {
	app := &cli.App{
		Name:    "banco",
		Usage:   "testing CLI: perform the maker side of a banco swap for a market",
		Version: Version,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name: "market", Required: true,
				Usage: "market pair in canonical {base}/{quote} format (as the solver lists it), each side \"BTC\" or a hex asset ID",
			},
			&cli.StringFlag{
				Name: "side", Required: true,
				Usage: "sell = deposit base, receive quote; buy = deposit quote, receive base",
			},
			&cli.StringFlag{
				Name: "price-feed", Required: true,
				Usage: "price feed URL (same format as a solverd pair); read as quote-per-base",
			},
			&cli.StringFlag{
				Name:  "price-path",
				Usage: "JSON pointer to the price in the feed response, e.g. /bitcoin/usd (empty = guess from the feed host)",
			},
			&cli.StringFlag{
				Name: "arkd", Value: "localhost:7070",
				Usage: "arkd server address",
			},
			&cli.StringFlag{
				Name: "emulator", Value: "localhost:7173",
				Usage: "emulator address; host:port dials gRPC, http(s):// uses the REST API",
			},
			&cli.IntFlag{
				Name: "count", Value: 1,
				Usage: "run this many swaps in parallel, splitting the deposit evenly across them",
			},
			&cli.DurationFlag{
				Name: "timeout", Value: 0,
				Usage: "give up waiting for a taker after this long (0 = wait forever); an unfulfilled swap's deposit stays reclaimable at its swap address",
			},
			&cli.UintFlag{
				Name: "fee-bps", Value: 0,
				Usage: "the market's maker fee in bps (from the solver card's fee_bps); the offer requests fewer quote units by this fee so it clears",
			},
			&cli.StringFlag{
				Name: "payout", Required: true,
				Usage: "offchain address that receives the quote after each swap fulfills, so nothing is left in the ephemeral wallet",
			},
		},
		Action: run,
	}

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
}

func run(c *cli.Context) error {
	ctx, stop := signal.NotifyContext(c.Context, os.Interrupt, syscall.SIGTERM)
	defer stop()

	marketBase, marketQuote, ok := splitPair(c.String("market"))
	if !ok {
		return fmt.Errorf("market must be in format 'base/quote'")
	}
	// Direction, solver-style: sell = deposit base / receive quote, buy = deposit
	// quote / receive base. The feed always reads quote-per-base, so which side we
	// deposit is the only thing that flips — no user-facing price inversion.
	var depositAsset, receiveAsset string
	switch c.String("side") {
	case "sell":
		depositAsset, receiveAsset = marketBase, marketQuote
	case "buy":
		depositAsset, receiveAsset = marketQuote, marketBase
	default:
		return fmt.Errorf("side must be 'buy' or 'sell'")
	}
	var wantAsset *asset.AssetId
	if receiveAsset != "BTC" {
		var err error
		if wantAsset, err = asset.NewAssetIdFromString(receiveAsset); err != nil {
			return fmt.Errorf("invalid receive asset: %w", err)
		}
	}

	// Ephemeral wallet: receives the QR payment, funds the swap address with
	// the offer packet attached (normal wallets can't), and collects the
	// payout once a taker fulfills.
	datadir, err := os.MkdirTemp("", "banco-*")
	if err != nil {
		return err
	}

	// nolint:errcheck
	defer os.RemoveAll(datadir)

	wallet, err := arksdk.NewWallet(datadir, arksdk.WithoutAutoSettle())
	if err != nil {
		return fmt.Errorf("failed to create wallet: %w", err)
	}
	defer wallet.Stop()

	password := randomHex(16)
	if err := wallet.Init(ctx, c.String("arkd"), "", password); err != nil {
		return fmt.Errorf("failed to init wallet: %w", err)
	}
	if err := wallet.Unlock(ctx, password); err != nil {
		return fmt.Errorf("failed to unlock wallet: %w", err)
	}
	synced := <-wallet.IsSynced(ctx)
	if synced.Err != nil {
		return fmt.Errorf("failed to sync wallet: %w", synced.Err)
	}

	recoveryKey, err := wallet.Dump(ctx)
	if err != nil {
		return fmt.Errorf("failed to dump recovery key: %w", err)
	}
	fmt.Println("Recovery key (keep it: only way to recover funds if this process dies):")
	fmt.Println(recoveryKey)
	fmt.Println()

	relayAddress, err := wallet.NewOffchainAddress(ctx)
	if err != nil {
		return fmt.Errorf("failed to get funding address: %w", err)
	}

	// Subscribe once; the same channel serves the deposit and fulfillment waits.
	vtxoCh := wallet.GetVtxoEventChannel(ctx)

	qrterminal.GenerateHalfBlock(relayAddress, qrterminal.L, os.Stdout)
	fmt.Printf("\nSend any amount of %s to:\n%s\n\nWaiting for deposit...\n",
		assetLabel(depositAsset), relayAddress)

	// The deposit is whatever the first payment carries; the offer is priced
	// afterwards, at the feed price current when the funds arrive.
	btcReceived, deposit, err := waitForDeposit(ctx, vtxoCh, depositAsset)
	if err != nil {
		return err
	}

	// The feed reads quote-per-base. computeWantAmount wants deposit-per-receive,
	// so invert on sell (deposit is base) and use it directly on buy (deposit is
	// quote). This is where --invert-price used to live; --side now decides it.
	feedPrice, err := pricefeed.New().Fetch(ctx, c.String("price-feed"), c.String("price-path"))
	if err != nil {
		return fmt.Errorf("failed to fetch price: %w", err)
	}
	price := feedPrice
	if c.String("side") == "sell" {
		price = 1 / feedPrice
	}

	depositDec, err := assetDecimals(ctx, wallet.Indexer(), depositAsset)
	if err != nil {
		return err
	}
	receiveDec, err := assetDecimals(ctx, wallet.Indexer(), receiveAsset)
	if err != nil {
		return err
	}
	emulator, closeEmulator, err := newEmulatorClient(c.String("emulator"))
	if err != nil {
		return fmt.Errorf("failed to connect to emulator: %w", err)
	}
	defer closeEmulator()

	cfg := swapConfig{
		deposit: depositAsset, receive: receiveAsset, wantAsset: wantAsset, price: price,
		depositDec: depositDec, receiveDec: receiveDec, wallet: wallet, emulator: emulator,
		timeout: c.Duration("timeout"), feeBps: c.Uint("fee-bps"),
		payout: c.String("payout"),
	}

	count := max(c.Int("count"), 1)

	if count == 1 {
		want, err := cfg.wantAmount(deposit)
		if err != nil {
			return err
		}
		fmt.Printf("\nOffer: deposit %d %s -> receive %d %s (price %g quote/base)\nWaiting for a taker to fulfill...\n",
			deposit, assetLabel(depositAsset), want, assetLabel(receiveAsset), feedPrice)
		res := runOneSwap(ctx, cfg, deposit, btcReceived)
		if res.err != nil {
			printStranded(res)
			reportSweep(sweepToPayout(ctx, cfg))
			return res.err
		}
		fmt.Printf("Swap fulfilled: received %d %s (txid %s).\n", res.want, assetLabel(receiveAsset), res.txid)
		if _, err := sweepToPayout(ctx, cfg); err != nil {
			return fmt.Errorf("forwarding to %s failed: %w — the %d %s is in the ephemeral wallet, recover it with the printed key",
				cfg.payout, err, res.want, assetLabel(receiveAsset))
		}
		fmt.Printf("Forwarded %d %s to %s.\n", res.want, assetLabel(receiveAsset), cfg.payout)
		return nil
	}

	// Parallel: split the single manual deposit across `count` independent
	// offers from this one wallet. All payouts land back here; one final sweep
	// forwards them (and any leftover from failed swaps) to payout at once.
	if deposit/uint64(count) == 0 {
		return fmt.Errorf("deposit %d %s is too small to split across %d swaps", deposit, assetLabel(depositAsset), count)
	}
	// Each swap spends the wallet's whole balance and returns the rest as change,
	// so the deposits must sum to the full amount: an unallocated remainder lands
	// as a sub-dust change on whichever swap runs last, which CoinSelect drops to
	// 0 and unbalances the ark tx ("input amount is not equal to output amount").
	deposits := splitEvenly(deposit, count)
	carriers := splitEvenly(btcReceived, count)
	fmt.Printf("\nSplitting %d %s across %d parallel swaps (%d each)...\n\n",
		deposit, assetLabel(depositAsset), count, deposit/uint64(count))

	results := make([]swapResult, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Go(func() {
			results[i] = runOneSwap(ctx, cfg, deposits[i], carriers[i])
		})
	}
	wg.Wait()

	var fulfilled int
	var totalElapsed time.Duration
	for i, r := range results {
		if r.err != nil {
			fmt.Printf("  swap %2d: FAILED: %v\n", i, r.err)
			printStranded(r)
			continue
		}
		fulfilled++
		totalElapsed += r.elapsed
		fmt.Printf("  swap %2d: fulfilled %d %s in %s (txid %s)\n",
			i, r.want, assetLabel(receiveAsset), r.elapsed.Round(time.Millisecond), r.txid)
	}
	fmt.Printf("\n%d/%d fulfilled", fulfilled, count)
	if fulfilled > 0 {
		fmt.Printf(", avg %s", (totalElapsed / time.Duration(fulfilled)).Round(time.Millisecond))
	}
	fmt.Println()
	reportSweep(sweepToPayout(ctx, cfg))
	if fulfilled < count {
		return fmt.Errorf("%d/%d swaps did not fulfill", count-fulfilled, count)
	}
	return nil
}

// swapConfig is the shared, per-run context every swap needs. deposit/receive
// are the assets this side sends and gets back (which market side that is
// depends on --side); price is deposit-per-receive, already oriented for the
// chosen direction.
type swapConfig struct {
	deposit, receive       string
	wantAsset              *asset.AssetId
	price                  float64
	depositDec, receiveDec int
	wallet                 arksdk.Wallet
	emulator               emulatorclient.TransportClient
	// timeout bounds the wait for a taker; 0 waits forever.
	timeout time.Duration
	// feeBps is the market's maker fee; the requested quote amount is divided by
	// (1 + fee) to match the solver's price check. 0 requests the raw feed price.
	feeBps uint
	// payout, if set, receives the swap's quote payout so nothing stays in the
	// ephemeral wallet.
	payout string
}

// wantAmount converts a deposit into the quote units to request. The solver
// inflates the offer price by feeBps, so we divide the raw amount by (1 + fee)
// to land exactly on the accepted price; server-side tolerance absorbs drift.
func (cfg swapConfig) wantAmount(deposit uint64) (uint64, error) {
	want, err := computeWantAmount(deposit, cfg.depositDec, cfg.receiveDec, cfg.price)
	if err != nil {
		return 0, err
	}
	if cfg.feeBps > 0 {
		want = want * 10000 / (10000 + uint64(cfg.feeBps))
		if want < 1 {
			return 0, fmt.Errorf("want rounds to 0 after %d bps fee", cfg.feeBps)
		}
	}
	return want, nil
}

// splitEvenly divides total into count parts that sum to exactly total, folding
// the remainder into the first part.
func splitEvenly(total uint64, count int) []uint64 {
	parts := make([]uint64, count)
	per := total / uint64(count)
	for i := range parts {
		parts[i] = per
	}
	parts[0] += total % uint64(count)
	return parts
}

type swapResult struct {
	want    uint64
	txid    string
	elapsed time.Duration
	err     error
	// funded is true once the deposit has been sent to swapAddress. When a
	// funded swap fails to fulfill, its deposit sits at the covenant swap
	// address and can only be reclaimed with the recovery key plus offerHex.
	funded      bool
	swapAddress string
	offerHex    string
}

// runOneSwap creates one offer, funds its swap address with depositAmount (base
// units; carrierSats rides along as the BTC carrier for asset deposits), and
// blocks until the taker's payout lands at this offer's own MakerPkScript.
func runOneSwap(ctx context.Context, cfg swapConfig, depositAmount, carrierSats uint64) swapResult {
	start := time.Now()
	res := swapResult{}
	fail := func(err error) swapResult { res.err, res.elapsed = err, time.Since(start); return res }

	want, err := cfg.wantAmount(depositAmount)
	if err != nil {
		return fail(err)
	}
	res.want = want
	offer, err := contract.CreateOffer(ctx, contract.CreateOfferParams{
		WantAmount: want, WantAsset: cfg.wantAsset,
	}, cfg.wallet, cfg.emulator)
	if err != nil {
		return fail(fmt.Errorf("create offer: %w", err))
	}
	res.swapAddress, res.offerHex = offer.SwapAddress, offer.OfferHex

	// Subscribe before funding so a fast fulfillment can't be missed.
	vtxoCh := cfg.wallet.GetVtxoEventChannel(ctx)

	receiver := clientTypes.Receiver{To: offer.SwapAddress, Amount: depositAmount}
	if cfg.deposit != "BTC" {
		// Asset deposit: the BTC is just the carrier; the swap amount rides the packet.
		receiver.Amount = carrierSats
		receiver.Assets = []clientTypes.Asset{{AssetId: cfg.deposit, Amount: depositAmount}}
	}
	if _, err := cfg.wallet.SendOffChain(
		ctx, []clientTypes.Receiver{receiver}, arksdk.WithExtraPacket(offer.Packet),
	); err != nil {
		return fail(fmt.Errorf("fund swap: %w", err))
	}
	// From here the deposit is committed at the swap address.
	res.funded = true

	waitCtx := ctx
	if cfg.timeout > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, cfg.timeout)
		defer cancel()
	}
	txid, err := waitForFulfillment(waitCtx, vtxoCh, cfg.receive, want, offer.MakerPkScript)
	if err != nil {
		return fail(err)
	}
	res.txid = txid
	// The payout stays in the ephemeral wallet; a single sweepToPayout after all
	// swaps finish forwards everything at once. Forwarding here would race the
	// other swaps' whole-wallet sends (VTXO_ALREADY_SPENT).
	res.elapsed = time.Since(start)
	return res
}

// sweepToPayout forwards the wallet's whole spendable balance (all swap payouts
// plus any leftover from failed swaps) to cfg.payout in one send, so nothing is
// left behind the recovery key. It runs once, after all swaps finish, so it
// never races the sibling sends that a per-swap forward would (VTXO_ALREADY_SPENT).
// A freshly-landed payout can take a beat to become spendable, so it retries.
// Recoverable vtxos and deposits committed to a swap address can't be moved with
// a plain send; those still need the recovery key (see printStranded). Returns
// the sats swept.
func sweepToPayout(ctx context.Context, cfg swapConfig) (uint64, error) {
	var lastErr error
	for range 5 {
		bal, err := cfg.wallet.Balance(ctx)
		if err != nil {
			return 0, err
		}
		btc := bal.OffchainBalance.Total - bal.OffchainBalance.Recoverable
		if btc == 0 && len(bal.AssetBalances) == 0 {
			return 0, nil
		}
		r := clientTypes.Receiver{To: cfg.payout, Amount: btc}
		for id, amt := range bal.AssetBalances {
			if amt > 0 {
				r.Assets = append(r.Assets, clientTypes.Asset{AssetId: id, Amount: amt})
			}
		}
		if _, err := cfg.wallet.SendOffChain(ctx, []clientTypes.Receiver{r}); err == nil {
			return btc, nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return 0, lastErr
}

// reportSweep prints the outcome of a sweepToPayout call.
func reportSweep(swept uint64, err error) {
	switch {
	case err != nil:
		fmt.Printf("    ⚠ returning leftover to payout failed: %v — use the recovery key above\n", err)
	case swept > 0:
		fmt.Printf("    returned %d sats of leftover to payout\n", swept)
	}
}

// printStranded warns about a funded-but-unfulfilled swap and prints exactly
// what's needed to reclaim the deposit: the swap address and the offer hex
// (the recovery key was printed at startup).
func printStranded(r swapResult) {
	if !r.funded {
		return
	}
	fmt.Printf("    ⚠ deposit stranded at swap address %s\n      reclaim with the recovery key above + offer: %s\n",
		r.swapAddress, r.offerHex)
}

// waitForDeposit blocks until the wallet receives a payment carrying the base
// asset and returns the BTC received alongside it and the deposit amount (the
// BTC itself for BTC markets, the base-asset amount otherwise).
// ponytail: the first payment wins; anything sent later stays in the
// ephemeral wallet (recoverable via the printed key).
func waitForDeposit(
	ctx context.Context, vtxoCh <-chan sdktypes.VtxoEvent, base string,
) (btcTotal, deposit uint64, err error) {
	for {
		ev, err := nextVtxosAdded(ctx, vtxoCh)
		if err != nil {
			return 0, 0, err
		}
		var assetTotal uint64
		for _, v := range ev {
			fmt.Printf("Deposit received: txid %s\n", v.Txid)
			btcTotal += v.Amount
			for _, a := range v.Assets {
				if a.AssetId == base {
					assetTotal += a.Amount
				}
			}
		}
		if base == "BTC" && btcTotal > 0 {
			return btcTotal, btcTotal, nil
		}
		if base != "BTC" && assetTotal > 0 {
			return btcTotal, assetTotal, nil
		}
	}
}

// waitForFulfillment blocks until the taker's payout lands at makerPkScript,
// the fresh payout address of this specific offer. Matching on the script keeps
// concurrent swaps that share one wallet from claiming each other's payouts.
func waitForFulfillment(
	ctx context.Context, vtxoCh <-chan sdktypes.VtxoEvent, quote string, want uint64, makerPkScript []byte,
) (string, error) {
	target := hex.EncodeToString(makerPkScript)
	for {
		vtxos, err := nextVtxosAdded(ctx, vtxoCh)
		if err != nil {
			return "", err
		}
		for _, v := range vtxos {
			if v.Script != target {
				continue
			}
			if quote == "BTC" && v.Amount >= want {
				return v.Txid, nil
			}
			for _, a := range v.Assets {
				if a.AssetId == quote {
					return v.Txid, nil
				}
			}
		}
	}
}

func nextVtxosAdded(
	ctx context.Context, vtxoCh <-chan sdktypes.VtxoEvent,
) ([]clientTypes.Vtxo, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case ev, ok := <-vtxoCh:
			if !ok {
				return nil, fmt.Errorf("vtxo event channel closed")
			}
			if ev.Type == sdktypes.VtxosAdded && len(ev.Vtxos) > 0 {
				return ev.Vtxos, nil
			}
		}
	}
}

// computeWantAmount converts the deposit into quote base units at the feed
// price, where price = baseAmount/quoteAmount in human units (the same
// convention as banco.Offer.ComputePrice).
func computeWantAmount(deposit uint64, baseDec, quoteDec int, price float64) (uint64, error) {
	if price <= 0 {
		return 0, fmt.Errorf("invalid price: %g", price)
	}
	want := float64(deposit) / math.Pow10(baseDec) / price * math.Pow10(quoteDec)
	rounded := math.Round(want)
	if rounded < 1 || rounded > math.MaxUint64 {
		return 0, fmt.Errorf("want amount out of range: %g", want)
	}
	return uint64(rounded), nil
}

// assetDecimals mirrors solverd's rule: BTC has 8, assets publish a
// "decimals" metadata entry.
func assetDecimals(ctx context.Context, idx indexer.Indexer, assetID string) (int, error) {
	if assetID == "BTC" {
		return 8, nil
	}
	info, err := idx.GetAsset(ctx, assetID)
	if err != nil {
		return 0, fmt.Errorf("asset %s: %w", assetID, err)
	}
	if info == nil {
		return 0, fmt.Errorf("asset %s: not found", assetID)
	}
	for _, md := range info.Metadata {
		if string(md.Key) != "decimals" {
			continue
		}
		n, err := strconv.Atoi(string(md.Value))
		if err != nil || n < 0 {
			return 0, fmt.Errorf("asset %s: invalid decimals metadata %q", assetID, string(md.Value))
		}
		return n, nil
	}
	return 0, fmt.Errorf("asset %s: no decimals metadata", assetID)
}

func splitPair(name string) (string, string, bool) {
	parts := strings.SplitN(name, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func assetLabel(assetID string) string {
	if assetID == "BTC" {
		return "sats"
	}
	if len(assetID) > 8 {
		return assetID[:8] + "…"
	}
	return assetID
}

// newEmulatorClient returns a gRPC transport client for host:port addresses,
// or a REST-backed one for http(s):// URLs (the public deployments only
// expose the REST API).
func newEmulatorClient(addr string) (emulatorclient.TransportClient, func(), error) {
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return &restEmulator{baseURL: strings.TrimSuffix(addr, "/")}, func() {}, nil
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, err
	}
	// nolint:errcheck
	return emulatorclient.NewGRPCClient(conn), func() { conn.Close() }, nil
}

// restEmulator implements the only TransportClient method CreateOffer needs.
// ponytail: the embedded nil interface panics on any other method — fine, the
// offer flow never calls them.
type restEmulator struct {
	emulatorclient.TransportClient
	baseURL string
}

func (r *restEmulator) GetInfo(ctx context.Context) (*emulatorclient.Info, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.baseURL+"/v1/info", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	// nolint:errcheck
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("emulator info: unexpected status %d", resp.StatusCode)
	}
	var out struct {
		Version                 string   `json:"version"`
		SignerPubkey            string   `json:"signerPubkey"`
		DeprecatedSignerPubkeys []string `json:"deprecatedSignerPubkeys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &emulatorclient.Info{
		Version:                    out.Version,
		SignerPublicKey:            out.SignerPubkey,
		DeprecatedSignerPublicKeys: out.DeprecatedSignerPubkeys,
	}, nil
}

func randomHex(n int) string {
	buf := make([]byte, n)
	// nolint:errcheck
	rand.Read(buf)
	return hex.EncodeToString(buf)
}
