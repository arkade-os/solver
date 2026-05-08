package preimage

import (
	"bytes"
	"fmt"

	"github.com/ArkLabsHQ/introspector/pkg/arkade"
	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/txscript"
)

// p2trWitnessProgram returns the 32-byte taproot witness program from a
// 34-byte P2TR pkScript. Returns an error if the script does not match
// `OP_1 OP_DATA_32 <key>`.
func p2trWitnessProgram(pkScript []byte) ([]byte, error) {
	if len(pkScript) != 34 {
		return nil, fmt.Errorf("expected 34-byte P2TR pkScript, got %d", len(pkScript))
	}
	if pkScript[0] != txscript.OP_1 || pkScript[1] != txscript.OP_DATA_32 {
		return nil, fmt.Errorf("not a P2TR script: prefix %02x %02x", pkScript[0], pkScript[1])
	}
	return pkScript[2:], nil
}

// EnforcePayTo assembles the arkade enforcement script the introspector
// evaluates for an HTLC claim input before signing. The script self-derives
// the output index from OP_PUSHCURRENTINPUTINDEX, pinning output[i] to
// receiverPkScript with output[i].value >= input[i].value.
//
// Witness arg: none. Layout (mirrors introspector/test/htlc_test.go:376-395):
//
//	OP_PUSHCURRENTINPUTINDEX OP_DUP
//	OP_INSPECTOUTPUTSCRIPTPUBKEY
//	OP_1 OP_EQUALVERIFY
//	<receiverWitnessProgram> OP_EQUALVERIFY
//	OP_INSPECTOUTPUTVALUE
//	OP_PUSHCURRENTINPUTINDEX OP_INSPECTINPUTVALUE
//	OP_GREATERTHANOREQUAL
func EnforcePayTo(receiverPkScript []byte) ([]byte, error) {
	witnessProgram, err := p2trWitnessProgram(receiverPkScript)
	if err != nil {
		return nil, fmt.Errorf("invalid receiver pkScript: %w", err)
	}

	b := txscript.NewScriptBuilder()
	b.AddOp(arkade.OP_PUSHCURRENTINPUTINDEX)
	b.AddOp(arkade.OP_DUP)
	b.AddOp(arkade.OP_INSPECTOUTPUTSCRIPTPUBKEY)
	b.AddOp(arkade.OP_1)
	b.AddOp(arkade.OP_EQUALVERIFY)
	b.AddData(witnessProgram)
	b.AddOp(arkade.OP_EQUALVERIFY)
	b.AddOp(arkade.OP_INSPECTOUTPUTVALUE)
	b.AddOp(arkade.OP_PUSHCURRENTINPUTINDEX)
	b.AddOp(arkade.OP_INSPECTINPUTVALUE)
	b.AddOp(arkade.OP_GREATERTHANOREQUAL)
	return b.Script()
}

// preimageCondition returns OP_HASH160 <preimageHash> OP_EQUAL — the script
// the ConditionMultisigClosure runs against the witness preimage.
func preimageCondition(preimageHash []byte) ([]byte, error) {
	if len(preimageHash) != 20 {
		return nil, fmt.Errorf("expected 20-byte HASH160, got %d", len(preimageHash))
	}
	return txscript.NewScriptBuilder().
		AddOp(txscript.OP_HASH160).
		AddData(preimageHash).
		AddOp(txscript.OP_EQUAL).
		Script()
}

// VtxoScript returns the single-closure HTLC VTXO script. Mirrors the
// introspector test's `claim` subtest (lines 99-116):
//
//	ConditionMultisigClosure{
//	    MultisigClosure{ serverPubKey, IntrospectorTweaked(EnforcePayTo) },
//	    Condition: OP_HASH160 <preimageHash> OP_EQUAL,
//	}
//
// The introspector's tweaked key is unspendable unless the arkade enforcement
// script passes during introspector evaluation. The Condition gates the entire
// closure on the witness revealing a preimage that hashes to preimageHash.
func VtxoScript(
	preimageHash []byte,
	receiverPkScript []byte,
	serverPubKey, introspectorPubKey *btcec.PublicKey,
) (*script.TapscriptsVtxoScript, error) {
	if serverPubKey == nil {
		return nil, fmt.Errorf("server pubkey must not be nil")
	}
	if introspectorPubKey == nil {
		return nil, fmt.Errorf("introspector pubkey must not be nil")
	}
	enforcement, err := EnforcePayTo(receiverPkScript)
	if err != nil {
		return nil, err
	}
	condition, err := preimageCondition(preimageHash)
	if err != nil {
		return nil, err
	}
	tweakedKey := arkade.ComputeArkadeScriptPublicKey(
		introspectorPubKey,
		arkade.ArkadeScriptHash(enforcement),
	)
	return &script.TapscriptsVtxoScript{
		Closures: []script.Closure{
			&script.ConditionMultisigClosure{
				MultisigClosure: script.MultisigClosure{
					PubKeys: []*btcec.PublicKey{serverPubKey, tweakedKey},
				},
				Condition: condition,
			},
		},
	}, nil
}

// Address derives the HTLC ark address (V0).
func Address(
	preimageHash []byte,
	receiverPkScript []byte,
	serverPubKey, introspectorPubKey *btcec.PublicKey,
	network arklib.Network,
) (string, error) {
	vtxoScript, err := VtxoScript(preimageHash, receiverPkScript, serverPubKey, introspectorPubKey)
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

// ValidateArkadeScript accepts only the byte sequence produced by
// EnforcePayTo for some P2TR receiver. Returns the receiver pkScript on success.
func ValidateArkadeScript(arkadeScript []byte) ([]byte, error) {
	receiver, err := parseReceiverFromArkadeScript(arkadeScript)
	if err != nil {
		return nil, err
	}
	expected, err := EnforcePayTo(receiver)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(arkadeScript, expected) {
		return nil, fmt.Errorf("unsupported arkade_script shape: only EnforcePayTo is accepted in v1")
	}
	return receiver, nil
}

func parseReceiverFromArkadeScript(arkadeScript []byte) ([]byte, error) {
	if len(arkadeScript) == 0 {
		return nil, fmt.Errorf("empty arkade_script")
	}
	tokenizer := txscript.MakeScriptTokenizer(0, arkadeScript)
	var witnessPrograms [][]byte
	for tokenizer.Next() {
		if tokenizer.Opcode() == txscript.OP_DATA_32 {
			witnessPrograms = append(witnessPrograms, tokenizer.Data())
		}
	}
	if err := tokenizer.Err(); err != nil {
		return nil, fmt.Errorf("tokenize arkade_script: %w", err)
	}
	if len(witnessPrograms) == 0 {
		return nil, fmt.Errorf("arkade_script contains no OP_DATA_32 push (no receiver witness program)")
	}
	if len(witnessPrograms) > 1 {
		return nil, fmt.Errorf("arkade_script contains %d OP_DATA_32 pushes; expected exactly 1", len(witnessPrograms))
	}
	receiver := make([]byte, 0, 34)
	receiver = append(receiver, txscript.OP_1, txscript.OP_DATA_32)
	receiver = append(receiver, witnessPrograms[0]...)
	return receiver, nil
}
