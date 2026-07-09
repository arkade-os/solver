package sqlitedb

import (
	"context"
	"database/sql"
	"errors"

	"github.com/arkade-os/solver/internal/core/ports"
	"github.com/arkade-os/solver/internal/infrastructure/db/sqlite/sqlc"
	"github.com/arkade-os/solver/pkg/banco"
	"modernc.org/sqlite"
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
		return nil, translateErr(err)
	}

	pairs := make([]banco.Pair, 0, len(rows))
	for _, row := range rows {
		pairs = append(pairs, toDomainPair(row))
	}
	return pairs, nil
}

func (r *PairRepository) Add(ctx context.Context, pair banco.Pair) error {
	return translateErr(r.queries.InsertPair(ctx, sqlc.InsertPairParams{
		Pair:          pair.Pair,
		MinBaseAmount: int64(pair.MinBaseAmount),
		MaxBaseAmount: int64(pair.MaxBaseAmount),
		BaseDecimals:  int64(pair.BaseDecimals),
		QuoteDecimals: int64(pair.QuoteDecimals),
		BaseName:      pair.BaseName,
		BaseTicker:    pair.BaseTicker,
		QuoteName:     pair.QuoteName,
		QuoteTicker:   pair.QuoteTicker,
		PriceFeed:     pair.PriceFeed,
		PriceDecimals: int64(pair.PriceDecimals),
		InvertPrice:   boolToInt(pair.InvertPrice),
		ToleranceBps:  int64(pair.ToleranceBps),
		FeeBps:        int64(pair.FeeBps),
	}))
}

func (r *PairRepository) Update(ctx context.Context, pair banco.Pair) error {
	rows, err := r.queries.UpdatePair(ctx, sqlc.UpdatePairParams{
		Pair:          pair.Pair,
		MinBaseAmount: int64(pair.MinBaseAmount),
		MaxBaseAmount: int64(pair.MaxBaseAmount),
		BaseDecimals:  int64(pair.BaseDecimals),
		QuoteDecimals: int64(pair.QuoteDecimals),
		BaseName:      pair.BaseName,
		BaseTicker:    pair.BaseTicker,
		QuoteName:     pair.QuoteName,
		QuoteTicker:   pair.QuoteTicker,
		PriceFeed:     pair.PriceFeed,
		PriceDecimals: int64(pair.PriceDecimals),
		InvertPrice:   boolToInt(pair.InvertPrice),
		ToleranceBps:  int64(pair.ToleranceBps),
		FeeBps:        int64(pair.FeeBps),
	})
	if err != nil {
		return translateErr(err)
	}
	if rows == 0 {
		return ports.ErrPairNotFound
	}
	return nil
}

func (r *PairRepository) Remove(ctx context.Context, pairName string) error {
	return translateErr(r.queries.DeletePair(ctx, pairName))
}

func toDomainPair(row sqlc.BancoPair) banco.Pair {
	return banco.Pair{
		Pair:          row.Pair,
		MinBaseAmount: uint64(row.MinBaseAmount),
		MaxBaseAmount: uint64(row.MaxBaseAmount),
		BaseDecimals:  int(row.BaseDecimals),
		QuoteDecimals: int(row.QuoteDecimals),
		BaseName:      row.BaseName,
		BaseTicker:    row.BaseTicker,
		QuoteName:     row.QuoteName,
		QuoteTicker:   row.QuoteTicker,
		PriceFeed:     row.PriceFeed,
		PriceDecimals: int(row.PriceDecimals),
		InvertPrice:   row.InvertPrice != 0,
		ToleranceBps:  uint32(row.ToleranceBps),
		FeeBps:        uint32(row.FeeBps),
	}
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// translateErr keeps raw sqlite driver messages from leaking to API clients:
// a unique/primary-key violation becomes ErrPairExists, any other driver error
// becomes a generic message.
func translateErr(err error) error {
	if err == nil {
		return nil
	}
	if se, ok := errors.AsType[*sqlite.Error](err); ok {
		if se.Code()&0xff == 19 { // SQLITE_CONSTRAINT
			return ports.ErrPairExists
		}
		return errors.New("database error")
	}
	return err
}
