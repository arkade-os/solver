package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	arkdclient "github.com/arkade-os/arkd/pkg/client-lib"
	singlekey "github.com/arkade-os/arkd/pkg/client-lib/identity/singlekey"
	singlekeyfilestore "github.com/arkade-os/arkd/pkg/client-lib/identity/singlekey/store/file"
	emulatorclient "github.com/arkade-os/emulator/pkg/client"
	arksdk "github.com/arkade-os/go-sdk"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/arkade-os/solver/internal/config"
	"github.com/arkade-os/solver/internal/core/application"
	sqlitedb "github.com/arkade-os/solver/internal/infrastructure/db/sqlite"
	"github.com/arkade-os/solver/internal/infrastructure/pricefeed"
	grpcservice "github.com/arkade-os/solver/internal/interface/grpc"
	"github.com/arkade-os/solver/pkg/banco"
	"github.com/arkade-os/solver/pkg/preimage"
	"github.com/arkade-os/solver/pkg/solver"
	"github.com/arkade-os/solver/pkg/solver/arkdsource"
)

// Version is injected at build time via -ldflags "-X main.Version=<tag>".
// Defaults to "dev" for local builds.
var Version = "dev"

func main() {
	log := logrus.New()

	if err := run(log); err != nil {
		log.Error(err)
		os.Exit(1)
	}
}

func run(log *logrus.Logger) error {
	cfg, err := config.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	log.SetLevel(logrus.Level(cfg.LogLevel))

	if err := os.MkdirAll(cfg.Datadir, 0750); err != nil {
		return fmt.Errorf("failed to create datadir: %w", err)
	}

	emulatorConn, err := grpc.NewClient(
		cfg.EmulatorURL,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return fmt.Errorf("failed to connect to emulator: %w", err)
	}
	// nolint:errcheck
	defer emulatorConn.Close()
	emulator := emulatorclient.NewGRPCClient(emulatorConn)

	ctx := context.Background()
	arkClient, err := setupWallet(ctx, cfg)
	if err != nil {
		return fmt.Errorf("failed to setup wallet: %w", err)
	}
	defer arkClient.Stop()

	plugins := make([]solver.Plugin, 0)
	serverOpts := make([]grpcservice.Option, 0)

	if cfg.BancoEnabled {
		db, err := sqlitedb.OpenDB(cfg.Datadir)
		if err != nil {
			return fmt.Errorf("failed to open database: %w", err)
		}
		// nolint:errcheck
		defer db.Close()

		svc, plugin, err := setupBanco(db, arkClient, emulator, log)
		if err != nil {
			return fmt.Errorf("failed to setup banco: %w", err)
		}
		plugins = append(plugins, plugin)
		serverOpts = append(serverOpts, grpcservice.WithBancoService(svc))

		log.Info("banco plugin enabled")
	}

	if cfg.PreimageEnabled {
		svc, plugin, err := setupPreimage(ctx, cfg, arkClient, emulator, log)
		if err != nil {
			return fmt.Errorf("failed to setup preimage: %w", err)
		}
		plugins = append(plugins, plugin)
		serverOpts = append(serverOpts, grpcservice.WithPreimageService(svc))
		log.Info("preimage plugin enabled")
	}

	// One gRPC + HTTP server hosts whichever services are enabled.
	if len(serverOpts) > 0 {
		srv := grpcservice.NewServer(cfg.GRPCPort, cfg.HTTPPort, log, serverOpts...)
		if err := srv.Start(); err != nil {
			return fmt.Errorf("failed to start server: %w", err)
		}
		defer srv.Stop()
	}

	solverCtx, cancelSolver := context.WithCancel(ctx)
	defer cancelSolver()

	solverDone := make(chan error, 1)
	s := solver.New(plugins...).WithLogger(log)
	src := arkdsource.New(arkClient.Client(), log)
	go func() {
		solverDone <- s.Run(solverCtx, src)
	}()

	log.WithField("version", Version).
		WithField("banco", cfg.BancoEnabled).
		WithField("preimage", cfg.PreimageEnabled).
		Info("solverd started")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-sigCh:
		log.Info("shutting down...")
		cancelSolver()
		signal.Stop(sigCh)
		<-solverDone
	case err := <-solverDone:
		if !errors.Is(err, context.Canceled) {
			return fmt.Errorf("solver runtime exited unexpectedly: %w", err)
		}
	}

	return nil
}

