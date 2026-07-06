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
	"github.com/arkade-os/solver/pkg/banco"
	"github.com/arkade-os/solver/pkg/executor"
	"github.com/arkade-os/solver/pkg/executor/arkdsource"
)

type Service struct {
	pairRepo  ports.PairRepository
	tradeRepo ports.TradeRepository
	arkClient arksdk.Wallet
	indexer   indexer.Indexer

	log          *logrus.Logger
	cfg          *config.Config
	plugin       executor.Plugin
	db           *sql.DB
	emulatorConn *grpc.ClientConn
}

// New wires a Service from config and an unlocked wallet: it opens the
// database, builds the banco plugin, and connects to the emulator.
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

	pairRepo := sqlitedb.NewPairRepository(db)
	tradeRepo := sqlitedb.NewTradeRepository(db)

	feed := pricefeed.NewCoinGecko()

	plugin := banco.NewPlugin(banco.Config{
		SolverClient:    wallet,
		Emulator:        emulator,
		PairsRepository: pairRepo,
		PriceFeed:       feed,
		Listener:        &tradeListener{tradeRepo},
		Log:             log,
	})

	svc := &Service{
		pairRepo:     pairRepo,
		tradeRepo:    tradeRepo,
		arkClient:    wallet,
		indexer:      wallet.Indexer(),
		log:          log,
		cfg:          cfg,
		plugin:       plugin,
		db:           db,
		emulatorConn: emulatorConn,
	}

	log.Info("banco plugin enabled")
	return svc, nil
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

func (l *tradeListener) OnFulfill(_ context.Context, evt banco.FulfillmentEvent) {
	trade := ports.Trade{
		Pair:          evt.Pair,
		DepositAsset:  evt.DepositAsset,
		DepositAmount: evt.DepositAmount,
		WantAsset:     evt.WantAsset,
		WantAmount:    evt.WantAmount,
		OfferTxid:     evt.OfferTxid,
		FulfillTxid:   evt.FulfillTxid,
		CreatedAt:     evt.Timestamp,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := l.repo.Add(ctx, trade); err != nil {
		logrus.WithError(err).WithField("fulfillTxid", evt.FulfillTxid).
			Error("failed to persist trade")
	}
}
