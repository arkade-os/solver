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
	"sync"
	"testing"
	"time"

	introclient "github.com/ArkLabsHQ/introspector/pkg/client"
	"reflect"

	"github.com/arkade-os/arkd/pkg/ark-lib/asset"
	"github.com/arkade-os/arkd/pkg/ark-lib/extension"
	clientlib "github.com/arkade-os/arkd/pkg/client-lib"
	"github.com/arkade-os/arkd/pkg/client-lib/indexer"
	clientTypes "github.com/arkade-os/arkd/pkg/client-lib/types"
	arksdk "github.com/arkade-os/go-sdk"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/txscript"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	arkdURL          = "localhost:7170"
	arkdHTTPURL      = "http://localhost:7171"
	introspectorAddr = "localhost:7173"
)

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

func generateNoteCtx(_ context.Context, amount uint64) (string, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	reqBody := bytes.NewReader([]byte(fmt.Sprintf(`{"amount": "%d"}`, amount)))
	req, err := http.NewRequest("POST", arkdHTTPURL+"/v1/admin/note", reqBody)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Basic YWRtaW46YWRtaW4=")
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
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

func generateNote(t *testing.T, amount uint64) string {
	t.Helper()
	note, err := generateNoteCtx(t.Context(), amount)
	require.NoError(t, err)
	return note
}

func faucetOffchain(t *testing.T, client arksdk.ArkClient, amount float64) clientTypes.Vtxo {
	t.Helper()
	offchainAddr, err := client.NewOffchainAddress(t.Context())
	require.NoError(t, err)
	note := generateNote(t, uint64(amount*1e8))
	wg := &sync.WaitGroup{}
	wg.Add(1)
	var incomingFunds []clientTypes.Vtxo
	var incomingErr error
	go func() {
		incomingFunds, incomingErr = client.NotifyIncomingFunds(t.Context(), offchainAddr)
		wg.Done()
	}()
	txid, err := client.RedeemNotes(t.Context(), []string{note})
	require.NoError(t, err)
	require.NotEmpty(t, txid)
	wg.Wait()
	require.NoError(t, incomingErr)
	require.NotEmpty(t, incomingFunds)
	time.Sleep(time.Second)
	return incomingFunds[0]
}

func setupArkClient(t *testing.T) arksdk.ArkClient {
	t.Helper()
	arkClient, err := arksdk.NewArkClient("")
	require.NoError(t, err)
	privkey, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	err = arkClient.Init(t.Context(), arkdURL, hex.EncodeToString(privkey.Serialize()), "pass")
	require.NoError(t, err)
	err = arkClient.Unlock(t.Context(), "pass")
	require.NoError(t, err)
	syncCtx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	select {
	case ev, ok := <-arkClient.IsSynced(syncCtx):
		require.True(t, ok)
		require.NoError(t, ev.Err)
		require.True(t, ev.Synced)
	case <-syncCtx.Done():
		t.Fatal("timed out waiting for sync")
	}
	t.Cleanup(func() { arkClient.Stop() })
	return arkClient
}

// sendOffChainWithExtension funds an address with a receiver while attaching
// an arbitrary extension packet to the ark transaction's OP_RETURN. go-sdk's
// SendOffChain wrapper does not pass options through to the underlying
// client-lib, so we extract the embedded client.ArkClient and call it
// directly. Reflection is used because the wrapping struct is unexported.
func sendOffChainWithExtension(
	t *testing.T,
	c arksdk.ArkClient,
	receiver clientTypes.Receiver,
	pkt extension.Packet,
) {
	t.Helper()
	v := reflect.ValueOf(c)
	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		v = v.Elem()
	}
	field := v.FieldByName("ArkClient")
	require.True(t, field.IsValid(), "go-sdk arkClient.ArkClient field not found")
	inner, ok := field.Interface().(clientlib.ArkClient)
	require.True(t, ok, "embedded ArkClient does not implement clientlib.ArkClient")
	_, err := inner.SendOffChain(
		t.Context(),
		[]clientTypes.Receiver{receiver},
		clientlib.WithExtraPacket(pkt),
	)
	require.NoError(t, err)
}

func newIntroClient(t *testing.T) introclient.TransportClient {
	t.Helper()
	conn, err := grpc.NewClient(introspectorAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	return introclient.NewGRPCClient(conn)
}

func issueAsset(t *testing.T, client arksdk.ArkClient, supply uint64) string {
	t.Helper()
	// bancod's pair validation looks up "decimals" metadata via the indexer,
	// so every test asset must publish it (zero-decimal assets are fine).
	decimalsMd, err := asset.NewMetadata("decimals", "0")
	require.NoError(t, err)
	_, assetIds, err := client.IssueAsset(t.Context(), supply, nil, []asset.Metadata{*decimalsMd})
	require.NoError(t, err)
	require.Len(t, assetIds, 1)
	return assetIds[0].String()
}

func listVtxosWithAsset(t *testing.T, client arksdk.ArkClient, assetID string) []clientTypes.Vtxo {
	t.Helper()
	vtxos, _, err := client.ListVtxos(t.Context())
	require.NoError(t, err)
	var result []clientTypes.Vtxo
	for _, v := range vtxos {
		for _, a := range v.Assets {
			if a.AssetId == assetID {
				result = append(result, v)
				break
			}
		}
	}
	return result
}

// waitForCondition polls until fn returns true or timeout expires.
func waitForCondition(t *testing.T, timeout time.Duration, interval time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(interval)
	}
	t.Fatal("condition not met within timeout")
}

// fetchIntroPubkey returns the introspector signer pubkey, parsed.
func fetchIntroPubkey(t *testing.T, introClient introclient.TransportClient) *btcec.PublicKey {
	t.Helper()
	info, err := introClient.GetInfo(t.Context())
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
// Returns the first match or fails the test on timeout.
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
