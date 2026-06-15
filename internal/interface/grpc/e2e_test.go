package grpcservice

import (
	"context"
	"testing"

	"github.com/arkade-os/arkd/pkg/ark-lib/asset"
	"github.com/arkade-os/arkd/pkg/client-lib/indexer"
	arksdk "github.com/arkade-os/go-sdk"
	sdktypes "github.com/arkade-os/go-sdk/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	bancov1 "github.com/arkade-os/solver/api-spec/protobuf/gen/go/solverd/v1"
	"github.com/arkade-os/solver/internal/core/application"
	sqlitedb "github.com/arkade-os/solver/internal/infrastructure/db/sqlite"
)

// mockArkClient implements arksdk.Wallet with stub methods for balance and address.
type mockArkClient struct {
	arksdk.Wallet // embed for nil stubs of unused methods
}

// mockIndexer returns pre-configured decimals for test asset IDs via asset metadata.
type mockIndexer struct {
	indexer.Indexer
	decimals map[string]string
}

func (m *mockIndexer) GetAsset(_ context.Context, assetID string) (*indexer.AssetInfo, error) {
	dec, ok := m.decimals[assetID]
	if !ok {
		return &indexer.AssetInfo{AssetId: assetID}, nil
	}
	return &indexer.AssetInfo{
		AssetId:  assetID,
		Metadata: []asset.Metadata{{Key: []byte("decimals"), Value: []byte(dec)}},
	}, nil
}

func (m *mockArkClient) Balance(_ context.Context) (*sdktypes.Balance, error) {
	return &sdktypes.Balance{
		OnchainBalance: sdktypes.OnchainBalance{
			SpendableAmount: 100000,
		},
		OffchainBalance: sdktypes.OffchainBalance{
			Total: 500000,
		},
	}, nil
}

func (m *mockArkClient) NewOffchainAddress(_ context.Context) (string, error) {
	return "tark1offchain_test_address", nil
}

func (m *mockArkClient) NewBoardingAddress(_ context.Context) (string, error) {
	return "bcrt1_boarding_test_address", nil
}

// setupHandler creates a full handler backed by a real SQLite DB in a temp dir.
// It returns the handler and a cancel func for cleanup.
func setupHandler(t *testing.T) *handler {
	t.Helper()

	tmpDir := t.TempDir()

	db, err := sqlitedb.OpenDB(tmpDir)
	require.NoError(t, err, "OpenDB should succeed")

	t.Cleanup(func() {
		// nolint:errcheck
		db.Close()
	})

	pairRepo := sqlitedb.NewPairRepository(db)

	idx := &mockIndexer{decimals: map[string]string{
		"USDT": "6",
		"ETH":  "18",
		"LTC":  "8",
	}}
	tradeRepo := sqlitedb.NewTradeRepository(db)
	svc := application.NewService(pairRepo, tradeRepo, &mockArkClient{}, idx, nil)

	return newHandler(svc)
}

// validPair returns a minimal valid PairInfo for use in tests.
func validPair() *bancov1.PairInfo {
	return &bancov1.PairInfo{
		Pair:        "BTC/USDT",
		MinAmount:   1000,
		MaxAmount:   100000,
		PriceFeed:   "https://api.example.com/price",
		InvertPrice: false,
	}
}

// TestListPairs_Empty verifies that ListPairs on a fresh DB returns an empty list.
func TestListPairs_Empty(t *testing.T) {
	h := setupHandler(t)
	ctx := context.Background()

	resp, err := h.ListPairs(ctx, &bancov1.ListPairsRequest{})
	require.NoError(t, err)
	assert.Empty(t, resp.Pairs)
}

// TestAddPair_ListPairs verifies that a pair added via gRPC appears in ListPairs.
func TestAddPair_ListPairs(t *testing.T) {
	h := setupHandler(t)
	ctx := context.Background()

	pair := validPair()

	_, err := h.AddPair(ctx, &bancov1.AddPairRequest{Pair: pair})
	require.NoError(t, err)

	resp, err := h.ListPairs(ctx, &bancov1.ListPairsRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Pairs, 1)

	got := resp.Pairs[0]
	assert.Equal(t, pair.Pair, got.Pair)
	assert.Equal(t, pair.MinAmount, got.MinAmount)
	assert.Equal(t, pair.MaxAmount, got.MaxAmount)
	assert.Equal(t, pair.PriceFeed, got.PriceFeed)
	assert.Equal(t, pair.InvertPrice, got.InvertPrice)
}

