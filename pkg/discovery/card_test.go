package discovery

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arkade-os/solver/pkg/banco"
)

var update = flag.Bool("update", false, "rewrite golden files")

// fixedAssetID is a stable, valid serialized AssetId (32-byte txid + 2-byte
// index) used across golden fixtures.
const fixedAssetID = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20" +
	"0000"

// fixedSecretKey is a throwaway test key; its x-only pubkey is pinned in the
// signed golden file. Never use outside tests.
const fixedSecretKey = "0000000000000000000000000000000000000000000000000000000000000001"

// fixturePairs is the fixed pair set behind the golden files. Listing order
// is deliberately not sorted: BuildCard must sort markets by id pair.
func fixturePairs() []banco.Pair {
	return []banco.Pair{
		{
			Pair:          fixedAssetID + "/BTC",
			MinBaseAmount: 5000000,
			MaxBaseAmount: 100000000,
			BaseDecimals:  6,
			QuoteDecimals: 8,
			BaseName:      "Tether USD",
			BaseTicker:    "USDT",
			QuoteName:     "Bitcoin",
			QuoteTicker:   "BTC",
			PriceFeed:     "https://feed.example.com/price?pair=USDT-BTC&x=1",
			PriceDecimals: 8,
			InvertPrice:   true,
			ToleranceBps:  80, // internal only: must NOT appear in the card
			FeeBps:        45,
		},
		{
			Pair:          "BTC/" + fixedAssetID,
			MinBaseAmount: 1000,
			MaxBaseAmount: 5000000,
			BaseDecimals:  8,
			QuoteDecimals: 6,
			BaseName:      "Bitcoin",
			BaseTicker:    "BTC",
			QuoteName:     "Tether USD",
			QuoteTicker:   "USDT",
			PriceFeed:     "https://feed.example.com/price?pair=BTC-USDT",
			PriceDecimals: 0,
			InvertPrice:   false,
			ToleranceBps:  100,
			FeeBps:        30,
		},
	}
}

func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(path, got, 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "golden file missing — run: go test ./pkg/discovery -update")
	assert.Equal(t, string(want), string(got), "golden drift in %s", name)
}

func TestBuildCardUnsignedGolden(t *testing.T) {
	got, err := BuildCard("arklabs-solver", fixturePairs(), Options{})
	require.NoError(t, err)
	checkGolden(t, "card_unsigned.golden.json", got)

	// The default card is bare: no key material at all.
	assert.NotContains(t, string(got), "discovery_pubkey")
	assert.NotContains(t, string(got), "sig")
	// tolerance is internal and never published.
	assert.NotContains(t, strings.ToLower(string(got)), "tolerance")

	require.NoError(t, ValidateCard(got), "emitted card must pass registry validation")
}

func TestBuildCardSignedGolden(t *testing.T) {
	key, err := ParseSecretKey(fixedSecretKey)
	require.NoError(t, err)

	got, err := BuildCard("arklabs-solver", fixturePairs(), Options{SecretKey: key})
	require.NoError(t, err)
	checkGolden(t, "card_signed.golden.json", got)

	require.NoError(t, ValidateCard(got), "signed card must pass registry validation")
}

// TestCanonicalGolden pins the exact canonical bytes the signature commits
// to, so any canonicalization drift (key order, whitespace, escaping) fails.
func TestCanonicalGolden(t *testing.T) {
	key, err := ParseSecretKey(fixedSecretKey)
	require.NoError(t, err)
	card, err := BuildCard("arklabs-solver", fixturePairs(), Options{SecretKey: key})
	require.NoError(t, err)

	canonical, err := canonicalJSON(card)
	require.NoError(t, err)
	checkGolden(t, "card_signed.canonical.golden.json", canonical)

	// Canonical form drops sig, keeps discovery_pubkey, has no whitespace.
	assert.NotContains(t, string(canonical), `"sig"`)
	assert.Contains(t, string(canonical), `"discovery_pubkey"`)
	assert.NotContains(t, string(canonical), "\n")
	assert.NotContains(t, string(canonical), ": ")
	// URLs must not be HTML-escaped in the canonical form.
	assert.Contains(t, string(canonical), "pair=USDT-BTC&x=1")
}

