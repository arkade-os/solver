// Package discovery implements the solver side of the Arkade Market Discovery
// Protocol v0: building and validating the solver card
// (solvers/<network>/<name>.json) that operators PR to a registry repo.
//
// The card is purely advisory — it never touches the covenant, the TLV offer
// format, or the fill path. BuildCard turns the solver's configured pairs into
// the spec's card document; ValidateCard runs the same checks a registry's CI
// runs so a broken card is caught before opening a PR.
package discovery

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/arkade-os/arkd/pkg/ark-lib/asset"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"

	"github.com/arkade-os/solver/pkg/banco"
)

// Version is the card schema version this package produces and accepts.
const Version = 0

// BTCAssetID is the canonical asset id for native bitcoin in a card.
const BTCAssetID = "btc"

// AssetDescriptor describes one side of a market. ID is the canonical
// identity ("btc" or the serialized AssetId in lowercase hex); Name and
// Ticker are unverified labels chosen by the solver; Precision is the number
// of decimal places of the atomic unit (8 for BTC => amounts in sats).
type AssetDescriptor struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Ticker    string `json:"ticker"`
	Precision int    `json:"precision"`
}

// Market is one entry of the card's markets list. Field order matches the
// spec's example so emitted documents diff cleanly in a registry repo.
type Market struct {
	Pair          string          `json:"pair"`
	BaseAsset     AssetDescriptor `json:"base_asset"`
	QuoteAsset    AssetDescriptor `json:"quote_asset"`
	PriceFeed     string          `json:"price_feed"`
	PriceDecimals int             `json:"price_decimals"`
	Invert        bool            `json:"invert"`
	FeeBps        uint32          `json:"fee_bps"`
	MinBaseAmount uint64          `json:"min_base_amount"`
	MaxBaseAmount uint64          `json:"max_base_amount"`
}

// Card is the solver card document. DiscoveryPubkey and Sig are optional in
// v0: a bare card with neither is fully valid and the registry PR is the
// authentication.
type Card struct {
	Version         int      `json:"version"`
	Name            string   `json:"name"`
	DiscoveryPubkey string   `json:"discovery_pubkey,omitempty"`
	Sig             string   `json:"sig,omitempty"`
	Markets         []Market `json:"markets"`
}

// Options tunes BuildCard. The zero value produces a bare unsigned card,
// which is the default output.
type Options struct {
	// SecretKey, when non-nil, signs the card: discovery_pubkey is set to the
	// key's x-only public key and sig to a BIP340 Schnorr signature over
	// sha256 of the canonical serialization (sig removed, keys sorted, no
	// whitespace, UTF-8).
	SecretKey *btcec.PrivateKey
}

// nameRe matches registry-safe card names: the name doubles as the filename
// solvers/<network>/<name>.json and must be unique within the network
// directory.
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// BuildCard renders the solver card for the given pairs. Markets are sorted
// by (base id, quote id) so the output is deterministic regardless of pair
// listing order; the document is indented with two spaces and ends with a
// newline so diffs in the registry repo stay reviewable.
func BuildCard(name string, pairs []banco.Pair, opts Options) ([]byte, error) {
	if !nameRe.MatchString(name) {
		return nil, fmt.Errorf(
			"invalid solver name %q: must be lowercase alphanumeric with '.', '_' or '-' (it becomes the registry filename)",
			name,
		)
	}

	markets := make([]Market, 0, len(pairs))
	for _, pair := range pairs {
		m, err := marketFromPair(pair)
		if err != nil {
			return nil, fmt.Errorf("pair %q: %w", pair.Pair, err)
		}
		markets = append(markets, m)
	}
	sort.Slice(markets, func(i, j int) bool {
		if markets[i].BaseAsset.ID != markets[j].BaseAsset.ID {
			return markets[i].BaseAsset.ID < markets[j].BaseAsset.ID
		}
		return markets[i].QuoteAsset.ID < markets[j].QuoteAsset.ID
	})
	for i, m := range markets {
		for _, prev := range markets[:i] {
			if prev.BaseAsset.ID == m.BaseAsset.ID && prev.QuoteAsset.ID == m.QuoteAsset.ID {
				return nil, fmt.Errorf(
					"duplicate market for id pair (%s, %s)", m.BaseAsset.ID, m.QuoteAsset.ID,
				)
			}
		}
	}

	card := &Card{
		Version: Version,
		Name:    name,
		Markets: markets,
	}

	if opts.SecretKey != nil {
		card.DiscoveryPubkey = fmt.Sprintf("%x", schnorr.SerializePubKey(opts.SecretKey.PubKey()))
		canonical, err := canonicalJSON(mustMarshal(card))
		if err != nil {
			return nil, fmt.Errorf("canonicalize card: %w", err)
		}
		digest := cardDigest(canonical)
		sig, err := schnorr.Sign(opts.SecretKey, digest[:])
		if err != nil {
			return nil, fmt.Errorf("sign card: %w", err)
		}
		card.Sig = fmt.Sprintf("%x", sig.Serialize())
	}

	return marshalCard(card)
}

