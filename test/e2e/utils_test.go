package e2e_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	emulatorclient "github.com/arkade-os/emulator/pkg/client"

	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/arkade-os/arkd/pkg/ark-lib/asset"
	"github.com/arkade-os/arkd/pkg/ark-lib/extension"
	"github.com/arkade-os/arkd/pkg/ark-lib/offchain"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/arkade-os/arkd/pkg/client-lib/indexer"
	clientTypes "github.com/arkade-os/arkd/pkg/client-lib/types"
	arksdk "github.com/arkade-os/go-sdk"
	sdkcontract "github.com/arkade-os/go-sdk/contract"
	sdktypes "github.com/arkade-os/go-sdk/types"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcwallet/waddrmgr"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	password     = "secret"
	arkdURL      = "localhost:7170"
	arkdHTTPURL  = "http://localhost:7171"
	emulatorAddr = "localhost:7173"
)

// setupArkClient builds, inits, and unlocks a fresh ark wallet on a temp
// datadir. Mirrors go-sdk's test/e2e setupClient.
//
// Auto-settle is disabled by default: e2e flows immediately spend every
// VTXO (fund swap → fulfill → claim), so the scheduler racing those
// spends only produces VTXO_ALREADY_SPENT noise in the logs without
// affecting correctness.
func setupArkClient(t *testing.T, opts ...arksdk.WalletOption) arksdk.Wallet {
	t.Helper()

	opts = append([]arksdk.WalletOption{arksdk.WithoutAutoSettle()}, opts...)
	arkClient, err := arksdk.NewWallet(t.TempDir(), opts...)
	require.NoError(t, err)

	err = arkClient.Init(t.Context(), arkdURL, "", password)
	require.NoError(t, err)

	err = arkClient.Unlock(t.Context(), password)
	require.NoError(t, err)

	synced := <-arkClient.IsSynced(t.Context())
	require.Nil(t, synced.Err)
	require.True(t, synced.Synced)

	t.Cleanup(arkClient.Stop)

	return arkClient
}

// faucetOffchain redeems a fresh admin-note for the wallet and waits for the
// resulting VTXO via the wallet's vtxo event channel. Mirrors go-sdk's
// test/e2e faucetOffchain.
func faucetOffchain(t *testing.T, client arksdk.Wallet, amount float64) clientTypes.Vtxo {
	t.Helper()
	ctx := t.Context()

	note := generateNote(t, uint64(amount*1e8))

	vtxoCh := client.GetVtxoEventChannel(ctx)

	vtxoCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	done := make(chan clientTypes.Vtxo, 1)
	go func() {
		for {
			select {
			case <-vtxoCtx.Done():
				return
			case event, ok := <-vtxoCh:
				if !ok {
					return
				}
				if event.Type != sdktypes.VtxosAdded || len(event.Vtxos) == 0 {
					continue
				}
				done <- event.Vtxos[0]
				return
			}
		}
	}()

	txid, err := client.RedeemNotes(ctx, []string{note})
	require.NoError(t, err)
	require.NotEmpty(t, txid)

	select {
	case v := <-done:
		return v
	case <-vtxoCtx.Done():
		t.Fatal("faucetOffchain: timed out waiting for VtxosAdded event")
		return clientTypes.Vtxo{}
	}
}

// generateNote requests a fresh admin note from arkd for `amount` sats.
func generateNote(t *testing.T, amount uint64) string {
	t.Helper()
	note, err := generateNoteCtx(t.Context(), amount)
	require.NoError(t, err)
	return note
}

