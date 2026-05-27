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

	introclient "github.com/ArkLabsHQ/introspector/pkg/client"
	arkdclient "github.com/arkade-os/arkd/pkg/client-lib"
	singlekey "github.com/arkade-os/arkd/pkg/client-lib/identity/singlekey"
	singlekeyfilestore "github.com/arkade-os/arkd/pkg/client-lib/identity/singlekey/store/file"
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
	"github.com/arkade-os/solver/pkg/solver"
)

// Version is injected at build time via -ldflags "-X main.Version=<tag>".
// Defaults to "dev" for local builds.
var Version = "dev"

func main() {
	cfg, err := config.LoadConfig()
	if err != nil {
		logrus.WithError(err).Fatal("failed to load config")
	}

	log := logrus.New()
	log.SetLevel(logrus.Level(cfg.LogLevel))

	if err := os.MkdirAll(cfg.Datadir, 0750); err != nil {
		log.WithError(err).Fatal("failed to create datadir")
	}

	introConn, err := grpc.NewClient(
		cfg.IntrospectorURL,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.WithError(err).Fatal("failed to connect to introspector")
	}
	// nolint:errcheck
	defer introConn.Close()
	introspector := introclient.NewGRPCClient(introConn)

	ctx := context.Background()
	identityStore, err := singlekeyfilestore.NewStore(cfg.Datadir)
	if err != nil {
		log.WithError(err).Fatal("failed to init identity store")
	}
	singleKeyIdentity, err := singlekey.NewIdentity(identityStore)
	if err != nil {
		log.WithError(err).Fatal("failed to init single-key identity")
	}
	walletOpts := []arksdk.WalletOption{arksdk.WithIdentity(singleKeyIdentity)}
	arkClient, err := arksdk.LoadWallet(cfg.Datadir, walletOpts...)
	if err != nil {
		// Fresh datadir surfaces as either go-sdk's or client-lib's
		// ErrNotInitialized depending on which layer first noticed the
		// missing config — both are valid "no wallet yet" signals.
		if !errors.Is(err, arksdk.ErrNotInitialized) &&
			!errors.Is(err, arkdclient.ErrNotInitialized) {
			log.WithError(err).Fatal("failed to load ark client")
		}
		// Fresh datadir — create and initialize the wallet.
		arkClient, err = arksdk.NewWallet(cfg.Datadir, walletOpts...)
		if err != nil {
			log.WithError(err).Fatal("failed to create ark client")
		}
		if err := arkClient.Init(ctx, cfg.ArkURL, cfg.WalletSeed, cfg.WalletPassword); err != nil {
			log.WithError(err).Fatal("failed to init ark client")
		}
	}
	if err := arkClient.Unlock(ctx, cfg.WalletPassword); err != nil {
		log.WithError(err).Fatal("failed to unlock ark client")
	}
	defer arkClient.Stop()

	var (
		takerSvc    *application.TakerService
		preimageSvc *application.PreimageService
		srv         *grpcservice.Server
		db          = optionalSqliteDB(cfg, log)
	)
	if db != nil {
		// nolint:errcheck
		defer db.Close()
	}

	if cfg.BancoEnabled {
		if db == nil {
			log.Fatal("banco plugin requires sqlite datadir")
		}
		pairRepo := sqlitedb.NewPairRepository(db)
		tradeRepo := sqlitedb.NewTradeRepository(db)
		priceFeed := pricefeed.NewCoinGecko()
		tradeListener := application.NewTradeListener(tradeRepo, log)

		plugin := banco.NewPlugin(banco.Config{
			SolverClient:    arkClient,
			Introspector:    introspector,
			PairsRepository: pairRepo,
			PriceFeed:       priceFeed,
			Listener:        tradeListener,
			Log:             log,
		})
		s := solver.New(plugin).WithLogger(log)

		takerSvc = application.NewTakerService(s, pairRepo, tradeRepo, arkClient, arkClient.Indexer(), log)
		takerSvc.Start()
		log.Info("banco plugin started")
	}

	if cfg.PreimageEnabled {
		solverPriv, err := deriveSolverPrivKey(cfg.WalletSeed)
		if err != nil {
			log.WithError(err).Fatal("failed to derive preimage solver privkey")
		}
		preimageSvc, err = application.NewPreimageService(ctx, application.PreimageServiceConfig{
			ArkClient:     arkClient,
			Introspector:  introspector,
			SolverPrivKey: solverPriv,
			Log:           log,
		})
		if err != nil {
			log.WithError(err).Fatal("failed to create preimage service")
		}
		if err := preimageSvc.Start(); err != nil {
			log.WithError(err).Fatal("failed to start preimage service")
		}
		log.Info("preimage plugin started")
	}

	// One gRPC + HTTP server hosts whichever services are enabled.
	if cfg.BancoEnabled || cfg.PreimageEnabled {
		srv = grpcservice.NewServer(takerSvc, cfg.GRPCPort, cfg.HTTPPort, log).
			WithPreimageService(preimageSvc)
		if err := srv.Start(); err != nil {
			log.WithError(err).Fatal("failed to start server")
		}
	}

	log.WithField("version", Version).
		WithField("banco", cfg.BancoEnabled).
		WithField("preimage", cfg.PreimageEnabled).
		Info("bancod started")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Info("shutting down...")
	if preimageSvc != nil {
		preimageSvc.Stop()
	}
	if takerSvc != nil {
		takerSvc.Stop()
	}
	if srv != nil {
		srv.Stop()
	}
	log.Info("bancod stopped")
}

// optionalSqliteDB opens the sqlite DB iff banco plugin is enabled
func optionalSqliteDB(cfg *config.Config, log logrus.FieldLogger) *sql.DB {
	if !cfg.BancoEnabled {
		return nil
	}
	db, err := sqlitedb.OpenDB(cfg.Datadir)
	if err != nil {
		log.WithError(err).Fatal("failed to open database")
	}
	return db
}

const preimageKeyDomain = "bancod/preimage-plugin/v1"

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
