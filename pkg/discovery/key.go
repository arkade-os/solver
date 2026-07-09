package discovery

import (
	"encoding/hex"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"github.com/btcsuite/btcd/chaincfg"
)

// discoveryKeyPurpose is the hardened BIP32 purpose of the dedicated
// discovery-identity derivation path m/38173'/0': 38173 is the protocol's v1
// quote event kind, keeping the identity clearly separate from wallet keys.
const discoveryKeyPurpose = 38173

// ParseSecretKey decodes a 32-byte hex secret key (the format of the
// SOLVER_DISCOVERY_SECRET_KEY environment variable).
func ParseSecretKey(hexKey string) (*btcec.PrivateKey, error) {
	raw, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("invalid discovery secret key: must be hex")
	}
	if len(raw) != btcec.PrivKeyBytesLen {
		return nil, fmt.Errorf(
			"invalid discovery secret key: must be %d bytes, got %d",
			btcec.PrivKeyBytesLen, len(raw),
		)
	}
	priv, _ := btcec.PrivKeyFromBytes(raw)
	if priv.Key.IsZero() {
		return nil, fmt.Errorf("invalid discovery secret key: zero scalar")
	}
	return priv, nil
}

// DeriveSecretKey derives the discovery identity from the wallet seed at the
// dedicated path m/38173'/0', used when signing is requested and no explicit
// key is configured. The network parameters only affect serialization, not
// derivation, so mainnet params are used unconditionally.
func DeriveSecretKey(seed []byte) (*btcec.PrivateKey, error) {
	master, err := hdkeychain.NewMaster(seed, &chaincfg.MainNetParams)
	if err != nil {
		return nil, fmt.Errorf("derive discovery key: %w", err)
	}
	purpose, err := master.Derive(hdkeychain.HardenedKeyStart + discoveryKeyPurpose)
	if err != nil {
		return nil, fmt.Errorf("derive discovery key: %w", err)
	}
	child, err := purpose.Derive(hdkeychain.HardenedKeyStart)
	if err != nil {
		return nil, fmt.Errorf("derive discovery key: %w", err)
	}
	return child.ECPrivKey()
}
