package application

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/arkade-os/arkd/pkg/client-lib/indexer"
	arksdk "github.com/arkade-os/go-sdk"
	"github.com/sirupsen/logrus"

	"github.com/arkade-os/bancod/internal/core/ports"
	"github.com/arkade-os/bancod/pkg/banco"
	"github.com/arkade-os/bancod/pkg/solver"
	"github.com/arkade-os/bancod/pkg/solver/arkdsource"
)

const btcDecimals = 8

// Status reports whether the solver run goroutine is currently active.
type Status struct {
	Running bool
}

// TakerService is the application-level service that owns the solver run
// loop and provides CRUD for trading pairs plus wallet operations.
type TakerService struct {
	solver    *solver.Solver
	pairRepo  ports.PairRepository
	tradeRepo ports.TradeRepository
	arkClient arksdk.ArkClient
	indexer   indexer.Indexer
	log       logrus.FieldLogger

	cancel  context.CancelFunc
	done    chan struct{}
	mu      sync.RWMutex
	running bool
}

// NewTakerService creates a new TakerService. The caller is responsible
// for constructing the solver from a banco.Plugin.
func NewTakerService(
	s *solver.Solver,
	pairRepo ports.PairRepository,
	tradeRepo ports.TradeRepository,
	arkClient arksdk.ArkClient,
	idx indexer.Indexer,
	log logrus.FieldLogger,
) *TakerService {
	if log == nil {
		log = logrus.StandardLogger()
	}
	return &TakerService{
		solver:    s,
		pairRepo:  pairRepo,
		tradeRepo: tradeRepo,
		arkClient: arkClient,
		indexer:   idx,
		log:       log,
	}
}

// ListTrades returns up to `limit` most recent fulfilled trades, newest first.
// Passing limit <= 0 uses the repository default.
func (svc *TakerService) ListTrades(ctx context.Context, limit int) ([]ports.Trade, error) {
	if svc.tradeRepo == nil {
		return nil, nil
	}
	return svc.tradeRepo.List(ctx, limit)
}

// Start spawns the solver run goroutine, subscribed to the arkd tx stream.
func (svc *TakerService) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	svc.cancel = cancel
	svc.done = make(chan struct{})

	txs := arkdsource.Subscribe(ctx, svc.arkClient, svc.log)
	svc.setRunning(true)
	go func() {
		defer close(svc.done)
		defer svc.setRunning(false)
		if err := svc.solver.Run(ctx, txs); err != nil && !errors.Is(err, context.Canceled) {
			svc.log.WithError(err).Error("solver run exited")
		}
	}()
}

// Stop signals the run goroutine to exit and waits for it.
func (svc *TakerService) Stop() {
	if svc.cancel != nil {
		svc.cancel()
		<-svc.done
	}
}

// Status reports whether Run is active.
func (svc *TakerService) Status() Status {
	svc.mu.RLock()
	defer svc.mu.RUnlock()
	return Status{Running: svc.running}
}

func (svc *TakerService) setRunning(v bool) {
	svc.mu.Lock()
	svc.running = v
	svc.mu.Unlock()
}

// AddPair validates, resolves decimals from the indexer, and adds a new pair.
func (svc *TakerService) AddPair(ctx context.Context, pair banco.Pair) error {
	resolved, err := svc.resolveDecimals(ctx, pair)
	if err != nil {
		return err
	}
	if err := validatePair(resolved); err != nil {
		return fmt.Errorf("invalid pair: %w", err)
	}
	return svc.pairRepo.Add(ctx, resolved)
}

// UpdatePair validates, resolves decimals from the indexer, and updates an existing pair.
func (svc *TakerService) UpdatePair(ctx context.Context, pair banco.Pair) error {
	resolved, err := svc.resolveDecimals(ctx, pair)
	if err != nil {
		return err
	}
	if err := validatePair(resolved); err != nil {
		return fmt.Errorf("invalid pair: %w", err)
	}
	return svc.pairRepo.Update(ctx, resolved)
}

