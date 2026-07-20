package application

import (
	"context"
	"fmt"
	"strconv"

	"github.com/arkade-os/solver/internal/core/ports"
	"github.com/arkade-os/solver/pkg/swap"
)

func (svc *Service) ListTrades(ctx context.Context, limit int, status string) ([]ports.Trade, error) {
	return svc.tradeRepo.List(ctx, limit, status)
}

func (svc *Service) AddMarket(ctx context.Context, m swap.Market) (swap.Market, error) {
	resolved, err := svc.resolveDecimals(ctx, m)
	if err != nil {
		return swap.Market{}, err
	}
	if err := validateMarket(resolved); err != nil {
		return swap.Market{}, fmt.Errorf("invalid market: %w", err)
	}
	if err := svc.marketRepo.Add(ctx, resolved); err != nil {
		return swap.Market{}, err
	}
	return resolved, nil
}

func (svc *Service) UpdateMarket(ctx context.Context, m swap.Market) (swap.Market, error) {
	resolved, err := svc.resolveDecimals(ctx, m)
	if err != nil {
		return swap.Market{}, err
	}
	if err := validateMarket(resolved); err != nil {
		return swap.Market{}, fmt.Errorf("invalid market: %w", err)
	}
	if err := svc.marketRepo.Update(ctx, resolved); err != nil {
		return swap.Market{}, err
	}
	return resolved, nil
}

func (svc *Service) RemoveMarket(ctx context.Context, base, quote string) error {
	if base == "" || quote == "" {
		return fmt.Errorf("base and quote assets are required")
	}
	return svc.marketRepo.Remove(ctx, base, quote)
}

func (svc *Service) ListMarkets(ctx context.Context) ([]swap.Market, error) {
	return svc.marketRepo.List(ctx)
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

func (svc *Service) resolveDecimals(ctx context.Context, m swap.Market) (swap.Market, error) {
	if m.BaseAsset == "" || m.QuoteAsset == "" {
		return m, fmt.Errorf("base and quote assets are required")
	}
	baseDec, err := svc.assetDecimals(ctx, m.BaseAsset)
	if err != nil {
		return m, fmt.Errorf("resolve base decimals: %w", err)
	}
	quoteDec, err := svc.assetDecimals(ctx, m.QuoteAsset)
	if err != nil {
		return m, fmt.Errorf("resolve quote decimals: %w", err)
	}
	m.BaseDecimals = baseDec
	m.QuoteDecimals = quoteDec
	return m, nil
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

func validateMarket(m swap.Market) error {
	if m.BaseAsset == "" || m.QuoteAsset == "" {
		return fmt.Errorf("base and quote assets are required")
	}
	if m.BaseAsset == m.QuoteAsset {
		return fmt.Errorf("base and quote assets must differ")
	}
	sellEnabled := m.MaxQuoteAmount != 0
	buyEnabled := m.MaxBaseAmount != 0
	if !sellEnabled && !buyEnabled {
		return fmt.Errorf("at least one direction must be enabled (set a non-zero max)")
	}
	if sellEnabled && m.MinQuoteAmount > m.MaxQuoteAmount {
		return fmt.Errorf("min_quote_amount must be <= max_quote_amount")
	}
	if buyEnabled && m.MinBaseAmount > m.MaxBaseAmount {
		return fmt.Errorf("min_base_amount must be <= max_base_amount")
	}
	if m.PriceFeed == "" {
		return fmt.Errorf("price_feed is required")
	}
	if m.SlippageBps > 5000 {
		return fmt.Errorf("slippage_bps must be at most 5000 (50%%)")
	}
	if m.FeeBps > 5000 {
		return fmt.Errorf("fee_bps must be at most 5000 (50%%)")
	}
	return nil
}
