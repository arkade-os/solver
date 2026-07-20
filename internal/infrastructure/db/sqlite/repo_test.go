package sqlitedb_test

import (
	"context"
	"testing"
	"time"

	"github.com/arkade-os/solver/internal/core/ports"
	sqlitedb "github.com/arkade-os/solver/internal/infrastructure/db/sqlite"
	"github.com/arkade-os/solver/pkg/swap"
)

// Verifies a failed attempt round-trips with its reason, alongside a fulfilled
// trade, so the operator can tell the two apart when listing.
func TestTradeErrorRoundTrip(t *testing.T) {
	db, err := sqlitedb.OpenDB(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// nolint:errcheck
	defer db.Close()

	repo := sqlitedb.NewTradeRepository(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	failed := ports.Trade{
		Market:    "BTC/aabbcc",
		OfferTxid: "offer1",
		Error:     "insufficient offchain balance: have 1 want 2",
		CreatedAt: now,
	}
	fulfilled := ports.Trade{
		Market:      "BTC/aabbcc",
		OfferTxid:   "offer2",
		FulfillTxid: "fill2",
		CreatedAt:   now.Add(time.Second),
	}
	for _, trade := range []ports.Trade{failed, fulfilled} {
		if err := repo.Add(ctx, trade); err != nil {
			t.Fatalf("add: %v", err)
		}
	}

	trades, err := repo.List(ctx, 10, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(trades) != 2 {
		t.Fatalf("expected 2 trades, got %+v", trades)
	}
	// newest first
	if trades[0].Error != "" || trades[0].FulfillTxid != "fill2" {
		t.Fatalf("fulfilled trade mismatch: %+v", trades[0])
	}
	if trades[1].Error != failed.Error || trades[1].FulfillTxid != "" {
		t.Fatalf("failed attempt mismatch: %+v", trades[1])
	}

	onlyFailed, err := repo.List(ctx, 10, "failed")
	if err != nil {
		t.Fatalf("list failed only: %v", err)
	}
	if len(onlyFailed) != 1 || onlyFailed[0].OfferTxid != failed.OfferTxid {
		t.Fatalf("failed filter mismatch: %+v", onlyFailed)
	}

	onlySucceeded, err := repo.List(ctx, 10, "succeeded")
	if err != nil {
		t.Fatalf("list succeeded only: %v", err)
	}
	if len(onlySucceeded) != 1 || onlySucceeded[0].OfferTxid != fulfilled.OfferTxid {
		t.Fatalf("succeeded filter mismatch: %+v", onlySucceeded)
	}
}

// Verifies migrations apply on a fresh DB and both direction bound sets
// round-trip through the repository.
func TestMarketSlippageRoundTrip(t *testing.T) {
	db, err := sqlitedb.OpenDB(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// nolint:errcheck
	defer db.Close()

	repo := sqlitedb.NewMarketRepository(db)
	ctx := context.Background()

	in := swap.Market{
		BaseAsset:      "BTC",
		QuoteAsset:     "aabbcc",
		MinQuoteAmount: 1000,
		MaxQuoteAmount: 100000,
		MinBaseAmount:  500,
		MaxBaseAmount:  50000,
		PriceFeed:      "https://example.com/price",
		SlippageBps:    250,
		FeeBps:         300,
	}
	if err := repo.Add(ctx, in); err != nil {
		t.Fatalf("add: %v", err)
	}

	markets, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(markets) != 1 || markets[0].SlippageBps != 250 {
		t.Fatalf("round-trip mismatch: %+v", markets)
	}
	if markets[0].FeeBps != 300 {
		t.Fatalf("fee round-trip mismatch: %+v", markets[0])
	}
	if markets[0].MinQuoteAmount != 1000 || markets[0].MaxQuoteAmount != 100000 {
		t.Fatalf("quote bound round-trip mismatch: %+v", markets[0])
	}
	if markets[0].MinBaseAmount != 500 || markets[0].MaxBaseAmount != 50000 {
		t.Fatalf("base bound round-trip mismatch: %+v", markets[0])
	}

	in.SlippageBps = 0
	in.MinQuoteAmount = 2000
	in.MaxQuoteAmount = 200000
	in.MinBaseAmount = 1500
	in.MaxBaseAmount = 150000
	if err := repo.Update(ctx, in); err != nil {
		t.Fatalf("update: %v", err)
	}
	markets, err = repo.List(ctx)
	if err != nil {
		t.Fatalf("list after update: %v", err)
	}
	if markets[0].SlippageBps != 0 {
		t.Fatalf("update mismatch: %+v", markets)
	}
	if markets[0].EffectiveSlippageBps() != swap.DefaultSlippageBps {
		t.Fatalf("default resolution mismatch: %d", markets[0].EffectiveSlippageBps())
	}
	if markets[0].MinQuoteAmount != 2000 || markets[0].MaxQuoteAmount != 200000 {
		t.Fatalf("quote bound update mismatch: %+v", markets[0])
	}
	if markets[0].MinBaseAmount != 1500 || markets[0].MaxBaseAmount != 150000 {
		t.Fatalf("base bound update mismatch: %+v", markets[0])
	}

	if err := repo.Update(ctx, swap.Market{BaseAsset: "no", QuoteAsset: "such"}); err != ports.ErrMarketNotFound {
		t.Fatalf("update missing market: want ErrMarketNotFound, got %v", err)
	}

	if err := repo.Add(ctx, in); err != ports.ErrMarketExists {
		t.Fatalf("add duplicate: want ErrMarketExists, got %v", err)
	}

	if err := repo.Remove(ctx, in.BaseAsset, in.QuoteAsset); err != nil {
		t.Fatalf("remove: %v", err)
	}
	markets, err = repo.List(ctx)
	if err != nil {
		t.Fatalf("list after remove: %v", err)
	}
	if len(markets) != 0 {
		t.Fatalf("expected empty list after remove: %+v", markets)
	}
}