func TestBuildCardDeterministic(t *testing.T) {
	a, err := BuildCard("arklabs-solver", fixturePairs(), Options{})
	require.NoError(t, err)

	// Reversed input order must produce identical bytes.
	pairs := fixturePairs()
	pairs[0], pairs[1] = pairs[1], pairs[0]
	b, err := BuildCard("arklabs-solver", pairs, Options{})
	require.NoError(t, err)
	assert.Equal(t, string(a), string(b))
}

func TestBuildCardMarketShape(t *testing.T) {
	out, err := BuildCard("arklabs-solver", fixturePairs(), Options{})
	require.NoError(t, err)

	var card Card
	require.NoError(t, json.Unmarshal(out, &card))
	require.Len(t, card.Markets, 2)

	// Sorted by (base id, quote id): the asset-base market sorts before btc.
	first, second := card.Markets[0], card.Markets[1]
	assert.Equal(t, fixedAssetID, first.BaseAsset.ID)
	assert.Equal(t, "btc", first.QuoteAsset.ID)
	assert.Equal(t, "USDT/BTC", first.Pair)
	assert.True(t, first.Invert)
	assert.Equal(t, uint32(45), first.FeeBps)
	assert.Equal(t, uint64(5000000), first.MinBaseAmount)

	assert.Equal(t, "btc", second.BaseAsset.ID)
	assert.Equal(t, "BTC/USDT", second.Pair)
	assert.Equal(t, 8, second.BaseAsset.Precision)
	assert.Equal(t, 6, second.QuoteAsset.Precision)
}

func TestBuildCardErrors(t *testing.T) {
	valid := fixturePairs()[1] // BTC/asset

	t.Run("invalid name", func(t *testing.T) {
		_, err := BuildCard("Ark Labs!", []banco.Pair{valid}, Options{})
		assert.ErrorContains(t, err, "invalid solver name")
	})
	t.Run("missing ticker", func(t *testing.T) {
		p := valid
		p.QuoteTicker = ""
		_, err := BuildCard("s", []banco.Pair{p}, Options{})
		assert.ErrorContains(t, err, "quote ticker missing")
	})
	t.Run("bad asset id", func(t *testing.T) {
		p := valid
		p.Pair = "BTC/nothex"
		_, err := BuildCard("s", []banco.Pair{p}, Options{})
		assert.ErrorContains(t, err, "invalid asset id")
	})
	t.Run("min greater than max", func(t *testing.T) {
		p := valid
		p.MinBaseAmount, p.MaxBaseAmount = 10, 5
		_, err := BuildCard("s", []banco.Pair{p}, Options{})
		assert.ErrorContains(t, err, "min_base_amount exceeds max_base_amount")
	})
	t.Run("duplicate id pair", func(t *testing.T) {
		p2 := valid
		p2.FeeBps = 99
		_, err := BuildCard("s", []banco.Pair{valid, p2}, Options{})
		assert.ErrorContains(t, err, "duplicate market")
	})
	t.Run("non-http feed", func(t *testing.T) {
		p := valid
		p.PriceFeed = "ftp://feed.example.com/x"
		_, err := BuildCard("s", []banco.Pair{p}, Options{})
		assert.ErrorContains(t, err, "price_feed")
	})
}

