// Command init bootstraps a locally-running solverd for development.
//
// It mirrors the funding logic in test/e2e and performs three steps so that
// `make run` produces a solver that can immediately fulfill swaps:
//
//  1. Fund the solver's offchain BTC balance from a throwaway funder wallet.
//  2. Mint a regtest asset and send a slice of it to the solver, so the bot
//     can also pay out assets.
//  3. Read the price from the solver-pricefeed mock and register both
//     BTC/<asset> and <asset>/BTC trading pairs pointing at that feed.
//
// Endpoints default to the topology used by `make run` (arkd@7070, arkd
// admin HTTP@7071, emulator@7173, solverd gRPC@7170) and the pricefeed port
// exposed by test/docker-compose.yml. Every endpoint is overridable via env.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/arkade-os/arkd/pkg/ark-lib/asset"
	clientTypes "github.com/arkade-os/arkd/pkg/client-lib/types"
	arksdk "github.com/arkade-os/go-sdk"
	sdktypes "github.com/arkade-os/go-sdk/types"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	swapv1 "github.com/arkade-os/solver/api-spec/protobuf/gen/go/solverd/v1"
)

// log is a dedicated logger instance: go-sdk's wallet.Unlock forces the global
// logrus logger down to ErrorLevel, which would otherwise swallow our progress
// output. Using our own logger keeps init logs visible.
var log = logrus.New()

// config is resolved entirely from the environment with defaults matching the
// `make run` local stack.
type config struct {
	arkURL       string // arkd gRPC endpoint the funder wallet connects to
	arkHTTPURL   string // arkd REST endpoint serving the admin-note faucet
	solverGRPC   string // solverd gRPC endpoint (Swap + Wallet services)
	pricefeedURL string // base URL of the solver-pricefeed mock

	funderPassword string

	btcFunding   uint64 // sats sent to the solver for BTC payouts
	assetSupply  uint64 // total asset units minted on the funder
	assetFunding uint64 // asset units sent to the solver for asset payouts
}

