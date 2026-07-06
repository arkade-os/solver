package sqlitedb

import (
	"context"
	"database/sql"

	"github.com/arkade-os/solver/internal/core/ports"
	"github.com/arkade-os/solver/internal/infrastructure/db/sqlite/sqlc"
	"github.com/arkade-os/solver/pkg/banco"
)

type PairRepository struct {
	queries *sqlc.Queries
}

func NewPairRepository(db *sql.DB) *PairRepository {
	return &PairRepository{
		queries: sqlc.New(db),
	}
}

func (r *PairRepository) List(ctx context.Context) ([]banco.Pair, error) {
	rows, err := r.queries.ListPairs(ctx)
	if err != nil {
		return nil, err
	}

	pairs := make([]banco.Pair, 0, len(rows))
	for _, row := range rows {
		pairs = append(pairs, toDomainPair(row))
	}
	return pairs, nil
}

func (r *PairRepository) Add(ctx context.Context, pair banco.Pair) error {
	return r.queries.InsertPair(ctx, sqlc.InsertPairParams{
		Pair:          pair.Pair,
		MinAmount:     int64(pair.MinAmount),
		MaxAmount:     int64(pair.MaxAmount),
		BaseDecimals:  int64(pair.BaseDecimals),
		QuoteDecimals: int64(pair.QuoteDecimals),
		PriceFeed:     pair.PriceFeed,
		InvertPrice:   boolToInt(pair.InvertPrice),
		SlippageBps:   int64(pair.SlippageBps),
	})
}

func (r *PairRepository) Update(ctx context.Context, pair banco.Pair) error {
	rows, err := r.queries.UpdatePair(ctx, sqlc.UpdatePairParams{
		Pair:          pair.Pair,
		MinAmount:     int64(pair.MinAmount),
		MaxAmount:     int64(pair.MaxAmount),
		BaseDecimals:  int64(pair.BaseDecimals),
		QuoteDecimals: int64(pair.QuoteDecimals),
		PriceFeed:     pair.PriceFeed,
		InvertPrice:   boolToInt(pair.InvertPrice),
		SlippageBps:   int64(pair.SlippageBps),
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return ports.ErrPairNotFound
	}
	return nil
}

func (r *PairRepository) Remove(ctx context.Context, pairName string) error {
	return r.queries.DeletePair(ctx, pairName)
}

func toDomainPair(row sqlc.BancoPair) banco.Pair {
	return banco.Pair{
		Pair:          row.Pair,
		MinAmount:     uint64(row.MinAmount),
		MaxAmount:     uint64(row.MaxAmount),
		BaseDecimals:  int(row.BaseDecimals),
		QuoteDecimals: int(row.QuoteDecimals),
		PriceFeed:     row.PriceFeed,
		InvertPrice:   row.InvertPrice != 0,
		SlippageBps:   uint32(row.SlippageBps),
	}
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
