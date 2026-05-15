package preimage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ArkLabsHQ/introspector/pkg/arkade"
	introclient "github.com/ArkLabsHQ/introspector/pkg/client"
	"github.com/arkade-os/arkd/pkg/ark-lib/extension"
	"github.com/arkade-os/arkd/pkg/ark-lib/offchain"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/arkade-os/arkd/pkg/ark-lib/txutils"
	arksdk "github.com/arkade-os/go-sdk"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcwallet/waddrmgr"
)

// MatchedClaim is the typed intent produced by the plugin's decode stage.
type MatchedClaim struct {
	Outpoint    wire.OutPoint
	Amount      uint64
	Credentials ClaimCredentials
}

// BuildClaim builds the unsigned ark tx + checkpoint(s) that spend a single
// preimage-gated VTXO. Pure CPU.
func BuildClaim(
	matched *MatchedClaim,
	checkpointTapscriptBytes []byte,
	serverPubKey, introspectorPubKey *btcec.PublicKey,
) (*psbt.Packet, []*psbt.Packet, error) {
	if matched == nil {
		return nil, nil, errors.New("matched is nil")
	}
	creds := matched.Credentials
	if len(creds.Preimage) == 0 {
		return nil, nil, errors.New("preimage is empty")
	}

	receiverPkScript, err := ValidateArkadeScript(creds.ArkadeScript)
	if err != nil {
		return nil, nil, fmt.Errorf("re-validate arkade_script: %w", err)
	}

	vtxoScript := &script.TapscriptsVtxoScript{}
	if err := vtxoScript.Decode(creds.Taptree); err != nil {
		return nil, nil, fmt.Errorf("decode taptree: %w", err)
	}
	expectedTweaked := introspectorTweakedKey(creds.ArkadeScript, introspectorPubKey)
	claimClosure, err := findClaimClosure(vtxoScript, serverPubKey, expectedTweaked)
	if err != nil {
		return nil, nil, fmt.Errorf("find claim closure: %w", err)
	}

	revealedScript, err := claimClosure.Script()
	if err != nil {
		return nil, nil, fmt.Errorf("encode closure script: %w", err)
	}

	_, tapTree, err := vtxoScript.TapTree()
	if err != nil {
		return nil, nil, fmt.Errorf("compute taptree: %w", err)
	}
	leafHash := txscript.NewBaseTapLeaf(revealedScript).TapHash()
	merkleProof, err := tapTree.GetTaprootMerkleProof(leafHash)
	if err != nil {
		return nil, nil, fmt.Errorf("merkle proof: %w", err)
	}
	controlBlock, err := txscript.ParseControlBlock(merkleProof.ControlBlock)
	if err != nil {
		return nil, nil, fmt.Errorf("parse control block: %w", err)
	}

	introPacket, err := arkade.NewPacket(arkade.IntrospectorEntry{
		Vin:     0,
		Script:  slices.Clone(creds.ArkadeScript),
		Witness: wire.TxWitness{},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("build introspector packet: %w", err)
	}
	ext, err := extension.NewExtensionFromPackets(introPacket)
	if err != nil {
		return nil, nil, fmt.Errorf("build extension: %w", err)
	}
	extTxOut, err := ext.TxOut()
	if err != nil {
		return nil, nil, fmt.Errorf("build extension TxOut: %w", err)
	}

	outputs := []*wire.TxOut{
		{Value: int64(matched.Amount), PkScript: receiverPkScript},
		extTxOut,
	}

	vtxoInput := offchain.VtxoInput{
		Outpoint: &matched.Outpoint,
		Amount:   int64(matched.Amount),
		Tapscript: &waddrmgr.Tapscript{
			ControlBlock:   controlBlock,
			RevealedScript: revealedScript,
		},
		RevealedTapscripts: slices.Clone(creds.Taptree),
	}

	arkTx, checkpoints, err := offchain.BuildTxs(
		[]offchain.VtxoInput{vtxoInput}, outputs, checkpointTapscriptBytes,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("build offchain txs: %w", err)
	}

	preimageWitness := wire.TxWitness{slices.Clone(creds.Preimage)}
	if err := txutils.SetArkPsbtField(
		arkTx, 0, txutils.ConditionWitnessField, preimageWitness,
	); err != nil {
		return nil, nil, fmt.Errorf("set condition witness on ark tx: %w", err)
	}
	for i, cp := range checkpoints {
		if err := txutils.SetArkPsbtField(
			cp, 0, txutils.ConditionWitnessField, preimageWitness,
		); err != nil {
			return nil, nil, fmt.Errorf("set condition witness on checkpoint %d: %w", i, err)
		}
	}

	return arkTx, checkpoints, nil
}

// SubmitClaim signs and forwards the claim bundle to the introspector.
// Returns the finalized ark txid.
func SubmitClaim(
	ctx context.Context,
	arkClient arksdk.Wallet,
	introClient introclient.TransportClient,
	arkTx *psbt.Packet,
	checkpoints []*psbt.Packet,
) (string, error) {
	signedArk, err := signB64(ctx, arkClient, arkTx)
	if err != nil {
		return "", fmt.Errorf("sign ark tx: %w", err)
	}
	signedCheckpoints := make([]string, len(checkpoints))
	for i, cp := range checkpoints {
		s, err := signB64(ctx, arkClient, cp)
		if err != nil {
			return "", fmt.Errorf("sign checkpoint %d: %w", i, err)
		}
		signedCheckpoints[i] = s
	}
	finalArkB64, _, err := introClient.SubmitTx(ctx, signedArk, signedCheckpoints)
	if err != nil {
		return "", fmt.Errorf("introspector submit: %w", err)
	}
	finalPtx, err := psbt.NewFromRawBytes(strings.NewReader(finalArkB64), true)
	if err != nil {
		return "", fmt.Errorf("decode finalized claim tx: %w", err)
	}
	return finalPtx.UnsignedTx.TxHash().String(), nil
}

func findClaimClosure(
	vtxoScript *script.TapscriptsVtxoScript,
	serverPubKey, expectedTweaked *btcec.PublicKey,
) (script.Closure, error) {
	for _, c := range vtxoScript.Closures {
		cmc, ok := c.(*script.ConditionMultisigClosure)
		if !ok {
			continue
		}
		if hasExactlyTwoKeys(cmc.PubKeys, serverPubKey, expectedTweaked) {
			return cmc, nil
		}
	}
	return nil, errors.New("no ConditionMultisigClosure with (serverPubKey, expectedTweaked) found in taptree")
}

// hasExactlyTwoKeys compares via x-only Schnorr serialization because closure
// pubkeys lose Y-parity across the encode/decode boundary.
func hasExactlyTwoKeys(pubKeys []*btcec.PublicKey, a, b *btcec.PublicKey) bool {
	if len(pubKeys) != 2 {
		return false
	}
	wantA := schnorr.SerializePubKey(a)
	wantB := schnorr.SerializePubKey(b)
	k0 := schnorr.SerializePubKey(pubKeys[0])
	k1 := schnorr.SerializePubKey(pubKeys[1])
	return (bytes.Equal(k0, wantA) && bytes.Equal(k1, wantB)) ||
		(bytes.Equal(k0, wantB) && bytes.Equal(k1, wantA))
}

func signB64(ctx context.Context, arkClient arksdk.Wallet, p *psbt.Packet) (string, error) {
	b64, err := p.B64Encode()
	if err != nil {
		return "", err
	}
	return arkClient.SignTransaction(ctx, b64)
}