// marketFromPair maps a configured banco pair onto a card market. The pair's
// base is always the deposit side, so the spec's base-denominated limits map
// directly onto MinBaseAmount/MaxBaseAmount.
func marketFromPair(pair banco.Pair) (Market, error) {
	baseID, err := canonicalAssetID(pair.Base())
	if err != nil {
		return Market{}, fmt.Errorf("base asset: %w", err)
	}
	quoteID, err := canonicalAssetID(pair.Quote())
	if err != nil {
		return Market{}, fmt.Errorf("quote asset: %w", err)
	}
	if baseID == quoteID {
		return Market{}, fmt.Errorf("base and quote assets are identical (%s)", baseID)
	}
	if pair.BaseTicker == "" {
		return Market{}, fmt.Errorf("base ticker missing: set it with --base-ticker (or asset metadata)")
	}
	if pair.QuoteTicker == "" {
		return Market{}, fmt.Errorf("quote ticker missing: set it with --quote-ticker (or asset metadata)")
	}
	if pair.BaseName == "" {
		return Market{}, fmt.Errorf("base asset name missing: set it with --base-name (or asset metadata)")
	}
	if pair.QuoteName == "" {
		return Market{}, fmt.Errorf("quote asset name missing: set it with --quote-name (or asset metadata)")
	}
	if pair.BaseDecimals < 0 || pair.QuoteDecimals < 0 {
		return Market{}, fmt.Errorf("negative asset precision")
	}
	if pair.PriceDecimals < 0 {
		return Market{}, fmt.Errorf("negative price_decimals")
	}
	if err := validateFeedURL(pair.PriceFeed); err != nil {
		return Market{}, err
	}
	if pair.MinBaseAmount == 0 {
		return Market{}, fmt.Errorf("min_base_amount must be greater than 0")
	}
	if pair.MinBaseAmount > pair.MaxBaseAmount {
		return Market{}, fmt.Errorf("min_base_amount exceeds max_base_amount")
	}
	if pair.FeeBps >= 10000 {
		return Market{}, fmt.Errorf("fee_bps must be below 10000")
	}

	// tolerance_bps is deliberately absent: it is the solver's internal
	// enforcement knob, never a promise to makers.
	return Market{
		Pair: pair.BaseTicker + "/" + pair.QuoteTicker,
		BaseAsset: AssetDescriptor{
			ID:        baseID,
			Name:      pair.BaseName,
			Ticker:    pair.BaseTicker,
			Precision: pair.BaseDecimals,
		},
		QuoteAsset: AssetDescriptor{
			ID:        quoteID,
			Name:      pair.QuoteName,
			Ticker:    pair.QuoteTicker,
			Precision: pair.QuoteDecimals,
		},
		PriceFeed:     pair.PriceFeed,
		PriceDecimals: pair.PriceDecimals,
		Invert:        pair.InvertPrice,
		FeeBps:        pair.FeeBps,
		MinBaseAmount: pair.MinBaseAmount,
		MaxBaseAmount: pair.MaxBaseAmount,
	}, nil
}

// canonicalAssetID normalizes a pair side to the card's canonical identity:
// "btc" for native bitcoin, otherwise the serialized AssetId in lowercase hex.
func canonicalAssetID(id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("missing asset id")
	}
	if strings.EqualFold(id, "BTC") {
		return BTCAssetID, nil
	}
	lower := strings.ToLower(id)
	if len(lower) != asset.ASSET_ID_SIZE*2 {
		return "", fmt.Errorf(
			"invalid asset id %q: must be %d hex chars or \"btc\"", id, asset.ASSET_ID_SIZE*2,
		)
	}
	if _, err := asset.NewAssetIdFromString(lower); err != nil {
		return "", fmt.Errorf("invalid asset id %q: %w", id, err)
	}
	return lower, nil
}

func validateFeedURL(feed string) error {
	u, err := url.Parse(feed)
	if err != nil {
		return fmt.Errorf("invalid price_feed URL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("invalid price_feed URL %q: must be an absolute http(s) URL", feed)
	}
	return nil
}

// marshalCard renders the document bytes the operator checks into a registry:
// struct field order, two-space indent, no HTML escaping, trailing newline.
func marshalCard(card *Card) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(card); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// mustMarshal is used on values this package just built; a marshal failure is
// a programming error.
func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
