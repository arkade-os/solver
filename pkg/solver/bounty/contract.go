package bounty

import (
	"fmt"

	"github.com/ArkLabsHQ/introspector/pkg/arkade"
	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/txscript"
)

const (
	// AnnouncementPacketType is the extension packet type for Alice's bounty announcement.
	AnnouncementPacketType uint8 = 0x10
	// MiningNoncePacketType is the extension packet type for the bot's mining nonce.
	MiningNoncePacketType uint8 = 0x11
	// DustLimitSats is the conservative dust threshold used by Ark off-chain
	// outputs (sub-dust outputs are rejected by arkd unless OP_RETURN).
	DustLimitSats uint64 = 330
	// MinerFeeSats is the protocol-fixed fee paid to the mining bot per
	// bounty input. Set to the dust limit so a singleton claim's tip output
	// (1 * MinerFeeSats) is itself non-dust; batched claims (N>1) yield a
	// tip of N * MinerFeeSats and scale the bot's reward linearly.
	MinerFeeSats uint64 = DustLimitSats

	// MinDifficulty / MaxDifficulty are the inclusive bounds for the PoW
	// leading-zero-byte target. Above 32 the prefix would exceed the txid
	// length; zero would short-circuit the check.
	MinDifficulty uint8 = 1
	MaxDifficulty uint8 = 32
)

// validateDifficulty rejects out-of-range values. Centralizes the same check
// performed by the wire encoder/decoder, the maker, and the miner.
func validateDifficulty(d uint8) error {
	if d < MinDifficulty || d > MaxDifficulty {
		return fmt.Errorf("difficulty out of range [%d, %d]: %d", MinDifficulty, MaxDifficulty, d)
	}
	return nil
}

// p2trWitnessProgram returns the 32-byte taproot witness program from a 34-byte P2TR pkScript.
// Returns an error if the script does not match the OP_1 OP_DATA_32 <key> shape.
func p2trWitnessProgram(pkScript []byte) ([]byte, error) {
	if len(pkScript) != 34 {
		return nil, fmt.Errorf("expected 34-byte P2TR pkScript, got %d", len(pkScript))
	}
	if pkScript[0] != txscript.OP_1 || pkScript[1] != txscript.OP_DATA_32 {
		return nil, fmt.Errorf("not a P2TR script: prefix %02x %02x", pkScript[0], pkScript[1])
	}
	return pkScript[2:], nil
}

