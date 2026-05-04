package main

import (
	"context"
	"database/sql"
	"os"
	"os/signal"
	"syscall"

	introclient "github.com/ArkLabsHQ/introspector/pkg/client"
	arksdk "github.com/arkade-os/go-sdk"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/arkade-os/bancod/internal/config"
	"github.com/arkade-os/bancod/internal/core/application"
	sqlitedb "github.com/arkade-os/bancod/internal/infrastructure/db/sqlite"
	"github.com/arkade-os/bancod/internal/infrastructure/pricefeed"
	grpcservice "github.com/arkade-os/bancod/internal/interface/grpc"
	"github.com/arkade-os/bancod/pkg/banco"
	"github.com/arkade-os/bancod/pkg/solver"
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

	arkClient, err := arksdk.NewArkClient(cfg.Datadir)
	if err != nil {
		log.WithError(err).Fatal("failed to create ark client")
	}

	ctx := context.Background()
	if err := arkClient.Init(ctx, cfg.ArkURL, cfg.WalletSeed, cfg.WalletPassword); err != nil {
		log.WithError(err).Fatal("failed to init ark client")
	}
	if err := arkClient.Unlock(ctx, cfg.WalletPassword); err != nil {
		log.WithError(err).Fatal("failed to unlock ark client")
	}
	defer arkClient.Stop()

	var (
		takerSvc  *application.TakerService
		bountySvc *application.BountyService
		srv       *grpcservice.Server
		db        = optionalSqliteDB(cfg, log)
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

		srv = grpcservice.NewServer(takerSvc, cfg.GRPCPort, cfg.HTTPPort, log)
		if err := srv.Start(); err != nil {
			log.WithError(err).Fatal("failed to start server")
		}
	}

	if cfg.BountyEnabled {
		bountySvc, err = application.NewBountyService(ctx, application.BountyServiceConfig{
			ArkClient:    arkClient,
			Introspector: introspector,
			BatchSize:    cfg.BountyBatchSize,
			BatchTimeout: cfg.BountyBatchTimeout,
			Log:          log,
		})
		if err != nil {
			log.WithError(err).Fatal("failed to create bounty service")
		}
		if err := bountySvc.Start(); err != nil {
			log.WithError(err).Fatal("failed to start bounty service")
		}
		log.WithField("batch_size", cfg.BountyBatchSize).
			WithField("batch_timeout", cfg.BountyBatchTimeout).
			Info("bounty plugin started")
	}

	log.WithField("version", Version).
		WithField("banco", cfg.BancoEnabled).
		WithField("bounty", cfg.BountyEnabled).
		Info("bancod started")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Info("shutting down...")
	if bountySvc != nil {
		bountySvc.Stop()
	}
	if takerSvc != nil {
		takerSvc.Stop()
	}
	if srv != nil {
		srv.Stop()
	}
	log.Info("bancod stopped")
}

// optionalSqliteDB opens the sqlite DB iff at least one plugin needs it
// (currently only banco does).
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
