package preimage

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"

	"github.com/btcsuite/btcd/btcec/v2"
	"golang.org/x/crypto/hkdf"
)

const (
	eciesPubLen   = 33
	eciesNonceLen = 12
	eciesTagLen   = 16
	eciesHeader   = eciesPubLen + eciesNonceLen
	eciesOverhead = eciesHeader + eciesTagLen

	eciesHkdfInfo = "solverd/preimage/v1"
)

// Encrypt produces ephemeralPub(33) || nonce(12) || aead(plaintext, sharedKey).
// The shared key is HKDF-SHA256(ECDH(ephPriv, recipient).X(), salt=ephemeralPub,
// info="solverd/preimage/v1").
func Encrypt(recipient *btcec.PublicKey, plaintext []byte) ([]byte, error) {
	if recipient == nil {
		return nil, errors.New("recipient pubkey must not be nil")
	}
	ephPriv, err := btcec.NewPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("ephemeral key: %w", err)
	}
	ephPub := ephPriv.PubKey().SerializeCompressed()

	symKey := deriveSymKey(ephPriv, recipient, ephPub)
	aead, err := newAEAD(symKey)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, eciesNonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}

	ct := aead.Seal(nil, nonce, plaintext, ephPub)
	out := make([]byte, 0, eciesHeader+len(ct))
	out = append(out, ephPub...)
	out = append(out, nonce...)
	out = append(out, ct...)
	return out, nil
}

// Decrypt mirrors Encrypt; returns an error on bad blob, wrong key, or tag mismatch.
func Decrypt(priv *btcec.PrivateKey, blob []byte) ([]byte, error) {
	if priv == nil {
		return nil, errors.New("priv must not be nil")
	}
	if len(blob) < eciesOverhead {
		return nil, fmt.Errorf("blob too short: %d < %d", len(blob), eciesOverhead)
	}
	ephPubBytes := blob[:eciesPubLen]
	nonce := blob[eciesPubLen : eciesPubLen+eciesNonceLen]
	ct := blob[eciesPubLen+eciesNonceLen:]

	ephPub, err := btcec.ParsePubKey(ephPubBytes)
	if err != nil {
		return nil, fmt.Errorf("parse ephemeral pubkey: %w", err)
	}
	symKey := deriveSymKey(priv, ephPub, ephPubBytes)
	aead, err := newAEAD(symKey)
	if err != nil {
		return nil, err
	}
	pt, err := aead.Open(nil, nonce, ct, ephPubBytes)
	if err != nil {
		return nil, fmt.Errorf("aead open: %w", err)
	}
	return pt, nil
}

// deriveSymKey computes HKDF-SHA256 over the ECDH shared X coordinate.
// The ephPub bytes are used both as HKDF salt and as AAD on the AEAD —
// binding the ciphertext to the exact ephemeral pubkey transmitted.
func deriveSymKey(priv *btcec.PrivateKey, peer *btcec.PublicKey, salt []byte) []byte {
	shared := ecdhX(priv, peer)
	r := hkdf.New(newSHA256, shared, salt, []byte(eciesHkdfInfo))
	out := make([]byte, 32)
	_, _ = io.ReadFull(r, out)
	return out
}

// ecdhX returns the X coordinate of priv*peer (ECDH shared secret) as 32
// big-endian bytes. Implemented manually because the btcec/v2/ecdh subpackage
// is not present in this version of btcd.
func ecdhX(priv *btcec.PrivateKey, peer *btcec.PublicKey) []byte {
	var jp btcec.JacobianPoint
	peer.AsJacobian(&jp)
	var result btcec.JacobianPoint
	btcec.ScalarMultNonConst(&priv.Key, &jp, &result)
	result.ToAffine()
	x := result.X.Bytes()
	out := make([]byte, 32)
	copy(out, x[:])
	return out
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes: %w", err)
	}
	return cipher.NewGCM(block)
}

func newSHA256() hash.Hash { return sha256.New() }
