package e2e_test

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	introclient "github.com/ArkLabsHQ/introspector/pkg/client"
	arksdk "github.com/arkade-os/go-sdk"
	sdktypes "github.com/arkade-os/go-sdk/types"
	"github.com/btcsuite/btcd/btcec/v2"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/arkade-os/solver/internal/core/application"
	"github.com/arkade-os/solver/internal/core/ports"
	sqlitedb "github.com/arkade-os/solver/internal/infrastructure/db/sqlite"
	grpcservice "github.com/arkade-os/solver/internal/interface/grpc"
	"github.com/arkade-os/solver/pkg/banco"
	"github.com/arkade-os/solver/pkg/solver"
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

// mockPriceFeed always returns a fixed price of 1.0.
// This makes any offer with roughly 1:1 ratio pass the 1% margin check.
type mockPriceFeed struct{}

func (m *mockPriceFeed) Fetch(_ context.Context, _ string) (float64, error) {
	return 1.0, nil
}

var (
	takerSvc    *application.TakerService
	preimageSvc *application.PreimageService
	pairRepo    ports.PairRepository
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

	// Create taker's ArkClient
	takerDatadir, err := os.MkdirTemp("", "solverd-e2e-taker-*")
	if err != nil {
		log.Errorf("failed to create taker datadir: %s", err)
		return 1
	}
	// nolint:errcheck
	defer os.RemoveAll(takerDatadir)
	takerClient, err = setupTakerClient(ctx, takerDatadir)
	if err != nil {
		log.Errorf("failed to setup taker client: %s", err)
		return 1
	}

	// Fund taker with offchain BTC
	if err := fundTaker(ctx, takerClient); err != nil {
		log.Errorf("failed to fund taker: %s", err)
		return 1
	}

	// SQLite pair repo in temp dir
	tmpDir, err := os.MkdirTemp("", "solverd-e2e-*")
	if err != nil {
		log.Errorf("failed to create temp dir: %s", err)
		return 1
	}
	// nolint:errcheck
	defer os.RemoveAll(tmpDir)

	db, err := sqlitedb.OpenDB(tmpDir)
	if err != nil {
		log.Errorf("failed to open db: %s", err)
		return 1
	}
	// nolint:errcheck
	defer db.Close()

	pairRepo = sqlitedb.NewPairRepository(db)
	tradeRepo := sqlitedb.NewTradeRepository(db)

	// Introspector client
	introConn, err := grpc.NewClient(introspectorAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Errorf("failed to connect to introspector: %s", err)
		return 1
	}
	introClient := introclient.NewGRPCClient(introConn)

	// Build solver
	plugin := banco.NewPlugin(banco.Config{
		SolverClient:    takerClient,
		Introspector:    introClient,
		PairsRepository: pairRepo,
		PriceFeed:       &mockPriceFeed{},
		Log:             log.StandardLogger(),
	})
	s := solver.New(plugin)

	takerSvc = application.NewTakerService(s, pairRepo, tradeRepo, takerClient, takerClient.Indexer(), log.StandardLogger())
	takerSvc.Start()
	defer takerSvc.Stop()

	// Preimage service: stateless — the solver privkey is generated fresh for
	// the test and the preimage plugin recovers credentials from the tx stream
	// (no DB).
	preimagePriv, err := btcec.NewPrivateKey()
	if err != nil {
		log.Errorf("failed to generate preimage privkey: %s", err)
		return 1
	}
	preimageSvc, err = application.NewPreimageService(ctx, application.PreimageServiceConfig{
		ArkClient:     takerClient,
		Introspector:  introClient,
		SolverPrivKey: preimagePriv,
		Log:           log.StandardLogger(),
	})
	if err != nil {
		log.Errorf("failed to create preimage service: %s", err)
		return 1
	}
	if err := preimageSvc.Start(); err != nil {
		log.Errorf("failed to start preimage service: %s", err)
		return 1
	}
	defer preimageSvc.Stop()

	// Start the real gRPC + HTTP gateway server hosting both takerSvc and
	// preimageSvc. e2e tests dial this server as a real client would, rather
	// than calling application services directly.
	srv := grpcservice.NewServer(takerSvc, e2eGRPCPort, e2eHTTPPort, log.StandardLogger()).
		WithPreimageService(preimageSvc)
	if err := srv.Start(); err != nil {
		log.Errorf("failed to start grpc server: %s", err)
		return 1
	}
	defer srv.Stop()
	// Give the listeners a moment to come up before tests dial.
	time.Sleep(500 * time.Millisecond)

	return m.Run()
}

// setupTakerClient builds, inits, and unlocks the bot's wallet. Same flow as
// utils_test.go setupArkClient but adapted for TestMain (no *testing.T) and
// with a caller-managed datadir whose lifetime spans the whole test run.
func setupTakerClient(ctx context.Context, datadir string) (arksdk.Wallet, error) {
	// Auto-settle disabled: the bot spends its VTXOs to fulfill offers,
	// which races the scheduler and floods logs with VTXO_ALREADY_SPENT.
	client, err := arksdk.NewWallet(datadir, arksdk.WithoutAutoSettle())
	if err != nil {
		return nil, err
	}
	if err := client.Init(ctx, arkdURL, "", password); err != nil {
		return nil, err
	}
	if err := client.Unlock(ctx, password); err != nil {
		return nil, err
	}
	synced := <-client.IsSynced(ctx)
	if synced.Err != nil {
		return nil, fmt.Errorf("taker client sync: %w", synced.Err)
	}
	if !synced.Synced {
		return nil, fmt.Errorf("taker client failed to sync")
	}
	return client, nil
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
