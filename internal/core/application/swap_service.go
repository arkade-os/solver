package application

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/arkade-os/arkd/pkg/client-lib/indexer"
	arksdk "github.com/arkade-os/go-sdk"
	"github.com/sirupsen/logrus"

	"github.com/arkade-os/solver/internal/core/ports"
	"github.com/arkade-os/solver/pkg/banco"
)

const btcDecimals = 8

// NewService builds a Service with only its service-layer dependencies wired
// (pair/trade repositories, wallet, indexer). The runtime fields used by Run
// are populated by New. This constructor is handy for testing the service
// methods (and the gRPC handler) without standing up the full daemon.
func NewService(
	pairRepo ports.PairRepository,
	tradeRepo ports.TradeRepository,
	arkClient arksdk.Wallet,
	idx indexer.Indexer,
	log logrus.FieldLogger,
) *Service {
	if log == nil {
		log = logrus.StandardLogger()
	}
	return &Service{
		pairRepo:  pairRepo,
		tradeRepo: tradeRepo,
		arkClient: arkClient,
		indexer:   idx,
		log:       log,
	}
}

// ListTrades returns up to `limit` most recent fulfilled trades, newest first.
// Passing limit <= 0 uses the repository default.
func (svc *Service) ListTrades(ctx context.Context, limit int) ([]ports.Trade, error) {
	if svc.tradeRepo == nil {
		return nil, nil
	}
	return svc.tradeRepo.List(ctx, limit)
}

// AddPair validates, resolves decimals from the indexer, and adds a new pair.
func (svc *Service) AddPair(ctx context.Context, pair banco.Pair) error {
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
func (svc *Service) UpdatePair(ctx context.Context, pair banco.Pair) error {
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
func (svc *Service) RemovePair(ctx context.Context, pairName string) error {
	if pairName == "" {
		return fmt.Errorf("pair name is required")
	}
	return svc.pairRepo.Remove(ctx, pairName)
}

// ListPairs returns all configured trading pairs.
func (svc *Service) ListPairs(ctx context.Context) ([]banco.Pair, error) {
	return svc.pairRepo.List(ctx)
}

// GetBalance returns the wallet balance from the ark client.
func (svc *Service) GetBalance(ctx context.Context) (*ports.Balance, error) {
	bal, err := svc.arkClient.Balance(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get balance: %w", err)
	}

	var lockedAmount uint64
	for _, locked := range bal.OnchainBalance.LockedAmount {
		lockedAmount += locked.Amount
	}

	return &ports.Balance{
		OnchainSpendable: bal.OnchainBalance.SpendableAmount,
		OnchainLocked:    lockedAmount,
		OffchainTotal:    bal.OffchainBalance.Total,
	}, nil
}

// GetAddress returns a new offchain and boarding address from the ark client.
func (svc *Service) GetAddress(ctx context.Context) (*ports.Address, error) {
	offchain, err := svc.arkClient.NewOffchainAddress(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get offchain address: %w", err)
	}

	boarding, err := svc.arkClient.NewBoardingAddress(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get boarding address: %w", err)
	}

	return &ports.Address{
		OffchainAddress: offchain,
		BoardingAddress: boarding,
	}, nil
}

// resolveDecimals parses the pair and fills BaseDecimals/QuoteDecimals using
// the indexer. BTC always resolves to 8; any other side is treated as a hex
// asset ID and looked up via indexer.GetAsset.
func (svc *Service) resolveDecimals(ctx context.Context, pair banco.Pair) (banco.Pair, error) {
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

func (svc *Service) assetDecimals(ctx context.Context, assetID string) (int, error) {
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