func TestValidateCardNegative(t *testing.T) {
	unsigned, err := BuildCard("arklabs-solver", fixturePairs(), Options{})
	require.NoError(t, err)
	key, err := ParseSecretKey(fixedSecretKey)
	require.NoError(t, err)
	signed, err := BuildCard("arklabs-solver", fixturePairs(), Options{SecretKey: key})
	require.NoError(t, err)

	mutate := func(t *testing.T, src []byte, fn func(m map[string]any)) []byte {
		t.Helper()
		var m map[string]any
		require.NoError(t, json.Unmarshal(src, &m))
		fn(m)
		out, err := json.Marshal(m)
		require.NoError(t, err)
		return out
	}

	t.Run("unknown version", func(t *testing.T) {
		bad := mutate(t, unsigned, func(m map[string]any) { m["version"] = 1 })
		assert.ErrorContains(t, ValidateCard(bad), "unknown card version")
	})
	t.Run("bad pair asset id", func(t *testing.T) {
		bad := mutate(t, unsigned, func(m map[string]any) {
			market := m["markets"].([]any)[0].(map[string]any)
			market["base_asset"].(map[string]any)["id"] = "deadbeef"
		})
		assert.ErrorContains(t, ValidateCard(bad), "invalid asset id")
	})
	t.Run("uppercase asset id is not canonical", func(t *testing.T) {
		bad := mutate(t, unsigned, func(m map[string]any) {
			market := m["markets"].([]any)[0].(map[string]any)
			id := market["base_asset"].(map[string]any)["id"].(string)
			market["base_asset"].(map[string]any)["id"] = strings.ToUpper(id)
		})
		assert.ErrorContains(t, ValidateCard(bad), "not canonical")
	})
	t.Run("pair label mismatch", func(t *testing.T) {
		bad := mutate(t, unsigned, func(m map[string]any) {
			market := m["markets"].([]any)[0].(map[string]any)
			market["pair"] = "FOO/BAR"
		})
		assert.ErrorContains(t, ValidateCard(bad), "must equal the tickers")
	})
	t.Run("min greater than max", func(t *testing.T) {
		bad := mutate(t, unsigned, func(m map[string]any) {
			market := m["markets"].([]any)[0].(map[string]any)
			market["min_base_amount"] = float64(10)
			market["max_base_amount"] = float64(5)
		})
		assert.ErrorContains(t, ValidateCard(bad), "min_base_amount exceeds")
	})
	t.Run("sig without discovery_pubkey", func(t *testing.T) {
		bad := mutate(t, signed, func(m map[string]any) { delete(m, "discovery_pubkey") })
		assert.ErrorContains(t, ValidateCard(bad), "sig present without discovery_pubkey")
	})
	t.Run("invalid sig", func(t *testing.T) {
		bad := mutate(t, signed, func(m map[string]any) {
			sig := m["sig"].(string)
			// flip one nibble
			if sig[0] == '0' {
				sig = "1" + sig[1:]
			} else {
				sig = "0" + sig[1:]
			}
			m["sig"] = sig
		})
		err := ValidateCard(bad)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "sig")
	})
	t.Run("tampered content breaks sig", func(t *testing.T) {
		bad := mutate(t, signed, func(m map[string]any) {
			market := m["markets"].([]any)[0].(map[string]any)
			market["fee_bps"] = float64(1)
		})
		assert.ErrorContains(t, ValidateCard(bad), "does not verify")
	})
	t.Run("unknown field rejected", func(t *testing.T) {
		bad := mutate(t, unsigned, func(m map[string]any) { m["updated_at"] = 123 })
		assert.ErrorContains(t, ValidateCard(bad), "unknown field")
	})
	t.Run("tolerance_bps in market rejected", func(t *testing.T) {
		bad := mutate(t, unsigned, func(m map[string]any) {
			market := m["markets"].([]any)[0].(map[string]any)
			market["tolerance_bps"] = float64(100)
		})
		assert.ErrorContains(t, ValidateCard(bad), "unknown field")
	})
	t.Run("missing markets", func(t *testing.T) {
		bad := mutate(t, unsigned, func(m map[string]any) { delete(m, "markets") })
		assert.ErrorContains(t, ValidateCard(bad), "markets is required")
	})
	t.Run("bare card is valid", func(t *testing.T) {
		assert.NoError(t, ValidateCard(unsigned))
	})
	t.Run("pubkey without sig is valid", func(t *testing.T) {
		ok := mutate(t, signed, func(m map[string]any) { delete(m, "sig") })
		assert.NoError(t, ValidateCard(ok))
	})
}

func TestDeriveSecretKeyDeterministic(t *testing.T) {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	k1, err := DeriveSecretKey(seed)
	require.NoError(t, err)
	k2, err := DeriveSecretKey(seed)
	require.NoError(t, err)
	assert.Equal(t, k1.Serialize(), k2.Serialize())

	seed[0] ^= 0xFF
	k3, err := DeriveSecretKey(seed)
	require.NoError(t, err)
	assert.NotEqual(t, k1.Serialize(), k3.Serialize())
}

func TestParseSecretKeyErrors(t *testing.T) {
	_, err := ParseSecretKey("zz")
	assert.ErrorContains(t, err, "must be hex")
	_, err = ParseSecretKey("abcd")
	assert.ErrorContains(t, err, "32 bytes")
	_, err = ParseSecretKey(strings.Repeat("00", 32))
	assert.ErrorContains(t, err, "zero scalar")
	_, err = ParseSecretKey(fixedSecretKey)
	assert.NoError(t, err)
}