// RemovePair removes a trading pair by name.
func (svc *TakerService) RemovePair(ctx context.Context, pairName string) error {
	if pairName == "" {
		return fmt.Errorf("pair name is required")
	}
	return svc.pairRepo.Remove(ctx, pairName)
}

// ListPairs returns all configured trading pairs.
func (svc *TakerService) ListPairs(ctx context.Context) ([]banco.Pair, error) {
	return svc.pairRepo.List(ctx)
}

// Balance holds the wallet balance breakdown.
type Balance struct {
	OnchainSpendable uint64
	OnchainLocked    uint64
	OffchainTotal    uint64
}

// GetBalance returns the wallet balance from the ark client.
func (svc *TakerService) GetBalance(ctx context.Context) (*Balance, error) {
	bal, err := svc.arkClient.Balance(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get balance: %w", err)
	}

	var lockedAmount uint64
	for _, locked := range bal.OnchainBalance.LockedAmount {
		lockedAmount += locked.Amount
	}

	return &Balance{
		OnchainSpendable: bal.OnchainBalance.SpendableAmount,
		OnchainLocked:    lockedAmount,
		OffchainTotal:    bal.OffchainBalance.Total,
	}, nil
}

// Address holds the wallet addresses.
type Address struct {
	OffchainAddress string
	BoardingAddress string
}

// GetAddress returns a new offchain and boarding address from the ark client.
func (svc *TakerService) GetAddress(ctx context.Context) (*Address, error) {
	offchain, err := svc.arkClient.NewOffchainAddress(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get offchain address: %w", err)
	}

	boarding, err := svc.arkClient.NewBoardingAddress(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get boarding address: %w", err)
	}

	return &Address{
		OffchainAddress: offchain,
		BoardingAddress: boarding,
	}, nil
}

// resolveDecimals parses the pair and fills BaseDecimals/QuoteDecimals using
// the indexer. BTC always resolves to 8; any other side is treated as a hex
// asset ID and looked up via indexer.GetAsset.
func (svc *TakerService) resolveDecimals(ctx context.Context, pair banco.Pair) (banco.Pair, error) {
	base, quote, ok := splitPair(pair.Pair)
	if !ok {
		return pair, fmt.Errorf("pair must be in format 'base/quote'")
	}

	baseDec, err := svc.assetDecimals(ctx, base)
	if err != nil {
		return pair, fmt.Errorf("resolve base decimals: %w", err)
	}
	quoteDec, err := svc.assetDecimals(ctx, quote)
	if err != nil {
		return pair, fmt.Errorf("resolve quote decimals: %w", err)
	}

	pair.BaseDecimals = baseDec
	pair.QuoteDecimals = quoteDec
	return pair, nil
}

func (svc *TakerService) assetDecimals(ctx context.Context, assetID string) (int, error) {
	if assetID == "BTC" {
		return btcDecimals, nil
	}
	if svc.indexer == nil {
		return 0, fmt.Errorf("indexer not configured")
	}
	info, err := svc.indexer.GetAsset(ctx, assetID)
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
		n, perr := strconv.Atoi(string(md.Value))
		if perr != nil {
			return 0, fmt.Errorf("asset %s: invalid decimals metadata %q", assetID, string(md.Value))
		}
		if n < 0 {
			return 0, fmt.Errorf("asset %s: negative decimals %d", assetID, n)
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

func validatePair(pair banco.Pair) error {
	if pair.Pair == "" {
		return fmt.Errorf("pair name is required")
	}
	if _, _, ok := splitPair(pair.Pair); !ok {
		return fmt.Errorf("pair must be in format 'base/quote'")
	}
	if pair.MinAmount == 0 {
		return fmt.Errorf("min_amount must be greater than 0")
	}
	if pair.MaxAmount == 0 {
		return fmt.Errorf("max_amount must be greater than 0")
	}
	if pair.MinAmount > pair.MaxAmount {
		return fmt.Errorf("min_amount must be less than or equal to max_amount")
	}
	if pair.PriceFeed == "" {
		return fmt.Errorf("price_feed is required")
	}
	return nil
}
