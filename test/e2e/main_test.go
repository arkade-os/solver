package e2e_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	arksdk "github.com/arkade-os/go-sdk"
	sdktypes "github.com/arkade-os/go-sdk/types"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	bancov1 "github.com/arkade-os/solver/api-spec/protobuf/gen/go/solverd/v1"
	"github.com/arkade-os/solver/internal/config"
	"github.com/arkade-os/solver/internal/solverd"
)

const (
	// e2eGRPCPort + e2eHTTPPort are the ports the e2e gRPC server binds to.
	// They're separate from cmd/solverd's defaults (7070/7071) so a developer
	// can run both in parallel.
	e2eGRPCPort = 17070
	e2eHTTPPort = 17071
)

// e2eGRPCAddr is the address tests dial to act as a real client of the bot's
// gRPC API.
var e2eGRPCAddr = fmt.Sprintf("localhost:%d", e2eGRPCPort)

// mockPriceFeed returns deterministic prices keyed by the pair's feed URL.
type mockPriceFeed map[string]float64

func (m mockPriceFeed) Fetch(_ context.Context, feedURL string) (float64, error) {
	price, ok := m[feedURL]
	if !ok {
		return 0, fmt.Errorf("mock price feed missing price for %s", feedURL)
	}
	return price, nil
}

var (
	takerClient arksdk.Wallet
)

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

// runTests wraps the test setup so deferred cleanups (temp dirs, DB handles,
// service stops) actually run before the process exits — os.Exit in TestMain
// would skip them otherwise.
func runTests(m *testing.M) int {
	log.SetLevel(log.DebugLevel)
	ctx := context.Background()

	if err := refillArkd(ctx); err != nil {
		log.Errorf("failed to refill arkd: %s", err)
		return 1
	}

	takerDatadir, err := os.MkdirTemp("", "solverd-e2e-taker-*")
	if err != nil {
		log.Errorf("failed to create taker datadir: %s", err)
		return 1
	}
	// nolint:errcheck
	defer os.RemoveAll(takerDatadir)
	walletSeed, err := randomSeedHex()
	if err != nil {
		log.Errorf("failed to generate wallet seed: %s", err)
		return 1
	}
	cfg := &config.Config{
		Datadir:         takerDatadir,
		ArkURL:          arkdURL,
		WalletSeed:      walletSeed,
		WalletPassword:  password,
		EmulatorURL:     emulatorAddr,
		GRPCPort:        e2eGRPCPort,
		HTTPPort:        e2eHTTPPort,
		LogLevel:        int(log.DebugLevel),
		BancoEnabled:    true,
		PreimageEnabled: true,
	}
	takerClient, err = solverd.SetupWallet(ctx, cfg, arksdk.WithoutAutoSettle())
	if err != nil {
		log.Errorf("failed to setup taker client: %s", err)
		return 1
	}
	defer takerClient.Stop()

	runCtx, cancelSolver := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- solverd.Run(runCtx, cfg, log.StandardLogger(), takerClient,
			solverd.WithBancoPriceFeed(mockPriceFeed{
				mockAssetBTCPriceFeed:   100_000_000,
				mockBTCAssetPriceFeed:   0.00000001,
				mockAssetAssetPriceFeed: 1,
			}),
		)
	}()
	defer func() {
		cancelSolver()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			log.Errorf("solverd exited during shutdown: %s", err)
		}
	}()

	if err := waitBancoReady(ctx); err != nil {
		log.Errorf("failed waiting for solverd readiness: %s", err)
		return 1
	}

	if err := waitWalletSynced(ctx, takerClient); err != nil {
		log.Errorf("failed to sync taker client: %s", err)
		return 1
	}

	// Fund taker with offchain BTC.
	if err := fundTaker(ctx, takerClient); err != nil {
		log.Errorf("failed to fund taker: %s", err)
		return 1
	}

	return m.Run()
}

func randomSeedHex() (string, error) {
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return "", err
	}
	return hex.EncodeToString(seed), nil
}

func waitBancoReady(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	conn, err := grpc.NewClient(e2eGRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	// nolint:errcheck
	defer conn.Close()
	client := bancov1.NewBancoServiceClient(conn)

	for {
		callCtx, callCancel := context.WithTimeout(ctx, time.Second)
		resp, err := client.GetStatus(callCtx, &bancov1.GetStatusRequest{})
		callCancel()
		if err == nil && resp.GetRunning() {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func waitWalletSynced(ctx context.Context, client arksdk.Wallet) error {
	synced := <-client.IsSynced(ctx)
	if synced.Err != nil {
		return fmt.Errorf("taker client sync: %w", synced.Err)
	}
	if !synced.Synced {
		return fmt.Errorf("taker client failed to sync")
	}
	return nil
}

// fundTaker tops up the bot's offchain balance via an admin-issued note, using
// the wallet's vtxo event channel as the wait barrier — same pattern as
// go-sdk's test/e2e faucetOffchain.
func fundTaker(ctx context.Context, client arksdk.Wallet) error {
	bal, err := client.Balance(ctx)
	if err != nil {
		return err
	}
	if bal.OffchainBalance.Total >= 100000 {
		return nil // already funded
	}

	note, err := generateNoteCtx(ctx, 200000) // 200k sats
	if err != nil {
		return fmt.Errorf("failed to generate note: %w", err)
	}

	vtxoCh := client.GetVtxoEventChannel(ctx)

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

	if _, err := client.RedeemNotes(ctx, []string{note}); err != nil {
		return fmt.Errorf("failed to redeem note: %w", err)
	}

	select {
	case <-done:
		return nil
	case <-waitCtx.Done():
		return fmt.Errorf("fundTaker: timed out waiting for VtxosAdded event")
	}
}

func refillArkd(ctx context.Context) error {
	arkdExec := "docker exec solverd-arkd arkd"
	command := fmt.Sprintf("%s wallet balance", arkdExec)
	out, err := runCommand(ctx, command)
	if err != nil {
		return err
	}
	re := regexp.MustCompile(`available:\s*([0-9]+\.[0-9]+)`)
	matches := re.FindStringSubmatch(out)
	if len(matches) < 2 {
		return fmt.Errorf("could not parse arkd balance from: %s", out)
	}
	balance, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return err
	}
	if delta := 5.0 - balance; delta >= 1 {
		addrCmd := fmt.Sprintf("%s wallet address", arkdExec)
		address, err := runCommand(ctx, addrCmd)
		if err != nil {
			return err
		}
		for range int(delta) {
			if err := faucet(ctx, strings.TrimSpace(address), 1); err != nil {
				return err
			}
		}
	}
	time.Sleep(5 * time.Second)
	return nil
}