// payToAndPoW assembles the arkade enforcement script the introspector
// evaluates for a bounty input before signing. Combines a txid PoW check with
// a covenant pay-to-receiver-minus-fee. The output index is derived from the
// current input index via OP_PUSHCURRENTINPUTINDEX, enabling batched claims
// (input i → output i).
//
// Witness arg: none. The script self-derives indices.
//
// Layout:
//
//	# PoW check
//	OP_TXID
//	<difficulty>
//	OP_LEFT
//	<difficulty zero bytes>
//	OP_EQUALVERIFY
//
//	# Pay-to-receiver-with-fee
//	OP_PUSHCURRENTINPUTINDEX OP_DUP
//	OP_INSPECTOUTPUTSCRIPTPUBKEY
//	OP_1 OP_EQUALVERIFY
//	<receiver_witness_program> OP_EQUALVERIFY
//	OP_INSPECTOUTPUTVALUE
//	OP_PUSHCURRENTINPUTINDEX OP_INSPECTINPUTVALUE
//	<MinerFeeSats> OP_SUB
//	OP_EQUAL
func payToAndPoW(receiverPkScript []byte, difficulty uint8) ([]byte, error) {
	if err := validateDifficulty(difficulty); err != nil {
		return nil, err
	}
	witnessProgram, err := p2trWitnessProgram(receiverPkScript)
	if err != nil {
		return nil, fmt.Errorf("invalid receiver pkScript: %w", err)
	}

	// PoW segment: OP_TXID <difficulty> OP_LEFT.
	powHead := txscript.NewScriptBuilder()
	powHead.AddOp(arkade.OP_TXID)
	powHead.AddInt64(int64(difficulty))
	powHead.AddOp(arkade.OP_LEFT)
	powHeadBytes, err := powHead.Script()
	if err != nil {
		return nil, err
	}

	// Manual push of the N zero-byte prefix. txscript.ScriptBuilder.AddData
	// (and AddFullData) minify a single 0x00 byte to OP_0 (empty push), which
	// would compare unequal against the 1-byte slice OP_LEFT produces. Since
	// our prefix length is always 1..32 (within the 75-byte direct push range),
	// emit OP_DATA_<N> followed by N zero bytes manually. make() zero-inits.
	prefixPush := make([]byte, 1+int(difficulty))
	prefixPush[0] = byte(difficulty)

	// Pay-to-receiver-with-fee segment.
	tail := txscript.NewScriptBuilder()
	tail.AddOp(txscript.OP_EQUALVERIFY)
	tail.AddOp(arkade.OP_PUSHCURRENTINPUTINDEX)
	tail.AddOp(txscript.OP_DUP)
	tail.AddOp(arkade.OP_INSPECTOUTPUTSCRIPTPUBKEY)
	tail.AddOp(txscript.OP_1)
	tail.AddOp(txscript.OP_EQUALVERIFY)
	tail.AddData(witnessProgram)
	tail.AddOp(txscript.OP_EQUALVERIFY)
	tail.AddOp(arkade.OP_INSPECTOUTPUTVALUE)
	tail.AddOp(arkade.OP_PUSHCURRENTINPUTINDEX)
	tail.AddOp(arkade.OP_INSPECTINPUTVALUE)
	tail.AddInt64(int64(MinerFeeSats))
	tail.AddOp(txscript.OP_SUB)
	tail.AddOp(txscript.OP_EQUAL)
	tailBytes, err := tail.Script()
	if err != nil {
		return nil, err
	}

	out := make([]byte, 0, len(powHeadBytes)+len(prefixPush)+len(tailBytes))
	out = append(out, powHeadBytes...)
	out = append(out, prefixPush...)
	out = append(out, tailBytes...)
	return out, nil
}

// VtxoScript returns the single-closure VTXO script for a bounty.
//
// Closure: MultisigClosure{ serverPubKey, IntrospectorTweaked(payToAndPoW) }.
// No Condition — the introspector's tweaked key is unspendable unless the
// arkade enforcement script passes during introspector evaluation.
func VtxoScript(
	difficulty uint8,
	receiverPkScript []byte,
	serverPubKey, introspectorPubKey *btcec.PublicKey,
) (*script.TapscriptsVtxoScript, error) {
	if serverPubKey == nil {
		return nil, fmt.Errorf("server pubkey must not be nil")
	}
	if introspectorPubKey == nil {
		return nil, fmt.Errorf("introspector pubkey must not be nil")
	}
	enforcement, err := payToAndPoW(receiverPkScript, difficulty)
	if err != nil {
		return nil, err
	}
	tweakedKey := arkade.ComputeArkadeScriptPublicKey(
		introspectorPubKey,
		arkade.ArkadeScriptHash(enforcement),
	)
	return &script.TapscriptsVtxoScript{
		Closures: []script.Closure{
			&script.MultisigClosure{
				PubKeys: []*btcec.PublicKey{serverPubKey, tweakedKey},
			},
		},
	}, nil
}

// Address derives the bounty's ark address (V0).
func Address(
	difficulty uint8,
	receiverPkScript []byte,
	serverPubKey, introspectorPubKey *btcec.PublicKey,
	network arklib.Network,
) (string, error) {
	vtxoScript, err := VtxoScript(difficulty, receiverPkScript, serverPubKey, introspectorPubKey)
	if err != nil {
		return "", err
	}
	tapKey, _, err := vtxoScript.TapTree()
	if err != nil {
		return "", fmt.Errorf("build taptree: %w", err)
	}
	return (&arklib.Address{
		HRP:        network.Addr,
		Signer:     serverPubKey,
		VtxoTapKey: tapKey,
	}).EncodeV0()
}