func loadConfig() config {
	return config{
		arkURL:         env("ARK_URL", "localhost:7070"),
		arkHTTPURL:     env("ARK_HTTP_URL", "http://localhost:7071"),
		solverGRPC:     env("SOLVER_GRPC", "localhost:7170"),
		pricefeedURL:   env("PRICEFEED_URL", "http://localhost:8088"),
		funderPassword: env("FUNDER_PASSWORD", "password"),
		btcFunding:     1_000_000,
		assetSupply:    100_000,
		assetFunding:   50_000,
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	log.SetLevel(logrus.InfoLevel)
	if err := run(); err != nil {
		log.Fatalf("init-solverd failed: %s", err)
	}
}

func run() error {
	ctx := context.Background()
	cfg := loadConfig()

	conn, err := grpc.NewClient(cfg.solverGRPC, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial solverd: %w", err)
	}
	// nolint:errcheck
	defer conn.Close()
	swap := swapv1.NewSwapServiceClient(conn)
	wallet := swapv1.NewWalletServiceClient(conn)

	log.Info("waiting for solverd to become ready...")
	if err := waitSolverReady(ctx, swap, 90*time.Second); err != nil {
		return err
	}

	solverAddr, err := solverOffchainAddress(ctx, wallet)
	if err != nil {
		return fmt.Errorf("get solver address: %w", err)
	}
	log.Infof("solver offchain address: %s", solverAddr)

	// 1+2. Spin up a throwaway funder wallet, top it up via the admin-note
	// faucet, then use it to fund the solver with both BTC and a fresh asset.
	log.Info("creating funder wallet...")
	funder, cleanup, err := newFunderWallet(ctx, cfg)
	if err != nil {
		return fmt.Errorf("create funder wallet: %w", err)
	}
	defer cleanup()

	// Fund the funder generously: it has to cover the BTC sent to the solver,
	// the asset issuance backing, and the asset carrier output + fees.
	if err := faucetOffchain(ctx, funder, cfg.arkHTTPURL, cfg.btcFunding+cfg.assetSupply+1_000_000); err != nil {
		return fmt.Errorf("fund funder wallet: %w", err)
	}

	log.Infof("funding solver with %d sats BTC...", cfg.btcFunding)
	if _, err := funder.SendOffChain(ctx, []clientTypes.Receiver{{
		To:     solverAddr,
		Amount: cfg.btcFunding,
	}}); err != nil {
		return fmt.Errorf("send BTC to solver: %w", err)
	}
	if err := pollSolverBTC(ctx, wallet, cfg.btcFunding*9/10, 60*time.Second); err != nil {
		return err
	}

	log.Infof("minting asset (supply %d)...", cfg.assetSupply)
	assetID, err := issueAsset(ctx, funder, cfg.assetSupply)
	if err != nil {
		return fmt.Errorf("issue asset: %w", err)
	}
	log.Infof("minted asset: %s", assetID)

	log.Infof("funding solver with %d units of %s...", cfg.assetFunding, assetID)
	if _, err := funder.SendOffChain(ctx, []clientTypes.Receiver{{
		To:     solverAddr,
		Amount: cfg.assetFunding, // BTC carrier for the asset VTXO
		Assets: []clientTypes.Asset{{AssetId: assetID, Amount: cfg.assetFunding}},
	}}); err != nil {
		return fmt.Errorf("send asset to solver: %w", err)
	}
	if err := pollSolverAsset(ctx, wallet, assetID, cfg.assetFunding, 60*time.Second); err != nil {
		return err
	}

	// 3. Read the price from the solver-pricefeed mock and register the pairs.
	btcAssetFeed := cfg.pricefeedURL + "/btc-asset"
	assetBtcFeed := cfg.pricefeedURL + "/asset-btc"

	price, err := fetchPrice(ctx, btcAssetFeed)
	if err != nil {
		return fmt.Errorf("read pricefeed %s: %w", btcAssetFeed, err)
	}
	log.Infof("pricefeed %s => BTC/%s price %v", btcAssetFeed, assetID, price)

	pairs := []*swapv1.PairInfo{
		{
			Pair:      "BTC/" + assetID,
			MinAmount: 1,
			MaxAmount: 100_000_000,
			PriceFeed: btcAssetFeed,
		},
		{
			Pair:      assetID + "/BTC",
			MinAmount: 1,
			MaxAmount: 100_000_000,
			PriceFeed: assetBtcFeed,
		},
	}
	for _, p := range pairs {
		if _, err := swap.AddPair(ctx, &swapv1.AddPairRequest{Pair: p}); err != nil {
			return fmt.Errorf("add pair %s: %w", p.Pair, err)
		}
		log.Infof("added pair %s (feed %s)", p.Pair, p.PriceFeed)
	}

	log.Info("init-solverd complete: solver funded, asset minted, pairs registered")
	return nil
}

// waitSolverReady polls SwapService.GetStatus until the bot reports running.
func waitSolverReady(ctx context.Context, swap swapv1.SwapServiceClient, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		callCtx, callCancel := context.WithTimeout(ctx, time.Second)
		resp, err := swap.GetStatus(callCtx, &swapv1.GetStatusRequest{})
		callCancel()
		if err == nil && resp.GetRunning() {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for solverd: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func solverOffchainAddress(ctx context.Context, wallet swapv1.WalletServiceClient) (string, error) {
	resp, err := wallet.GetAddress(ctx, &swapv1.GetAddressRequest{})
	if err != nil {
		return "", err
	}
	if resp.GetOffchainAddress() == "" {
		return "", fmt.Errorf("empty offchain address")
	}
	return resp.GetOffchainAddress(), nil
}

// pollSolverBTC waits until the solver's offchain settled balance reaches target.
func pollSolverBTC(ctx context.Context, wallet swapv1.WalletServiceClient, target uint64, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		resp, err := wallet.GetBalance(ctx, &swapv1.GetBalanceRequest{})
		if err == nil && resp.GetOffchainSettled() >= target {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for solver BTC balance >= %d", target)
		}
		time.Sleep(time.Second)
	}
}

// pollSolverAsset waits until the solver reports at least amount of assetID.
func pollSolverAsset(ctx context.Context, wallet swapv1.WalletServiceClient, assetID string, amount uint64, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		resp, err := wallet.GetBalance(ctx, &swapv1.GetBalanceRequest{})
		if err == nil && resp.GetAssetBalances()[assetID] >= amount {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for solver asset %s balance >= %d", assetID, amount)
		}
		time.Sleep(time.Second)
	}
}

// newFunderWallet builds, inits, and unlocks a throwaway go-sdk wallet on a
// temp datadir. The returned cleanup stops the wallet and removes the datadir.
func newFunderWallet(ctx context.Context, cfg config) (arksdk.Wallet, func(), error) {
	dir, err := os.MkdirTemp("", "solverd-init-funder-*")
	if err != nil {
		return nil, nil, err
	}
	fail := func(err error) (arksdk.Wallet, func(), error) {
		// nolint:errcheck
		os.RemoveAll(dir)
		return nil, nil, err
	}

	w, err := arksdk.NewWallet(dir, arksdk.WithoutAutoSettle())
	if err != nil {
		return fail(err)
	}
	if err := w.Init(ctx, cfg.arkURL, "", cfg.funderPassword); err != nil {
		return fail(fmt.Errorf("init: %w", err))
	}
	if err := w.Unlock(ctx, cfg.funderPassword); err != nil {
		return fail(fmt.Errorf("unlock: %w", err))
	}
	synced := <-w.IsSynced(ctx)
	if synced.Err != nil {
		return fail(fmt.Errorf("sync: %w", synced.Err))
	}
	cleanup := func() {
		w.Stop()
		// nolint:errcheck
		os.RemoveAll(dir)
	}
	return w, cleanup, nil
}

// faucetOffchain redeems a fresh admin note into the wallet and waits for the
// resulting VTXO to land.
func faucetOffchain(ctx context.Context, w arksdk.Wallet, arkHTTPURL string, amount uint64) error {
	note, err := generateNote(ctx, arkHTTPURL, amount)
	if err != nil {
		return err
	}

	vtxoCh := w.GetVtxoEventChannel(ctx)
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-waitCtx.Done():
				return
			case ev, ok := <-vtxoCh:
				if !ok {
					return
				}
				if ev.Type == sdktypes.VtxosAdded && len(ev.Vtxos) > 0 {
					return
				}
			}
		}
	}()

	if _, err := w.RedeemNotes(ctx, []string{note}); err != nil {
		return err
	}
	select {
	case <-done:
		return nil
	case <-waitCtx.Done():
		return fmt.Errorf("timed out waiting for VtxosAdded after redeeming note")
	}
}

