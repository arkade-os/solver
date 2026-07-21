package sqlitedb

import (
	"context"
	"database/sql"
	"errors"

	"github.com/arkade-os/solver/internal/core/ports"
	"github.com/arkade-os/solver/internal/infrastructure/db/sqlite/sqlc"
	"github.com/arkade-os/solver/pkg/swap"
	"modernc.org/sqlite"
)

type MarketRepository struct {
	queries *sqlc.Queries
}

func NewMarketRepository(db *sql.DB) *MarketRepository {
	return &MarketRepository{
		queries: sqlc.New(db),
	}
}

func (r *MarketRepository) List(ctx context.Context) ([]swap.Market, error) {
	rows, err := r.queries.ListMarkets(ctx)
	if err != nil {
		return nil, translateErr(err)
	}

	out := make([]swap.Market, 0, len(rows))
	for _, row := range rows {
		out = append(out, toDomainMarket(row))
	}
	return out, nil
}

func (r *MarketRepository) Add(ctx context.Context, m swap.Market) error {
	return translateErr(r.queries.InsertMarket(ctx, sqlc.InsertMarketParams{
		BaseAsset:      m.BaseAsset,
		QuoteAsset:     m.QuoteAsset,
		BaseDecimals:   int64(m.BaseDecimals),
		QuoteDecimals:  int64(m.QuoteDecimals),
		MinQuoteAmount: int64(m.MinQuoteAmount),
		MaxQuoteAmount: int64(m.MaxQuoteAmount),
		MinBaseAmount:  int64(m.MinBaseAmount),
		MaxBaseAmount:  int64(m.MaxBaseAmount),
		PriceFeed:      m.PriceFeed,
		PricePath:      m.PricePath,
		SlippageBps:    int64(m.SlippageBps),
		FeeBps:         int64(m.FeeBps),
	}))
}

func (r *MarketRepository) Update(ctx context.Context, m swap.Market) error {
	rows, err := r.queries.UpdateMarket(ctx, sqlc.UpdateMarketParams{
		BaseDecimals:   int64(m.BaseDecimals),
		QuoteDecimals:  int64(m.QuoteDecimals),
		MinQuoteAmount: int64(m.MinQuoteAmount),
		MaxQuoteAmount: int64(m.MaxQuoteAmount),
		MinBaseAmount:  int64(m.MinBaseAmount),
		MaxBaseAmount:  int64(m.MaxBaseAmount),
		PriceFeed:      m.PriceFeed,
		PricePath:      m.PricePath,
		SlippageBps:    int64(m.SlippageBps),
		FeeBps:         int64(m.FeeBps),
		BaseAsset:      m.BaseAsset,
		QuoteAsset:     m.QuoteAsset,
	})
	if err != nil {
		return translateErr(err)
	}
	if rows == 0 {
		return ports.ErrMarketNotFound
	}
	return nil
}

func (r *MarketRepository) Remove(ctx context.Context, base, quote string) error {
	return translateErr(r.queries.DeleteMarket(ctx, sqlc.DeleteMarketParams{
		BaseAsset:  base,
		QuoteAsset: quote,
	}))
}

func toDomainMarket(row sqlc.Market) swap.Market {
	return swap.Market{
		BaseAsset:      row.BaseAsset,
		QuoteAsset:     row.QuoteAsset,
		BaseDecimals:   int(row.BaseDecimals),
		QuoteDecimals:  int(row.QuoteDecimals),
		MinQuoteAmount: uint64(row.MinQuoteAmount),
		MaxQuoteAmount: uint64(row.MaxQuoteAmount),
		MinBaseAmount:  uint64(row.MinBaseAmount),
		MaxBaseAmount:  uint64(row.MaxBaseAmount),
		PriceFeed:      row.PriceFeed,
		PricePath:      row.PricePath,
		SlippageBps:    uint32(row.SlippageBps),
		FeeBps:         uint32(row.FeeBps),
	}
}

// translateErr keeps raw sqlite driver messages from leaking to API clients:
// a unique/primary-key violation becomes ErrMarketExists, any other driver error
// becomes a generic message.
func translateErr(err error) error {
	if err == nil {
		return nil
	}
	if se, ok := errors.AsType[*sqlite.Error](err); ok {
		if se.Code()&0xff == 19 { // SQLITE_CONSTRAINT
			return ports.ErrMarketExists
		}
		return errors.New("database error")
	}
	return err
}
