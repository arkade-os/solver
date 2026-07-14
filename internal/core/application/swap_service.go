package application

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/arkade-os/solver/internal/core/ports"
	"github.com/arkade-os/solver/pkg/swap"
)

func (svc *Service) ListTrades(ctx context.Context, limit int) ([]ports.Trade, error) {
	return svc.tradeRepo.List(ctx, limit)
}

func (svc *Service) AddPair(ctx context.Context, pair swap.Pair) (swap.Pair, error) {
	resolved, err := svc.resolveDecimals(ctx, pair)
	if err != nil {
		return swap.Pair{}, err
	}
	if err := validatePair(resolved); err != nil {
		return swap.Pair{}, fmt.Errorf("invalid pair: %w", err)
	}
	if err := svc.pairRepo.Add(ctx, resolved); err != nil {
		return swap.Pair{}, err
	}
	return resolved, nil
}

func (svc *Service) UpdatePair(ctx context.Context, pair swap.Pair) (swap.Pair, error) {
	resolved, err := svc.resolveDecimals(ctx, pair)
	if err != nil {
		return swap.Pair{}, err
	}
	if err := validatePair(resolved); err != nil {
		return swap.Pair{}, fmt.Errorf("invalid pair: %w", err)
	}
	if err := svc.pairRepo.Update(ctx, resolved); err != nil {
		return swap.Pair{}, err
	}
	return resolved, nil
}

func (svc *Service) RemovePair(ctx context.Context, pairName string) error {
	if pairName == "" {
		return fmt.Errorf("pair name is required")
	}
	return svc.pairRepo.Remove(ctx, pairName)
}

func (svc *Service) ListPairs(ctx context.Context) ([]swap.Pair, error) {
	return svc.pairRepo.List(ctx)
}

// GetBalance returns the wallet balance from the ark client.
func (svc *Service) GetBalance(ctx context.Context) (*Balance, error) {
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
		AssetBalances:    bal.AssetBalances,
	}, nil
}

// GetAddress returns a new offchain and boarding address from the ark client.
func (svc *Service) GetAddress(ctx context.Context) (*Address, error) {
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

func (svc *Service) resolveDecimals(ctx context.Context, pair swap.Pair) (swap.Pair, error) {
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
		return 8, nil
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

func validatePair(pair swap.Pair) error {
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
	if pair.SlippageBps > 5000 {
		return fmt.Errorf("slippage_bps must be at most 5000 (50%%)")
	}
	return nil
}
