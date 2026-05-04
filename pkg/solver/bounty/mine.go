package bounty

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"slices"

	"github.com/arkade-os/arkd/pkg/ark-lib/extension"
	"github.com/btcsuite/btcd/btcutil/psbt"
)

// Mine iterates a uint64 BE counter inside a MiningNoncePacket appended to
// baseExt and breaks when tx.UnsignedTx.TxHash()[:difficulty] is all zero.
//
// Hot-path optimization: the OP_RETURN bytes are computed ONCE (with a
// placeholder nonce), then mutated in place each iteration — only the 8 nonce
// bytes change, and they sit at the end of the script payload (since the
// MiningNoncePacket is the last entry and its TLV's nonce field is the last
// element of that packet). The full extension serialization, packet
// validation, and OP_RETURN framing are done once instead of per-iteration.
//
// Mutates tx in place. Returns the winning nonce on success. Honors ctx
// cancellation between iterations.
//
// baseExt must contain everything that should remain in the extension besides
// the mining nonce (typically: the introspector packet).
func Mine(
	ctx context.Context,
	tx *psbt.Packet,
	baseExt extension.Extension,
	extOutputIdx int,
	difficulty uint8,
) ([]byte, error) {
	if tx == nil || tx.UnsignedTx == nil {
		return nil, fmt.Errorf("tx must not be nil")
	}
	if extOutputIdx < 0 || extOutputIdx >= len(tx.UnsignedTx.TxOut) {
		return nil, fmt.Errorf(
			"extOutputIdx %d out of range [0, %d)", extOutputIdx, len(tx.UnsignedTx.TxOut),
		)
	}
	if err := validateDifficulty(difficulty); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Compose the OP_RETURN once, with a zero-placeholder nonce.
	noncePktBytes, err := (&MiningNoncePacket{Nonce: make([]byte, NonceSize)}).ToExtensionPacket()
	if err != nil {
		return nil, fmt.Errorf("wrap nonce packet: %w", err)
	}
	pkts := append(slices.Clone(baseExt), noncePktBytes)
	ext, err := extension.NewExtensionFromPackets(pkts...)
	if err != nil {
		return nil, fmt.Errorf("assemble extension: %w", err)
	}
	pkScript, err := ext.Serialize()
	if err != nil {
		return nil, fmt.Errorf("serialize extension: %w", err)
	}
	tx.UnsignedTx.TxOut[extOutputIdx].PkScript = pkScript

	// The nonce sits at the very end of the script payload: MiningNoncePacket
	// is the last extension packet, and inside its TLV the nonce field is the
	// last element. So mutating the trailing NonceSize bytes of pkScript IS the
	// nonce mutation — no re-serialization required.
	if len(pkScript) < NonceSize {
		return nil, fmt.Errorf(
			"extension OP_RETURN too short (%d bytes) for in-place nonce", len(pkScript),
		)
	}
	noncePos := pkScript[len(pkScript)-NonceSize:]
	target := make([]byte, difficulty) // zero-initialized

	for counter := uint64(0); ; counter++ {
		// Periodic cancellation check (every 4096 iterations).
		if counter&0xFFF == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		binary.BigEndian.PutUint64(noncePos, counter)
		hash := tx.UnsignedTx.TxHash()
		if bytes.Equal(hash[:int(difficulty)], target) {
			return slices.Clone(noncePos), nil
		}
	}
}
