package discovery

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

// ValidateCard runs the same checks a registry's CI runs on a submitted card,
// so an operator catches a broken card before opening a PR:
//
//   - strict schema: unknown members (e.g. tolerance_bps) are rejected
//   - version must be 0, name must be registry-safe
//   - per market: the pair label must equal "<base ticker>/<quote ticker>",
//     asset ids must be "btc" or a lowercase hex AssetId, descriptors must be
//     complete, the feed must be an absolute http(s) URL, fee_bps < 10000,
//     0 < min_base_amount <= max_base_amount
//   - the (base id, quote id) pair must be unique within the card
//   - sig requires discovery_pubkey and must verify as BIP340 over
//     sha256(canonical serialization with sig removed)
func ValidateCard(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var card Card
	if err := dec.Decode(&card); err != nil {
		return fmt.Errorf("invalid card JSON: %w", err)
	}
	if dec.More() {
		return fmt.Errorf("invalid card JSON: trailing data")
	}

	if card.Version != Version {
		return fmt.Errorf("unknown card version %d (want %d)", card.Version, Version)
	}
	if !nameRe.MatchString(card.Name) {
		return fmt.Errorf("invalid solver name %q", card.Name)
	}
	if card.Markets == nil {
		return fmt.Errorf("markets is required")
	}

	seen := make(map[string]struct{}, len(card.Markets))
	for i, m := range card.Markets {
		if err := validateMarket(m); err != nil {
			return fmt.Errorf("market %d (%s): %w", i, m.Pair, err)
		}
		key := m.BaseAsset.ID + "|" + m.QuoteAsset.ID
		if _, dup := seen[key]; dup {
			return fmt.Errorf(
				"duplicate market for id pair (%s, %s)", m.BaseAsset.ID, m.QuoteAsset.ID,
			)
		}
		seen[key] = struct{}{}
	}

	return validateSignature(card, data)
}

func validateMarket(m Market) error {
	baseID, err := canonicalAssetID(m.BaseAsset.ID)
	if err != nil {
		return fmt.Errorf("base asset: %w", err)
	}
	if baseID != m.BaseAsset.ID {
		return fmt.Errorf("base asset id %q is not canonical (want %q)", m.BaseAsset.ID, baseID)
	}
	quoteID, err := canonicalAssetID(m.QuoteAsset.ID)
	if err != nil {
		return fmt.Errorf("quote asset: %w", err)
	}
	if quoteID != m.QuoteAsset.ID {
		return fmt.Errorf("quote asset id %q is not canonical (want %q)", m.QuoteAsset.ID, quoteID)
	}
	if baseID == quoteID {
		return fmt.Errorf("base and quote assets are identical (%s)", baseID)
	}
	for side, d := range map[string]AssetDescriptor{"base": m.BaseAsset, "quote": m.QuoteAsset} {
		if d.Name == "" {
			return fmt.Errorf("%s asset name is required", side)
		}
		if d.Ticker == "" {
			return fmt.Errorf("%s asset ticker is required", side)
		}
		if d.Precision < 0 {
			return fmt.Errorf("%s asset precision must not be negative", side)
		}
	}
	if want := m.BaseAsset.Ticker + "/" + m.QuoteAsset.Ticker; m.Pair != want {
		return fmt.Errorf("pair label %q must equal the tickers %q", m.Pair, want)
	}
	if err := validateFeedURL(m.PriceFeed); err != nil {
		return err
	}
	if m.PriceDecimals < 0 {
		return fmt.Errorf("price_decimals must not be negative")
	}
	if m.FeeBps >= 10000 {
		return fmt.Errorf("fee_bps must be below 10000")
	}
	if m.MinBaseAmount == 0 {
		return fmt.Errorf("min_base_amount must be greater than 0")
	}
	if m.MinBaseAmount > m.MaxBaseAmount {
		return fmt.Errorf("min_base_amount exceeds max_base_amount")
	}
	return nil
}

// validateSignature enforces the spec's optional-signature rules: a bare card
// is valid, sig without discovery_pubkey is not, and a present sig MUST
// verify against the canonical serialization.
func validateSignature(card Card, raw []byte) error {
	if card.DiscoveryPubkey == "" && card.Sig == "" {
		return nil
	}
	if card.Sig != "" && card.DiscoveryPubkey == "" {
		return fmt.Errorf("sig present without discovery_pubkey")
	}

	pubBytes, err := hex.DecodeString(card.DiscoveryPubkey)
	if err != nil || len(pubBytes) != schnorr.PubKeyBytesLen {
		return fmt.Errorf("invalid discovery_pubkey: must be %d hex chars (x-only)", schnorr.PubKeyBytesLen*2)
	}
	pub, err := schnorr.ParsePubKey(pubBytes)
	if err != nil {
		return fmt.Errorf("invalid discovery_pubkey: %w", err)
	}

	// discovery_pubkey without sig is allowed: the key is published, quotes
	// come in v1.
	if card.Sig == "" {
		return nil
	}

	sigBytes, err := hex.DecodeString(card.Sig)
	if err != nil || len(sigBytes) != schnorr.SignatureSize {
		return fmt.Errorf("invalid sig: must be %d hex chars", schnorr.SignatureSize*2)
	}
	sig, err := schnorr.ParseSignature(sigBytes)
	if err != nil {
		return fmt.Errorf("invalid sig: %w", err)
	}

	canonical, err := canonicalJSON(raw)
	if err != nil {
		return fmt.Errorf("canonicalize card: %w", err)
	}
	digest := cardDigest(canonical)
	if !sig.Verify(digest[:], pub) {
		return fmt.Errorf("sig does not verify against discovery_pubkey over the canonical card")
	}
	return nil
}
