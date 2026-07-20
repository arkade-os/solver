package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	arkdclient "github.com/arkade-os/arkd/pkg/client-lib"
	singlekey "github.com/arkade-os/arkd/pkg/client-lib/identity/singlekey"
	singlekeyfilestore "github.com/arkade-os/arkd/pkg/client-lib/identity/singlekey/store/file"
	"github.com/arkade-os/arkd/pkg/client-lib/indexer"
	emulatorclient "github.com/arkade-os/emulator/pkg/client"
	arksdk "github.com/arkade-os/go-sdk"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/arkade-os/solver/internal/config"
	"github.com/arkade-os/solver/internal/core/ports"
	sqlitedb "github.com/arkade-os/solver/internal/infrastructure/db/sqlite"
	"github.com/arkade-os/solver/internal/infrastructure/pricefeed"
	"github.com/arkade-os/solver/pkg/executor"
	"github.com/arkade-os/solver/pkg/executor/arkdsource"
	"github.com/arkade-os/solver/pkg/swap"
)

type Service struct {
	marketRepo ports.MarketRepository
	tradeRepo  ports.TradeRepository
	arkClient  arksdk.Wallet
	indexer    indexer.Indexer

	log          *logrus.Logger
	cfg          *config.Config
	plugin       executor.Plugin
	db           *sql.DB
	emulatorConn *grpc.ClientConn
}

// New wires a Service from config and an unlocked wallet: it opens the
// database, builds the swap plugin, and connects to the emulator.
func New(cfg *config.Config, wallet arksdk.Wallet) (*Service, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config must not be nil")
	}
	if wallet == nil {
		return nil, fmt.Errorf("wallet must not be nil")
	}

	log := logrus.StandardLogger()
	log.SetLevel(logrus.Level(cfg.LogLevel))

	if err := os.MkdirAll(cfg.Datadir, 0750); err != nil {
		return nil, fmt.Errorf("failed to create datadir: %w", err)
	}

	emulatorAddr, emulatorCreds := dialTarget(cfg.EmulatorURL)
	emulatorConn, err := grpc.NewClient(
		emulatorAddr,
		grpc.WithTransportCredentials(emulatorCreds),
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

	marketRepo := sqlitedb.NewMarketRepository(db)
	tradeRepo := sqlitedb.NewTradeRepository(db)

	feed := pricefeed.New()

	plugin := swap.NewPlugin(swap.Config{
		SolverClient:      wallet,
		Emulator:          emulator,
		MarketsRepository: marketRepo,
		PriceFeed:         feed,
		Listener:          &tradeListener{tradeRepo},
		Log:               log,
	})

	svc := &Service{
		marketRepo:   marketRepo,
		tradeRepo:    tradeRepo,
		arkClient:    wallet,
		indexer:      wallet.Indexer(),
		log:          log,
		cfg:          cfg,
		plugin:       plugin,
		db:           db,
		emulatorConn: emulatorConn,
	}

	log.Info("swap plugin enabled")
	return svc, nil
}

// OperatorConfig is the config shown in the dashboard, without wallet secrets.
type OperatorConfig struct {
	ArkURL      string `json:"ark_url"`
	EmulatorURL string `json:"emulator_url"`
	ExplorerURL string `json:"explorer_url"`
	Datadir     string `json:"datadir"`
	GRPCPort    int    `json:"grpc_port"`
	HTTPPort    int    `json:"http_port"`
	LogLevel    int    `json:"log_level"`
}

func (s *Service) GetConfig() OperatorConfig {
	return OperatorConfig{
		ArkURL:      s.cfg.ArkURL,
		EmulatorURL: s.cfg.EmulatorURL,
		ExplorerURL: s.cfg.ExplorerURL,
		Datadir:     s.cfg.Datadir,
		GRPCPort:    s.cfg.GRPCPort,
		HTTPPort:    s.cfg.HTTPPort,
		LogLevel:    s.cfg.LogLevel,
	}
}

// Close releases the resources opened by New (database + emulator connection).
func (s *Service) Close() {
	if s.db != nil {
		// nolint:errcheck
		s.db.Close()
	}
	if s.emulatorConn != nil {
		// nolint:errcheck
		s.emulatorConn.Close()
	}
}

// Run drives the solver against the arkd tx stream until ctx is canceled
func (s *Service) Run(ctx context.Context) error {
	runtimeCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make(chan error, 1)
	engine := executor.New(s.plugin).WithLogger(s.log)
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

// SetupWallet loads or initializes the solverd wallet.
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
		if !errors.Is(err, arksdk.ErrNotInitialized) &&
			!errors.Is(err, arkdclient.ErrNotInitialized) {
			return nil, fmt.Errorf("load ark client: %w", err)
		}

		arkClient, err = arksdk.NewWallet(cfg.Datadir, walletOpts...)
		if err != nil {
			return nil, fmt.Errorf("create ark client: %w", err)
		}
		var initOpts []arksdk.InitOption
		if cfg.ExplorerURL != "" {
			initOpts = append(initOpts, arksdk.WithExplorerURL(cfg.ExplorerURL))
		}
		if err := arkClient.Init(ctx, cfg.ArkURL, cfg.WalletSeed, cfg.WalletPassword, initOpts...); err != nil {
			return nil, fmt.Errorf("init ark client: %w", err)
		}
	}
	if err := arkClient.Unlock(ctx, cfg.WalletPassword); err != nil {
		return nil, fmt.Errorf("unlock ark client: %w", err)
	}

	// Unlock restores funds (contract scan) in the background; block until it
	// finishes so we don't serve a half-restored wallet or hide a scan failure.
	if synced := <-arkClient.IsSynced(ctx); synced.Err != nil {
		return nil, fmt.Errorf("restore/sync wallet: %w", synced.Err)
	}

	return arkClient, nil
}

func dialTarget(serverURL string) (string, credentials.TransportCredentials) {
	creds := credentials.TransportCredentials(insecure.NewCredentials())
	port := 80

	serverURL = strings.TrimPrefix(serverURL, "http://")
	if rest, ok := strings.CutPrefix(serverURL, "https://"); ok {
		serverURL = rest
		creds = credentials.NewTLS(nil)
		port = 443
	}
	if !strings.Contains(serverURL, ":") {
		serverURL = fmt.Sprintf("%s:%d", serverURL, port)
	}

	return serverURL, creds
}

type tradeListener struct {
	repo ports.TradeRepository
}

func (l *tradeListener) OnAttempt(_ context.Context, evt swap.FulfillmentAttempt) {
	trade := ports.Trade{
		Market:        evt.Market,
		DepositAsset:  evt.DepositAsset,
		DepositAmount: evt.DepositAmount,
		WantAsset:     evt.WantAsset,
		WantAmount:    evt.WantAmount,
		OfferTxid:     evt.OfferTxid,
		FulfillTxid:   evt.FulfillTxid,
		Error:         evt.Error,
		CreatedAt:     evt.Timestamp,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := l.repo.Add(ctx, trade); err != nil {
		logrus.WithError(err).WithField("offerTxid", evt.OfferTxid).
			Error("failed to persist trade")
	}
}