// generateNoteCtx is the context-aware variant used from non-test setup
// helpers (e.g. TestMain).
func generateNoteCtx(_ context.Context, amount uint64) (string, error) {
	httpClient := &http.Client{Timeout: 15 * time.Second}
	reqBody := bytes.NewReader([]byte(fmt.Sprintf(`{"amount": "%d"}`, amount)))
	req, err := http.NewRequest("POST", arkdHTTPURL+"/v1/admin/note", reqBody)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Basic YWRtaW46YWRtaW4=")
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	// nolint:errcheck
	defer resp.Body.Close()
	var noteResp struct {
		Notes []string `json:"notes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&noteResp); err != nil {
		return "", err
	}
	if len(noteResp.Notes) == 0 {
		return "", fmt.Errorf("no notes returned from admin API")
	}
	return noteResp.Notes[0], nil
}

func runCommand(ctx context.Context, command string) (string, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func faucet(ctx context.Context, address string, amount float64) error {
	command := fmt.Sprintf("nigiri faucet %s %.8f", address, amount)
	_, err := runCommand(ctx, command)
	return err
}

// sendOffChainToVHTLC funds claimAddr while attaching pkt as the OP_RETURN
// extension AND setting encodedTapTree on the funding output's
// POutput.TaprootTapTree. go-sdk's SendOffChain wrapper does not expose
// per-output TapTree, so this bypasses it and goes through
// offchain.BuildTxs + Client().SubmitTx + Client().FinalizeTx directly.
// It assumes one suitable spendable VTXO and BTC-only payment.
func sendOffChainToVHTLC(
	t *testing.T,
	c arksdk.Wallet,
	claimAddr string,
	amount uint64,
	encodedTapTree []byte,
	pkt extension.Packet,
) {
	t.Helper()
	ctx := t.Context()

	cfgData, err := c.GetConfigData(ctx)
	require.NoError(t, err)

	spendable, _, err := c.ListVtxos(ctx)
	require.NoError(t, err)

	cm := c.ContractManager()
	var (
		chosen      clientTypes.Vtxo
		tapscripts  []string
		signingKeys map[string]string
		found       bool
	)
	for _, v := range spendable {
		if v.IsRecoverable() || v.Spent {
			continue
		}
		if v.Amount < amount+cfgData.Dust {
			continue
		}
		contracts, err := cm.GetContracts(ctx, sdkcontract.WithScripts([]string{v.Script}))
		require.NoError(t, err)
		if len(contracts) == 0 {
			continue
		}
		handler, err := cm.GetHandler(ctx, contracts[0])
		require.NoError(t, err)
		ts, err := handler.GetTapscripts(contracts[0])
		require.NoError(t, err)
		// GetKeyRefs returns BOTH the contract's original script and the
		// derived checkpoint script keyed to the same keyId. The ark tx
		// spends a synthetic checkpoint VTXO whose pkScript is the latter,
		// so the keys map must cover it for identity.SignTransaction.
		keys, err := handler.GetKeyRefs(contracts[0])
		require.NoError(t, err)
		chosen = v
		tapscripts = ts
		signingKeys = keys
		found = true
		break
	}
	require.True(t, found, "no spendable VTXO with sufficient amount (need %d)", amount)

	vs, err := script.ParseVtxoScript(tapscripts)
	require.NoError(t, err)
	forfeitClosures := vs.ForfeitClosures()
	require.NotEmpty(t, forfeitClosures)
	forfeitScript, err := forfeitClosures[0].Script()
	require.NoError(t, err)
	leafHash := txscript.NewBaseTapLeaf(forfeitScript).TapHash()

	_, tapTree, err := vs.TapTree()
	require.NoError(t, err)
	merkleProof, err := tapTree.GetTaprootMerkleProof(leafHash)
	require.NoError(t, err)
	controlBlock, err := txscript.ParseControlBlock(merkleProof.ControlBlock)
	require.NoError(t, err)

	txidHash, err := chainhash.NewHashFromStr(chosen.Txid)
	require.NoError(t, err)
	in := offchain.VtxoInput{
		Outpoint: &wire.OutPoint{Hash: *txidHash, Index: chosen.VOut},
		Amount:   int64(chosen.Amount),
		Tapscript: &waddrmgr.Tapscript{
			ControlBlock:   controlBlock,
			RevealedScript: forfeitScript,
		},
		RevealedTapscripts: tapscripts,
	}

	vhtlcArk, err := arklib.DecodeAddressV0(claimAddr)
	require.NoError(t, err)
	vhtlcPkScript, err := script.P2TRScript(vhtlcArk.VtxoTapKey)
	require.NoError(t, err)

	outputs := []*wire.TxOut{{Value: int64(amount), PkScript: vhtlcPkScript}}

	if change := chosen.Amount - amount; change > 0 {
		changeAddr, err := c.NewOffchainAddress(ctx)
		require.NoError(t, err)
		myArk, err := arklib.DecodeAddressV0(changeAddr)
		require.NoError(t, err)
		myPkScript, err := script.P2TRScript(myArk.VtxoTapKey)
		require.NoError(t, err)
		outputs = append(outputs, &wire.TxOut{Value: int64(change), PkScript: myPkScript})
	}

	ext, err := extension.NewExtensionFromPackets(pkt)
	require.NoError(t, err)
	extTxOut, err := ext.TxOut()
	require.NoError(t, err)
	outputs = append(outputs, extTxOut)

	arkTx, checkpointTxs, err := offchain.BuildTxs(
		[]offchain.VtxoInput{in}, outputs, cfgData.CheckpointExitPath(),
	)
	require.NoError(t, err)

	vhtlcIdx := -1
	for i, out := range arkTx.UnsignedTx.TxOut {
		if bytes.Equal(out.PkScript, vhtlcPkScript) {
			vhtlcIdx = i
			break
		}
	}
	require.GreaterOrEqual(t, vhtlcIdx, 0, "VHTLC output not found in built ark tx")
	arkTx.Outputs[vhtlcIdx].TaprootTapTree = encodedTapTree

	arkB64, err := arkTx.B64Encode()
	require.NoError(t, err)
	// The ark tx's input pkScript is the checkpoint script (not the
	// original contract script), so c.SignTransaction's contract-based
	// key lookup silently no-ops. Sign via the identity directly with
	// the keys map that covers the checkpoint script (see GetKeyRefs).
	signedArk, err := c.Identity().SignTransaction(ctx, arkB64, signingKeys)
	require.NoError(t, err)

	cpB64s := make([]string, len(checkpointTxs))
	for i, cp := range checkpointTxs {
		b64, err := cp.B64Encode()
		require.NoError(t, err)
		cpB64s[i] = b64
	}

	arkTxid, _, serverSignedCps, err := c.Client().SubmitTx(ctx, signedArk, cpB64s)
	require.NoError(t, err)

	finalCps := make([]string, len(serverSignedCps))
	for i, cp := range serverSignedCps {
		sig, err := c.SignTransaction(ctx, cp)
		require.NoError(t, err)
		finalCps[i] = sig
	}
	require.NoError(t, c.Client().FinalizeTx(ctx, arkTxid, finalCps))
}

// sendOffChainWithExtension funds an address with a receiver while attaching
// an arbitrary extension packet to the ark transaction's OP_RETURN.
func sendOffChainWithExtension(
	t *testing.T,
	c arksdk.Wallet,
	receiver clientTypes.Receiver,
	pkt extension.Packet,
) {
	t.Helper()
	_, err := c.SendOffChain(
		t.Context(),
		[]clientTypes.Receiver{receiver},
		arksdk.WithExtension(pkt),
	)
	require.NoError(t, err)
}

func newEmulatorClient(t *testing.T) emulatorclient.TransportClient {
	t.Helper()
	conn, err := grpc.NewClient(emulatorAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	return emulatorclient.NewGRPCClient(conn)
}

func issueAsset(t *testing.T, client arksdk.Wallet, supply uint64) string {
	t.Helper()
	// solverd's pair validation looks up "decimals" metadata via the indexer,
	// so every test asset must publish it (zero-decimal assets are fine).
	decimalsMd, err := asset.NewMetadata("decimals", "0")
	require.NoError(t, err)
	_, assetIds, err := client.IssueAsset(t.Context(), supply, nil, []asset.Metadata{*decimalsMd})
	require.NoError(t, err)
	require.Len(t, assetIds, 1)
	return assetIds[0].String()
}

// fetchIntroPubkey returns the emulator signer pubkey, parsed.
func fetchIntroPubkey(t *testing.T, emulatorClient emulatorclient.TransportClient) *btcec.PublicKey {
	t.Helper()
	info, err := emulatorClient.GetInfo(t.Context())
	require.NoError(t, err)
	raw, err := hex.DecodeString(info.SignerPublicKey)
	require.NoError(t, err)
	pub, err := btcec.ParsePubKey(raw)
	require.NoError(t, err)
	return pub
}

// freshTaprootPkScript returns a fresh 34-byte P2TR script for a throwaway key.
func freshTaprootPkScript(t *testing.T) []byte {
	t.Helper()
	priv, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	pkScript, err := txscript.PayToTaprootScript(priv.PubKey())
	require.NoError(t, err)
	return pkScript
}

// indexerVtxo is a small projection of the bot indexer's GetVtxos response —
// just the fields tests need.
type indexerVtxo struct {
	Txid   string
	VOut   uint32
	Amount uint64
}

// pollForVtxoAt polls the indexer for any spendable VTXO at the given pkScript.
// Returns the first match or fails the test on timeout. Used when the address
// isn't owned by a wallet (e.g. preimage receiver pkScripts), so wallet event
// channels can't be used.
func pollForVtxoAt(t *testing.T, ctx context.Context, idx indexer.Indexer, pkScript []byte, timeout time.Duration) indexerVtxo {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := idx.GetVtxos(ctx,
			indexer.WithScripts([]string{hex.EncodeToString(pkScript)}),
			indexer.WithSpendableOnly(),
		)
		if err == nil && len(resp.Vtxos) > 0 {
			v := resp.Vtxos[0]
			return indexerVtxo{Txid: v.Txid, VOut: v.VOut, Amount: v.Amount}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("no VTXO appeared at pkScript %s within %v", hex.EncodeToString(pkScript), timeout)
	return indexerVtxo{}
}