// TestAddPair_InvalidInput verifies that invalid AddPair requests are rejected.
func TestAddPair_InvalidInput(t *testing.T) {
	h := setupHandler(t)
	ctx := context.Background()

	tests := []struct {
		name string
		req  *bancov1.AddPairRequest
	}{
		{
			name: "nil pair",
			req:  &bancov1.AddPairRequest{Pair: nil},
		},
		{
			name: "empty pair name",
			req: &bancov1.AddPairRequest{Pair: &bancov1.PairInfo{
				Pair:      "",
				MinAmount: 1000, MaxAmount: 100000,
				PriceFeed: "https://api.example.com/price",
			}},
		},
		{
			name: "invalid pair format (no slash)",
			req: &bancov1.AddPairRequest{Pair: &bancov1.PairInfo{
				Pair:      "BTCUSDT",
				MinAmount: 1000, MaxAmount: 100000,
				PriceFeed: "https://api.example.com/price",
			}},
		},
		{
			name: "min greater than max",
			req: &bancov1.AddPairRequest{Pair: &bancov1.PairInfo{
				Pair:      "BTC/USDT",
				MinAmount: 100000, MaxAmount: 1000,
				PriceFeed: "https://api.example.com/price",
			}},
		},
		{
			name: "empty price feed",
			req: &bancov1.AddPairRequest{Pair: &bancov1.PairInfo{
				Pair:      "BTC/USDT",
				MinAmount: 1000, MaxAmount: 100000,
				PriceFeed: "",
			}},
		},
		{
			name: "zero min amount",
			req: &bancov1.AddPairRequest{Pair: &bancov1.PairInfo{
				Pair:      "BTC/USDT",
				MinAmount: 0, MaxAmount: 100000,
				PriceFeed: "https://api.example.com/price",
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := h.AddPair(ctx, tt.req)
			assert.Error(t, err, "expected error for invalid input")
		})
	}
}

// TestUpdatePair verifies that an existing pair can be updated.
func TestUpdatePair(t *testing.T) {
	h := setupHandler(t)
	ctx := context.Background()

	pair := validPair()
	_, err := h.AddPair(ctx, &bancov1.AddPairRequest{Pair: pair})
	require.NoError(t, err)

	updated := &bancov1.PairInfo{
		Pair:        "BTC/USDT",
		MinAmount:   2000,
		MaxAmount:   200000,
		PriceFeed:   "https://api.updated.com/price",
		InvertPrice: true,
	}

	_, err = h.UpdatePair(ctx, &bancov1.UpdatePairRequest{Pair: updated})
	require.NoError(t, err)

	resp, err := h.ListPairs(ctx, &bancov1.ListPairsRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Pairs, 1)

	got := resp.Pairs[0]
	assert.Equal(t, updated.MinAmount, got.MinAmount)
	assert.Equal(t, updated.MaxAmount, got.MaxAmount)
	assert.Equal(t, updated.PriceFeed, got.PriceFeed)
	assert.Equal(t, updated.InvertPrice, got.InvertPrice)
}

// TestUpdatePair_NilPair verifies that UpdatePair with nil pair returns error.
func TestUpdatePair_NilPair(t *testing.T) {
	h := setupHandler(t)
	ctx := context.Background()

	_, err := h.UpdatePair(ctx, &bancov1.UpdatePairRequest{Pair: nil})
	assert.Error(t, err)
}

// TestRemovePair verifies that a pair can be removed and no longer appears in ListPairs.
func TestRemovePair(t *testing.T) {
	h := setupHandler(t)
	ctx := context.Background()

	pair := validPair()
	_, err := h.AddPair(ctx, &bancov1.AddPairRequest{Pair: pair})
	require.NoError(t, err)

	_, err = h.RemovePair(ctx, &bancov1.RemovePairRequest{Pair: pair.Pair})
	require.NoError(t, err)

	resp, err := h.ListPairs(ctx, &bancov1.ListPairsRequest{})
	require.NoError(t, err)
	assert.Empty(t, resp.Pairs)
}

// TestRemovePair_EmptyName verifies that RemovePair with empty name returns error.
func TestRemovePair_EmptyName(t *testing.T) {
	h := setupHandler(t)
	ctx := context.Background()

	_, err := h.RemovePair(ctx, &bancov1.RemovePairRequest{Pair: ""})
	assert.Error(t, err)
}

// TestRemovePair_NotFound verifies behavior when removing a non-existent pair.
// SQLite DELETE is idempotent so it should not error.
func TestRemovePair_NotFound(t *testing.T) {
	h := setupHandler(t)
	ctx := context.Background()

	// Remove a pair that was never added — SQLite DELETE is a no-op, not an error.
	_, err := h.RemovePair(ctx, &bancov1.RemovePairRequest{Pair: "BTC/USDT"})
	assert.NoError(t, err, "DELETE of non-existent pair should succeed silently")
}

// TestAddPair_Duplicate verifies that adding the same pair twice causes an error
// due to PRIMARY KEY conflict.
func TestAddPair_Duplicate(t *testing.T) {
	h := setupHandler(t)
	ctx := context.Background()

	pair := validPair()

	_, err := h.AddPair(ctx, &bancov1.AddPairRequest{Pair: pair})
	require.NoError(t, err)

	_, err = h.AddPair(ctx, &bancov1.AddPairRequest{Pair: pair})
	assert.Error(t, err, "adding duplicate pair should fail with PRIMARY KEY conflict")
}

// TestGetBalance verifies that GetBalance returns expected values from the mock client.
func TestGetBalance(t *testing.T) {
	h := setupHandler(t)
	ctx := context.Background()

	resp, err := h.GetBalance(ctx, &bancov1.GetBalanceRequest{})
	require.NoError(t, err)

	// OnchainConfirmed maps to mock's SpendableAmount (100000)
	assert.Equal(t, uint64(100000), resp.OnchainConfirmed)
	// OffchainSettled maps to mock's OffchainBalance.Total (500000)
	assert.Equal(t, uint64(500000), resp.OffchainSettled)
}

// TestGetAddress verifies that GetAddress returns expected values from the mock client.
func TestGetAddress(t *testing.T) {
	h := setupHandler(t)
	ctx := context.Background()

	resp, err := h.GetAddress(ctx, &bancov1.GetAddressRequest{})
	require.NoError(t, err)

	assert.Equal(t, "tark1offchain_test_address", resp.OffchainAddress)
	assert.Equal(t, "bcrt1_boarding_test_address", resp.BoardingAddress)
}

// TestMultiplePairs verifies that multiple pairs can be added and all appear in ListPairs.
func TestMultiplePairs(t *testing.T) {
	h := setupHandler(t)
	ctx := context.Background()

	pairs := []*bancov1.PairInfo{
		{
			Pair:      "BTC/USDT",
			MinAmount: 1000, MaxAmount: 100000,
			PriceFeed: "https://api.example.com/btcusdt",
		},
		{
			Pair:      "ETH/BTC",
			MinAmount: 500, MaxAmount: 50000,
			PriceFeed: "https://api.example.com/ethbtc",
		},
		{
			Pair:      "LTC/BTC",
			MinAmount: 100, MaxAmount: 10000,
			PriceFeed:   "https://api.example.com/ltcbtc",
			InvertPrice: true,
		},
	}

	for _, p := range pairs {
		_, err := h.AddPair(ctx, &bancov1.AddPairRequest{Pair: p})
		require.NoError(t, err, "adding pair %s should succeed", p.Pair)
	}

	resp, err := h.ListPairs(ctx, &bancov1.ListPairsRequest{})
	require.NoError(t, err)
	assert.Len(t, resp.Pairs, len(pairs))
}
