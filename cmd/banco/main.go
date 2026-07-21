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
	"syscall"

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
				Usage: "pair in {base}/{quote} format, each side \"BTC\" or a hex asset ID; base is deposited, quote is received",
			},
			&cli.StringFlag{
				Name: "price-feed", Required: true,
				Usage: "price feed URL (same format as a solverd pair)",
			},
			&cli.StringFlag{
				Name:  "price-path",
				Usage: "JSON pointer to the price in the feed response, e.g. /bitcoin/usd (empty = guess from the feed host)",
			},
			&cli.BoolFlag{
				Name:  "invert-price",
				Usage: "invert the feed price",
			},
			&cli.StringFlag{
				Name: "arkd", Value: "localhost:7070",
				Usage: "arkd server address",
			},
			&cli.StringFlag{
				Name: "emulator", Value: "localhost:7173",
				Usage: "emulator address; host:port dials gRPC, http(s):// uses the REST API",
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

	base, quote, ok := splitPair(c.String("market"))
	if !ok {
		return fmt.Errorf("market must be in format 'base/quote'")
	}
	var wantAsset *asset.AssetId
	if quote != "BTC" {
		var err error
		if wantAsset, err = asset.NewAssetIdFromString(quote); err != nil {
			return fmt.Errorf("invalid quote asset: %w", err)
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
		assetLabel(base), relayAddress)

	// The deposit is whatever the first payment carries; the offer is priced
	// afterwards, at the feed price current when the funds arrive.
	btcReceived, deposit, err := waitForDeposit(ctx, vtxoCh, base)
	if err != nil {
		return err
	}

	price, err := pricefeed.New().Fetch(ctx, c.String("price-feed"), c.String("price-path"))
	if err != nil {
		return fmt.Errorf("failed to fetch price: %w", err)
	}
	if c.Bool("invert-price") {
		price = 1 / price
	}

	baseDec, err := assetDecimals(ctx, wallet.Indexer(), base)
	if err != nil {
		return err
	}
	quoteDec, err := assetDecimals(ctx, wallet.Indexer(), quote)
	if err != nil {
		return err
	}
	want, err := computeWantAmount(deposit, baseDec, quoteDec, price)
	if err != nil {
		return err
	}

	emulator, closeEmulator, err := newEmulatorClient(c.String("emulator"))
	if err != nil {
		return fmt.Errorf("failed to connect to emulator: %w", err)
	}
	defer closeEmulator()

	offer, err := contract.CreateOffer(ctx, contract.CreateOfferParams{
		WantAmount: want,
		WantAsset:  wantAsset,
	}, wallet, emulator)
	if err != nil {
		return fmt.Errorf("failed to create offer: %w", err)
	}

	fmt.Printf("\nOffer: deposit %d %s -> receive %d %s (price %g)\n",
		deposit, assetLabel(base), want, assetLabel(quote), price)

	receiver := clientTypes.Receiver{To: offer.SwapAddress, Amount: deposit}
	if base != "BTC" {
		// Asset deposit: the received BTC is just the carrier; the swap
		// amount is carried by the asset packet.
		receiver.Amount = btcReceived
		receiver.Assets = []clientTypes.Asset{{AssetId: base, Amount: deposit}}
	}
	txid, err := wallet.SendOffChain(
		ctx, []clientTypes.Receiver{receiver}, arksdk.WithExtraPacket(offer.Packet),
	)
	if err != nil {
		return fmt.Errorf("failed to fund swap address: %w", err)
	}

	fmt.Printf("\nSwap funded.\n  swap address: %s\n  funding txid: %s\n\nWaiting for a taker to fulfill...\n",
		offer.SwapAddress, txid)

	fulfillTxid, err := waitForFulfillment(ctx, vtxoCh, quote, want)
	if err != nil {
		return err
	}
	fmt.Printf("Swap fulfilled: received %d %s (txid %s).\n", want, assetLabel(quote), fulfillTxid)
	return nil
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

// waitForFulfillment blocks until the taker's payout lands at the wallet.
// ponytail: matches on asset id / BTC amount only; a change vtxo of exactly
// the want amount could false-positive — fine for a testing CLI.
func waitForFulfillment(
	ctx context.Context, vtxoCh <-chan sdktypes.VtxoEvent, quote string, want uint64,
) (string, error) {
	for {
		vtxos, err := nextVtxosAdded(ctx, vtxoCh)
		if err != nil {
			return "", err
		}
		for _, v := range vtxos {
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
