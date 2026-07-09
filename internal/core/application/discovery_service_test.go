package application

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	clienttypes "github.com/arkade-os/arkd/pkg/client-lib/types"
	arksdk "github.com/arkade-os/go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arkade-os/solver/internal/config"
	sqlitedb "github.com/arkade-os/solver/internal/infrastructure/db/sqlite"
	"github.com/arkade-os/solver/pkg/banco"
	"github.com/arkade-os/solver/pkg/discovery"
)

// fakeWallet stubs the single Wallet method BuildDiscoveryCard touches;
// any other call panics via the embedded nil interface.
type fakeWallet struct {
	arksdk.Wallet
	network arklib.Network
}

func (w *fakeWallet) GetConfigData(context.Context) (*clienttypes.Config, error) {
	return &clienttypes.Config{Network: w.network}, nil
}

const testAssetIDHex = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20" + "0000"

// newCardService builds a Service backed by a real sqlite pair repo, a fake
// wallet reporting the given network, and the given config.
func newCardService(t *testing.T, cfg *config.Config, network arklib.Network) *Service {
	t.Helper()
	db, err := sqlitedb.OpenDB(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return &Service{
		pairRepo:  sqlitedb.NewPairRepository(db),
		arkClient: &fakeWallet{network: network},
		cfg:       cfg,
	}
}

func discoveryPair() banco.Pair {
	return banco.Pair{
		Pair:          "BTC/" + testAssetIDHex,
		MinBaseAmount: 1000,
		MaxBaseAmount: 5000000,
		BaseDecimals:  8,
		QuoteDecimals: 6,
		BaseName:      "Bitcoin",
		BaseTicker:    "BTC",
		QuoteName:     "Tether USD",
		QuoteTicker:   "USDT",
		PriceFeed:     "https://feed.example.com/price?pair=BTC-USDT",
		ToleranceBps:  80,
		FeeBps:        30,
	}
}

// TestBuildDiscoveryCardAcceptance walks the spec's acceptance criteria:
// the card validates, reflects the configured fee/limits/feed, contains no
// tolerance, changes when the pair changes, and the registry path hint maps
// arkd's network onto the registry directory.
func TestBuildDiscoveryCardAcceptance(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{SolverName: "test-solver", WalletSeed: strings.Repeat("42", 32)}
	svc := newCardService(t, cfg, arklib.Bitcoin)
	require.NoError(t, svc.pairRepo.Add(ctx, discoveryPair()))

	card, path, err := svc.BuildDiscoveryCard(ctx, false)
	require.NoError(t, err)

	assert.Equal(t, "solvers/mainnet/test-solver.json", path, "arkd 'bitcoin' maps to registry 'mainnet'")
	require.NoError(t, discovery.ValidateCard(card), "card must pass registry validation")
	assert.NotContains(t, strings.ToLower(string(card)), "tolerance")

	var parsed discovery.Card
	require.NoError(t, json.Unmarshal(card, &parsed))
	require.Len(t, parsed.Markets, 1)
	m := parsed.Markets[0]
	assert.Equal(t, "BTC/USDT", m.Pair)
	assert.Equal(t, "btc", m.BaseAsset.ID)
	assert.Equal(t, testAssetIDHex, m.QuoteAsset.ID)
	assert.Equal(t, uint32(30), m.FeeBps)
	assert.Equal(t, uint64(1000), m.MinBaseAmount)
	assert.Equal(t, uint64(5000000), m.MaxBaseAmount)
	assert.Equal(t, "https://feed.example.com/price?pair=BTC-USDT", m.PriceFeed)

	// Changing the pair changes the card.
	updated := discoveryPair()
	updated.FeeBps = 45
	require.NoError(t, svc.pairRepo.Update(ctx, updated))
	card2, _, err := svc.BuildDiscoveryCard(ctx, false)
	require.NoError(t, err)
	assert.NotEqual(t, string(card), string(card2))
	assert.Contains(t, string(card2), `"fee_bps": 45`)
}

func TestBuildDiscoveryCardSigned(t *testing.T) {
	ctx := context.Background()

	t.Run("explicit env key", func(t *testing.T) {
		cfg := &config.Config{
			SolverName:         "test-solver",
			DiscoverySecretKey: strings.Repeat("11", 32),
		}
		svc := newCardService(t, cfg, arklib.BitcoinSigNet)
		require.NoError(t, svc.pairRepo.Add(ctx, discoveryPair()))

		card, path, err := svc.BuildDiscoveryCard(ctx, true)
		require.NoError(t, err)
		assert.Equal(t, "solvers/signet/test-solver.json", path)
		assert.Contains(t, string(card), `"discovery_pubkey"`)
		assert.Contains(t, string(card), `"sig"`)
		require.NoError(t, discovery.ValidateCard(card), "signed card must verify")
	})

	t.Run("key derived from wallet seed", func(t *testing.T) {
		cfg := &config.Config{
			SolverName: "test-solver",
			WalletSeed: strings.Repeat("42", 32),
		}
		svc := newCardService(t, cfg, arklib.BitcoinMutinyNet)
		require.NoError(t, svc.pairRepo.Add(ctx, discoveryPair()))

		card, path, err := svc.BuildDiscoveryCard(ctx, true)
		require.NoError(t, err)
		assert.Equal(t, "solvers/mutinynet/test-solver.json", path)
		require.NoError(t, discovery.ValidateCard(card))
	})
}

func TestBuildDiscoveryCardRequiresName(t *testing.T) {
	svc := newCardService(t, &config.Config{}, arklib.Bitcoin)
	_, _, err := svc.BuildDiscoveryCard(context.Background(), false)
	assert.ErrorContains(t, err, "SOLVER_NAME")
}
