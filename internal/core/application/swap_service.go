package application

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/arkade-os/solver/internal/core/ports"
	"github.com/arkade-os/solver/pkg/banco"
)

func (svc *Service) ListTrades(ctx context.Context, limit int) ([]ports.Trade, error) {
	return svc.tradeRepo.List(ctx, limit)
}

func (svc *Service) AddPair(ctx context.Context, pair banco.Pair) (banco.Pair, error) {
	resolved, err := svc.resolveAssetMeta(ctx, pair)
	if err != nil {
		return banco.Pair{}, err
	}
	if err := validatePair(resolved); err != nil {
		return banco.Pair{}, fmt.Errorf("invalid pair: %w", err)
	}
	if err := svc.pairRepo.Add(ctx, resolved); err != nil {
		return banco.Pair{}, err
	}
	return resolved, nil
}

func (svc *Service) UpdatePair(ctx context.Context, pair banco.Pair) (banco.Pair, error) {
	resolved, err := svc.resolveAssetMeta(ctx, pair)
	if err != nil {
		return banco.Pair{}, err
	}
	if err := validatePair(resolved); err != nil {
		return banco.Pair{}, fmt.Errorf("invalid pair: %w", err)
	}
	if err := svc.pairRepo.Update(ctx, resolved); err != nil {
		return banco.Pair{}, err
	}
	return resolved, nil
}

func (svc *Service) RemovePair(ctx context.Context, pairName string) error {
	if pairName == "" {
		return fmt.Errorf("pair name is required")
	}
	return svc.pairRepo.Remove(ctx, pairName)
}

func (svc *Service) ListPairs(ctx context.Context) ([]banco.Pair, error) {
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

// resolveAssetMeta fills each side's decimals (always server-resolved) and
// its name/ticker display metadata (only when not supplied by the operator):
// the BTC side gets Bitcoin/BTC/8, asset sides fall back to the indexer's
// asset metadata.
func (svc *Service) resolveAssetMeta(ctx context.Context, pair banco.Pair) (banco.Pair, error) {
	base, quote, ok := splitPair(pair.Pair)
	if !ok {
		return pair, fmt.Errorf("pair must be in format 'base/quote'")
	}

	if err := svc.fillSideMeta(
		ctx, base, &pair.BaseName, &pair.BaseTicker, &pair.BaseDecimals,
	); err != nil {
		return pair, fmt.Errorf("resolve base asset: %w", err)
	}
	if err := svc.fillSideMeta(
		ctx, quote, &pair.QuoteName, &pair.QuoteTicker, &pair.QuoteDecimals,
	); err != nil {
		return pair, fmt.Errorf("resolve quote asset: %w", err)
	}
	return pair, nil
}

func (svc *Service) fillSideMeta(
	ctx context.Context, assetID string, name, ticker *string, decimals *int,
) error {
	if assetID == "BTC" {
		if *name == "" {
			*name = "Bitcoin"
		}
		if *ticker == "" {
			*ticker = "BTC"
		}
		*decimals = 8
		return nil
	}
	if svc.indexer == nil {
		return fmt.Errorf("indexer not configured")
	}
	info, err := svc.indexer.GetAsset(ctx, assetID)
	if err != nil {
		return fmt.Errorf("asset %s: %w", assetID, err)
	}
	if info == nil {
		return fmt.Errorf("asset %s: not found", assetID)
	}

	foundDecimals := false
	for _, md := range info.Metadata {
		key := strings.ToLower(string(md.Key))
		val := string(md.Value)
		switch key {
		case "decimals":
			n, perr := strconv.Atoi(val)
			if perr != nil {
				return fmt.Errorf("asset %s: invalid decimals metadata %q", assetID, val)
			}
			if n < 0 {
				return fmt.Errorf("asset %s: negative decimals %d", assetID, n)
			}
			*decimals = n
			foundDecimals = true
		case "name":
			if *name == "" {
				*name = val
			}
		case "ticker", "symbol":
			if *ticker == "" {
				*ticker = val
			}
		}
	}
	if !foundDecimals {
		return fmt.Errorf("asset %s: no decimals metadata", assetID)
	}
	return nil
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
	if pair.MinBaseAmount == 0 {
		return fmt.Errorf("min_base_amount must be greater than 0")
	}
	if pair.MaxBaseAmount == 0 {
		return fmt.Errorf("max_base_amount must be greater than 0")
	}
	if pair.MinBaseAmount > pair.MaxBaseAmount {
		return fmt.Errorf("min_base_amount must be less than or equal to max_base_amount")
	}
	if pair.PriceFeed == "" {
		return fmt.Errorf("price_feed is required")
	}
	if pair.PriceDecimals < 0 {
		return fmt.Errorf("price_decimals must not be negative")
	}
	for _, ticker := range []string{pair.BaseTicker, pair.QuoteTicker} {
		if strings.ContainsAny(ticker, "/ \t") {
			return fmt.Errorf("ticker %q must not contain '/' or whitespace", ticker)
		}
	}
	if pair.ToleranceBps > 5000 {
		return fmt.Errorf("tolerance_bps must be at most 5000 (50%%)")
	}
	// A market whose internal fill band is narrower than its published fee
	// cannot fill: offers priced fee_bps inside fair value would already sit
	// outside the tolerance.
	if pair.FeeBps >= pair.EffectiveToleranceBps() {
		return fmt.Errorf(
			"tolerance_bps (%d) must be greater than fee_bps (%d)",
			pair.EffectiveToleranceBps(), pair.FeeBps,
		)
	}
	return nil
}
