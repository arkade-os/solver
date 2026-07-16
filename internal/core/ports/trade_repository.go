package ports

import (
	"context"
	"time"
)

type Trade struct {
	ID            int64
	Market        string
	DepositAsset  string
	DepositAmount uint64
	WantAsset     string
	WantAmount    uint64
	OfferTxid     string
	FulfillTxid   string
	CreatedAt     time.Time
}

type TradeRepository interface {
	Add(ctx context.Context, trade Trade) error
	List(ctx context.Context, limit int) ([]Trade, error)
}
