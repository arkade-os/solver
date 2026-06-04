package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"

	arkdclient "github.com/arkade-os/arkd/pkg/client-lib"
	singlekey "github.com/arkade-os/arkd/pkg/client-lib/identity/singlekey"
	singlekeyfilestore "github.com/arkade-os/arkd/pkg/client-lib/identity/singlekey/store/file"
	"github.com/arkade-os/arkd/pkg/client-lib/indexer"
	emulatorclient "github.com/arkade-os/emulator/pkg/client"
	arksdk "github.com/arkade-os/go-sdk"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/arkade-os/solver/internal/config"
	"github.com/arkade-os/solver/internal/core/ports"
	sqlitedb "github.com/arkade-os/solver/internal/infrastructure/db/sqlite"
	"github.com/arkade-os/solver/internal/infrastructure/pricefeed"
	"github.com/arkade-os/solver/pkg/banco"
	"github.com/arkade-os/solver/pkg/solver"
	"github.com/arkade-os/solver/pkg/solver/arkdsource"
)

type Server interface {
	Start() error
	Stop()
}

// Service is the single solverd unit: it both exposes the taker operations
// (pair CRUD, balances, addresses, trades — see swap_service.go) and owns the
// daemon runtime that drives the solver against the arkd tx stream (Run).
type Service struct {
	// service-layer dependencies (set by NewService)
	pairRepo  ports.PairRepository
	tradeRepo ports.TradeRepository
	arkClient arksdk.Wallet
	indexer   indexer.Indexer
	log       logrus.FieldLogger

	// runtime dependencies (set by New, used by Run)
	cfg          *config.Config
	plugin       solver.Plugin
	db           *sql.DB
	emulatorConn *grpc.ClientConn
}

type options struct {
	bancoPriceFeed banco.PriceFeed
}

// Option customizes the solverd runtime wiring.
type Option func(*options)

// WithBancoPriceFeed overrides banco's default production price feed.
func WithBancoPriceFeed(feed banco.PriceFeed) Option {
	return func(o *options) {
		o.bancoPriceFeed = feed
	}
}

// New wires a Service from config and an unlocked wallet: it opens the
// database, builds the banco plugin, and connects to the emulator. The
// returned Service is ready to serve taker operations and to Run. Run takes
// ownership of the resources opened here (database + emulator connection) and
// closes them on exit.
func New(cfg *config.Config, log *logrus.Logger, wallet arksdk.Wallet, opts ...Option) (*Service, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config must not be nil")
	}
	if wallet == nil {
		return nil, fmt.Errorf("wallet must not be nil")
	}

	if log == nil {
		log = logrus.StandardLogger()
	}
	log.SetLevel(logrus.Level(cfg.LogLevel))

	var runtimeOpts options
	for _, opt := range opts {
		opt(&runtimeOpts)
	}

	if err := os.MkdirAll(cfg.Datadir, 0750); err != nil {
		return nil, fmt.Errorf("failed to create datadir: %w", err)
	}

	emulatorConn, err := grpc.NewClient(
		cfg.EmulatorURL,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to emulator: %w", err)
	}
	emulator := emulatorclient.NewGRPCClient(emulatorConn)

	db, err := sqlitedb.OpenDB(cfg.Datadir)
	if err != nil {
		// nolint:errcheck
		emulatorConn.Close()
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	pairRepo := sqlitedb.NewPairRepository(db)
	tradeRepo := sqlitedb.NewTradeRepository(db)

	feed := runtimeOpts.bancoPriceFeed
	if feed == nil {
		feed = pricefeed.NewCoinGecko()
	}

	plugin := banco.NewPlugin(banco.Config{
		SolverClient:    wallet,
		Emulator:        emulator,
		PairsRepository: pairRepo,
		PriceFeed:       feed,
		Listener:        NewTradeListener(tradeRepo, log),
		Log:             log,
	})

	svc := NewService(pairRepo, tradeRepo, wallet, wallet.Indexer(), log)
	svc.cfg = cfg
	svc.plugin = plugin
	svc.db = db
	svc.emulatorConn = emulatorConn

	log.Info("banco plugin enabled")
	return svc, nil
}

// Run starts the optional API server and drives the solver against the arkd
// tx stream until ctx is canceled or the solver exits unexpectedly. It closes
// the resources opened by New before returning.
func (s *Service) Run(ctx context.Context, server Server) error {
	if s.db != nil {
		// nolint:errcheck
		defer s.db.Close()
	}
	if s.emulatorConn != nil {
		// nolint:errcheck
		defer s.emulatorConn.Close()
	}

	if s.plugin == nil {
		return fmt.Errorf("no plugin configured")
	}

	if server != nil {
		if err := server.Start(); err != nil {
			return fmt.Errorf("failed to start server: %w", err)
		}
		defer server.Stop()
	}

	runtimeCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make(chan error, 1)
	engine := solver.New(s.plugin).WithLogger(s.log)
	src := arkdsource.New(s.arkClient.Client(), s.log)
	go func() {
		done <- engine.Run(runtimeCtx, src)
	}()

	select {
	case <-ctx.Done():
		cancel()
		<-done
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("solver runtime exited unexpectedly: %w", err)
		}
	}

	return nil
}

// SetupWallet loads or initializes and unlocks the solverd wallet.
func SetupWallet(ctx context.Context, cfg *config.Config, extraOpts ...arksdk.WalletOption) (arksdk.Wallet, error) {
	identityStore, err := singlekeyfilestore.NewStore(cfg.Datadir)
	if err != nil {
		return nil, fmt.Errorf("init identity store: %w", err)
	}
	singleKeyIdentity, err := singlekey.NewIdentity(identityStore)
	if err != nil {
		return nil, fmt.Errorf("init single-key identity: %w", err)
	}

	walletOpts := append([]arksdk.WalletOption{arksdk.WithIdentity(singleKeyIdentity)}, extraOpts...)
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
