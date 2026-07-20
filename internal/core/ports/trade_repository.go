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
	// Error is empty on a fulfilled trade, and otherwise holds the reason the
	// offer was rejected or the fulfillment failed.
	Error     string
	CreatedAt time.Time
}

type TradeRepository interface {
	Add(ctx context.Context, trade Trade) error
	// List returns the most recent trades, newest first. status "failed" or
	// "succeeded" filters by outcome; any other value returns all.
	List(ctx context.Context, limit int, status string) ([]Trade, error)
}
