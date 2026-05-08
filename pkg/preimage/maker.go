package preimage

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/arkade-os/arkd/pkg/ark-lib/extension"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
)

// CreateClaimParams is the input to CreateClaim. All fields are required.
type CreateClaimParams struct {
	Preimage     []byte           // 32-byte HTLC preimage
	ReceiverPk   []byte           // 34-byte P2TR pkScript paid by the claim
	SolverPubKey *btcec.PublicKey // bot's encryption pubkey (from GetSolverPubKey)
	ServerPubKey *btcec.PublicKey // ark signer pubkey (from arkClient.GetConfigData)
	IntroPubKey  *btcec.PublicKey // introspector signer pubkey (from intro.GetInfo)
	Network      arklib.Network
}

// CreateClaimResult is what the maker submits to arkd.
type CreateClaimResult struct {
	ClaimAddress string           // address to fund
	Packet       extension.Packet // pass to clientlib.WithExtraPacket
}

// CreateClaim builds the V0 claim address and the extension packet a maker
// attaches to the funding tx. No network calls — pure CPU.
//
// Wire shape of the encrypted secret payload:
//
//	preimage (32 bytes)
//	varint(len)
//	arkadeScript bytes
func CreateClaim(p CreateClaimParams) (*CreateClaimResult, error) {
	if len(p.Preimage) != 32 {
		return nil, fmt.Errorf("preimage must be 32 bytes, got %d", len(p.Preimage))
	}
	if p.SolverPubKey == nil || p.ServerPubKey == nil || p.IntroPubKey == nil {
		return nil, errors.New("solver, server, and introspector pubkeys are required")
	}
	if len(p.ReceiverPk) != 34 {
		return nil, fmt.Errorf("receiverPk must be 34 bytes, got %d", len(p.ReceiverPk))
	}

	arkadeScript, err := EnforcePayTo(p.ReceiverPk)
	if err != nil {
		return nil, fmt.Errorf("build arkade script: %w", err)
	}

	preimageHash := btcutil.Hash160(p.Preimage)
	vtxoScript, err := VtxoScript(preimageHash, p.ReceiverPk, p.ServerPubKey, p.IntroPubKey)
	if err != nil {
		return nil, fmt.Errorf("build vtxo script: %w", err)
	}
	taptree, err := vtxoScript.Encode()
	if err != nil {
		return nil, fmt.Errorf("encode taptree: %w", err)
	}
	addr, err := Address(preimageHash, p.ReceiverPk, p.ServerPubKey, p.IntroPubKey, p.Network)
	if err != nil {
		return nil, fmt.Errorf("build claim address: %w", err)
	}

	plaintext := buildSecretPayload(p.Preimage, arkadeScript)
	ciphertext, err := Encrypt(p.SolverPubKey, plaintext)
	if err != nil {
		return nil, fmt.Errorf("encrypt: %w", err)
	}

	claim := &ClaimPacket{Ciphertext: ciphertext, Taptree: taptree}
	pkt, err := claim.ToPacket()
	if err != nil {
		return nil, err
	}

	return &CreateClaimResult{ClaimAddress: addr, Packet: pkt}, nil
}

func buildSecretPayload(preimg, arkadeScript []byte) []byte {
	out := make([]byte, 0, 32+binary.MaxVarintLen64+len(arkadeScript))
	out = append(out, preimg...)
	lenBuf := make([]byte, binary.MaxVarintLen64)
	n := binary.PutUvarint(lenBuf, uint64(len(arkadeScript)))
	out = append(out, lenBuf[:n]...)
	out = append(out, arkadeScript...)
	return out
}

// SplitSecretPayload reverses buildSecretPayload. Exported so tests (and the
// plugin's decode path) can reuse the same parser.
func SplitSecretPayload(payload []byte) (preimg, arkadeScript []byte, err error) {
	if len(payload) < 32+1 {
		return nil, nil, fmt.Errorf("payload too short: %d", len(payload))
	}
	preimg = payload[:32]
	rest := payload[32:]
	scriptLen, n := binary.Uvarint(rest)
	if n <= 0 {
		return nil, nil, errors.New("invalid arkade-script length varint")
	}
	rest = rest[n:]
	if uint64(len(rest)) != scriptLen {
		return nil, nil, fmt.Errorf("arkade-script length mismatch: want %d, got %d",
			scriptLen, len(rest))
	}
	arkadeScript = bytes.Clone(rest)
	return preimg, arkadeScript, nil
}