func setupWallet(ctx context.Context, cfg *config.Config) (arksdk.Wallet, error) {
	identityStore, err := singlekeyfilestore.NewStore(cfg.Datadir)
	if err != nil {
		return nil, fmt.Errorf("init identity store: %w", err)
	}
	singleKeyIdentity, err := singlekey.NewIdentity(identityStore)
	if err != nil {
		return nil, fmt.Errorf("init single-key identity: %w", err)
	}

	walletOpts := []arksdk.WalletOption{arksdk.WithIdentity(singleKeyIdentity)}
	arkClient, err := arksdk.LoadWallet(cfg.Datadir, walletOpts...)
	if err != nil {
		// Fresh datadir surfaces as either go-sdk's or client-lib's
		// ErrNotInitialized depending on which layer first noticed the
		// missing config — both are valid "no wallet yet" signals.
		if !errors.Is(err, arksdk.ErrNotInitialized) &&
			!errors.Is(err, arkdclient.ErrNotInitialized) {
			return nil, fmt.Errorf("load ark client: %w", err)
		}

		arkClient, err = arksdk.NewWallet(cfg.Datadir, walletOpts...)
		if err != nil {
			return nil, fmt.Errorf("create ark client: %w", err)
		}
		if err := arkClient.Init(ctx, cfg.ArkURL, cfg.WalletSeed, cfg.WalletPassword); err != nil {
			return nil, fmt.Errorf("init ark client: %w", err)
		}
	}
	if err := arkClient.Unlock(ctx, cfg.WalletPassword); err != nil {
		return nil, fmt.Errorf("unlock ark client: %w", err)
	}

	return arkClient, nil
}

func setupBanco(
	db *sql.DB,
	arkClient arksdk.Wallet,
	emulator emulatorclient.TransportClient,
	log logrus.FieldLogger,
) (*application.TakerService, solver.Plugin, error) {
	pairRepo := sqlitedb.NewPairRepository(db)
	tradeRepo := sqlitedb.NewTradeRepository(db)
	priceFeed := pricefeed.NewCoinGecko()
	tradeListener := application.NewTradeListener(tradeRepo, log)

	plugin := banco.NewPlugin(banco.Config{
		SolverClient:    arkClient,
		Emulator:        emulator,
		PairsRepository: pairRepo,
		PriceFeed:       priceFeed,
		Listener:        tradeListener,
		Log:             log,
	})
	takerSvc := application.NewTakerService(pairRepo, tradeRepo, arkClient, arkClient.Indexer(), log)

	return takerSvc, plugin, nil
}

func setupPreimage(
	ctx context.Context,
	cfg *config.Config,
	arkClient arksdk.Wallet,
	emulator emulatorclient.TransportClient,
	log logrus.FieldLogger,
) (*application.PreimageService, solver.Plugin, error) {
	solverPriv, err := deriveSolverPrivKey(cfg.WalletSeed)
	if err != nil {
		return nil, nil, fmt.Errorf("derive preimage solver privkey: %w", err)
	}
	info, err := emulator.GetInfo(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("get emulator info: %w", err)
	}
	rawEmulatorPub, err := hex.DecodeString(info.SignerPublicKey)
	if err != nil {
		return nil, nil, fmt.Errorf("decode emulator pubkey: %w", err)
	}
	emulatorPub, err := btcec.ParsePubKey(rawEmulatorPub)
	if err != nil {
		return nil, nil, fmt.Errorf("parse emulator pubkey: %w", err)
	}
	configData, err := arkClient.GetConfigData(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("get ark config: %w", err)
	}
	checkpointBytes, err := hex.DecodeString(configData.CheckpointTapscript)
	if err != nil {
		return nil, nil, fmt.Errorf("decode checkpoint tapscript: %w", err)
	}
	preimageSvc, err := application.NewPreimageService(application.PreimageServiceConfig{
		SolverPrivKey:  solverPriv,
		EmulatorPubKey: emulatorPub,
		Log:            log,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create preimage service: %w", err)
	}
	plugin, err := preimage.NewPlugin(ctx, preimage.Config{
		ArkClient:           arkClient,
		Emulator:            emulator,
		SolverPrivKey:       solverPriv,
		EmulatorPubKey:      emulatorPub,
		ServerPubKey:        configData.SignerPubKey,
		CheckpointTapscript: checkpointBytes,
		Network:             configData.Network,
		Log:                 log,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("build preimage plugin: %w", err)
	}

	return preimageSvc, plugin, nil
}

const preimageKeyDomain = "solverd/preimage-plugin/v1"

// deriveSolverPrivKey derives the preimage-plugin's encryption privkey from
// the wallet seed via HMAC-SHA256(seed, domain). Stable across restarts as
// long as the seed is unchanged.
func deriveSolverPrivKey(seedHex string) (*btcec.PrivateKey, error) {
	seed, err := hex.DecodeString(seedHex)
	if err != nil {
		return nil, fmt.Errorf("decode wallet seed: %w", err)
	}
	mac := hmac.New(sha256.New, seed)
	mac.Write([]byte(preimageKeyDomain))
	priv, _ := btcec.PrivKeyFromBytes(mac.Sum(nil))
	return priv, nil
}