// generateNote requests a fresh admin note from arkd for `amount` sats.
func generateNote(ctx context.Context, arkHTTPURL string, amount uint64) (string, error) {
	httpClient := &http.Client{Timeout: 15 * time.Second}
	body := bytes.NewReader([]byte(fmt.Sprintf(`{"amount": "%d"}`, amount)))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, arkHTTPURL+"/v1/admin/note", body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Basic YWRtaW46YWRtaW4=") // admin:admin
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	// nolint:errcheck
	defer resp.Body.Close()
	var noteResp struct {
		Notes []string `json:"notes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&noteResp); err != nil {
		return "", err
	}
	if len(noteResp.Notes) == 0 {
		return "", fmt.Errorf("no notes returned from admin API")
	}
	return noteResp.Notes[0], nil
}

// issueAsset mints `supply` units of a fresh zero-decimal asset. solverd's pair
// validation looks up "decimals" metadata via the indexer, so it must be set.
func issueAsset(ctx context.Context, w arksdk.Wallet, supply uint64) (string, error) {
	decimalsMd, err := asset.NewMetadata("decimals", "0")
	if err != nil {
		return "", err
	}
	_, assetIds, err := w.IssueAsset(ctx, supply, nil, []asset.Metadata{*decimalsMd})
	if err != nil {
		return "", err
	}
	if len(assetIds) == 0 {
		return "", fmt.Errorf("no asset id returned")
	}
	return assetIds[0].String(), nil
}

// fetchPrice reads the CoinGecko-style mock feed ({"a":{"b": price}}) and
// returns the first nested value, matching the solver's own price parsing.
func fetchPrice(ctx context.Context, feedURL string) (float64, error) {
	httpClient := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return 0, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	// nolint:errcheck
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	var parsed map[string]map[string]float64
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return 0, fmt.Errorf("parse feed: %w", err)
	}
	for _, nested := range parsed {
		for _, price := range nested {
			return price, nil
		}
	}
	return 0, fmt.Errorf("no price found in feed response")
}
