package sqlitedb

import (
	"context"
	"database/sql"
	"time"

	"github.com/arkade-os/solver/internal/core/ports"
	"github.com/arkade-os/solver/internal/infrastructure/db/sqlite/sqlc"
)

// TradeRepository implements ports.TradeRepository backed by SQLite.
type TradeRepository struct {
	queries *sqlc.Queries
}

// NewTradeRepository creates a new SQLite-backed TradeRepository.
func NewTradeRepository(db *sql.DB) *TradeRepository {
	return &TradeRepository{queries: sqlc.New(db)}
}

// Add inserts a new trade record.
func (r *TradeRepository) Add(ctx context.Context, t ports.Trade) error {
	return r.queries.InsertTrade(ctx, sqlc.InsertTradeParams{
		Pair:          t.Pair,
		DepositAsset:  t.DepositAsset,
		DepositAmount: int64(t.DepositAmount),
		WantAsset:     t.WantAsset,
		WantAmount:    int64(t.WantAmount),
		OfferTxid:     t.OfferTxid,
		FulfillTxid:   t.FulfillTxid,
		CreatedAt:     t.CreatedAt.Unix(),
	})
}

// List returns up to `limit` most recent trades, newest first.
func (r *TradeRepository) List(ctx context.Context, limit int) ([]ports.Trade, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.queries.ListTrades(ctx, int64(limit))
	if err != nil {
		return nil, err
	}
	out := make([]ports.Trade, 0, len(rows))
	for _, row := range rows {
		out = append(out, ports.Trade{
			ID:            row.ID,
			Pair:          row.Pair,
			DepositAsset:  row.DepositAsset,
			DepositAmount: uint64(row.DepositAmount),
			WantAsset:     row.WantAsset,
			WantAmount:    uint64(row.WantAmount),
			OfferTxid:     row.OfferTxid,
			FulfillTxid:   row.FulfillTxid,
			CreatedAt:     time.Unix(row.CreatedAt, 0).UTC(),
		})
	}
	return out, nil
}
