package sqlitedb_test

import (
	"context"
	"testing"

	sqlitedb "github.com/arkade-os/solver/internal/infrastructure/db/sqlite"
	"github.com/arkade-os/solver/pkg/swap"
)

// Verifies migrations apply on a fresh DB and slippage round-trips through
// the repository.
func TestPairSlippageRoundTrip(t *testing.T) {
	db, err := sqlitedb.OpenDB(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// nolint:errcheck
	defer db.Close()

	repo := sqlitedb.NewPairRepository(db)
	ctx := context.Background()

	in := swap.Pair{
		Pair:        "BTC/aabbcc",
		MinAmount:   1000,
		MaxAmount:   100000,
		PriceFeed:   "https://example.com/price",
		SlippageBps: 250,
	}
	if err := repo.Add(ctx, in); err != nil {
		t.Fatalf("add: %v", err)
	}

	pairs, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(pairs) != 1 || pairs[0].SlippageBps != 250 {
		t.Fatalf("round-trip mismatch: %+v", pairs)
	}

	in.SlippageBps = 0
	if err := repo.Update(ctx, in); err != nil {
		t.Fatalf("update: %v", err)
	}
	pairs, err = repo.List(ctx)
	if err != nil {
		t.Fatalf("list after update: %v", err)
	}
	if pairs[0].SlippageBps != 0 {
		t.Fatalf("update mismatch: %+v", pairs)
	}
	if pairs[0].EffectiveSlippageBps() != swap.DefaultSlippageBps {
		t.Fatalf("default resolution mismatch: %d", pairs[0].EffectiveSlippageBps())
	}
}
