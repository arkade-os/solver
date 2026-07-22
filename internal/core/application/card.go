package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/arkade-os/solver/pkg/swap"
	"github.com/arkade-os/solver/pkg/swap/pricefeed"
)

var cardNameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

type Card struct {
	Version int32
	Name    string
	Markets []CardMarket
}

type CardMarket struct {
	Pair           string
	BaseAsset      CardAsset
	QuoteAsset     CardAsset
	PriceFeed      string
	PricePath      string
	PriceDecimals  int
	FeeBps         uint32
	MinBaseAmount  uint64
	MaxBaseAmount  uint64
	MinQuoteAmount uint64
	MaxQuoteAmount uint64
}

type CardAsset struct {
	ID       string
	Name     string
	Ticker   string
	Decimals int
}

func randomName() string {
	var b [4]byte
	// nolint:errcheck
	rand.Read(b[:])
	return "solver-" + hex.EncodeToString(b[:])
}

// RegistryCard builds the solver-registry card for the configured markets.
// An empty name defaults to a random slug the operator can rename later.
func (svc *Service) RegistryCard(ctx context.Context, name string) (*Card, error) {
	if name == "" {
		name = randomName()
	}
	markets, err := svc.marketRepo.List(ctx)
	if err != nil {
		return nil, err
	}

	meta := map[string]AssetInfo{}
	for _, m := range markets {
		for _, id := range []string{m.BaseAsset, m.QuoteAsset} {
			if isBTC(id) || meta[id].AssetID != "" {
				continue
			}
			info := AssetInfo{AssetID: id}
			if svc.indexer != nil {
				if a, err := svc.indexer.GetAsset(ctx, id); err == nil && a != nil {
					applyAssetMetadata(&info, a.Metadata)
				} else if err != nil {
					svc.log.WithError(err).Warnf("failed to resolve metadata for asset %s", id)
				}
			}
			meta[id] = info
		}
	}

	return buildCard(name, markets, meta)
}

func buildCard(name string, markets []swap.Market, meta map[string]AssetInfo) (*Card, error) {
	var errs []error
	if !cardNameRe.MatchString(name) {
		errs = append(errs, fmt.Errorf("name %q must match %s", name, cardNameRe))
	}
	if len(markets) == 0 {
		errs = append(errs, errors.New("at least one market is required"))
	}

	out := make([]CardMarket, 0, len(markets))
	for _, m := range markets {
		cm, err := buildCardMarket(m, meta)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, *cm)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}

	return &Card{Version: 0, Name: name, Markets: out}, nil
}

func buildCardMarket(m swap.Market, meta map[string]AssetInfo) (*CardMarket, error) {
	pair := m.BaseAsset + "/" + m.QuoteAsset
	fail := func(format string, args ...any) error {
		return fmt.Errorf("market %s: %s", pair, fmt.Sprintf(format, args...))
	}

	if m.MaxBaseAmount == 0 && m.MaxQuoteAmount == 0 {
		return nil, fail("no enabled direction")
	}

	base := cardAsset(m.BaseAsset, m.BaseDecimals, meta)
	quote := cardAsset(m.QuoteAsset, m.QuoteDecimals, meta)

	priceDecimals := m.BaseDecimals - m.QuoteDecimals

	pricePath := m.PricePath
	if pricePath == "" {
		var err error
		pricePath, err = pricefeed.DefaultPricePath(m.PriceFeed)
		if err != nil {
			return nil, fail("price_path cannot be derived from %q", m.PriceFeed)
		}
	}

	return &CardMarket{
		Pair:           base.Ticker + "/" + quote.Ticker,
		BaseAsset:      base,
		QuoteAsset:     quote,
		PriceFeed:      m.PriceFeed,
		PricePath:      pricePath,
		PriceDecimals:  priceDecimals,
		FeeBps:         m.FeeBps,
		MinBaseAmount:  boundAmount(m.MinBaseAmount, m.MaxBaseAmount),
		MaxBaseAmount:  m.MaxBaseAmount,
		MinQuoteAmount: boundAmount(m.MinQuoteAmount, m.MaxQuoteAmount),
		MaxQuoteAmount: m.MaxQuoteAmount,
	}, nil
}

// cardAsset resolves display metadata for an asset, falling back to the asset
// id when the indexer has none: the first 6 chars as the name, the first 4
// upper-cased as the ticker.
func cardAsset(id string, decimals int, meta map[string]AssetInfo) CardAsset {
	if isBTC(id) {
		return CardAsset{ID: "btc", Name: "Bitcoin", Ticker: "BTC", Decimals: decimals}
	}
	info := meta[id]
	name, ticker := info.Name, info.Ticker
	if name == "" {
		name = id[:min(6, len(id))]
	}
	if ticker == "" {
		ticker = strings.ToUpper(id[:min(4, len(id))])
	}
	return CardAsset{ID: id, Name: name, Ticker: ticker, Decimals: decimals}
}

// boundAmount reports a min of 0 when the side it bounds is disabled.
func boundAmount(min, max uint64) uint64 {
	if max == 0 {
		return 0
	}
	return min
}
