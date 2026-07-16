package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	emulatorclient "github.com/arkade-os/emulator/pkg/client"

	"github.com/arkade-os/arkd/pkg/ark-lib/asset"
	"github.com/arkade-os/arkd/pkg/ark-lib/extension"
	clientTypes "github.com/arkade-os/arkd/pkg/client-lib/types"
	arksdk "github.com/arkade-os/go-sdk"
	sdktypes "github.com/arkade-os/go-sdk/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	swapv1 "github.com/arkade-os/solver/api-spec/protobuf/gen/go/solverd/v1"
)

const (
	password     = "secret"
	arkdURL      = "localhost:7070"
	arkdHTTPURL  = "http://localhost:7071"
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
		arksdk.WithExtraPacket(pkt),
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

// dialWalletClient dials the dockerized solver's WalletService.
func dialWalletClient(t *testing.T) swapv1.WalletServiceClient {
	t.Helper()
	conn, err := grpc.NewClient(e2eGRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return swapv1.NewWalletServiceClient(conn)
}

// solverOffchainAddress fetches the solver's offchain address over the
// WalletService RPC (context variant for use in TestMain setup).
func solverOffchainAddress(ctx context.Context) (string, error) {
	conn, err := grpc.NewClient(e2eGRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return "", err
	}
	// nolint:errcheck
	defer conn.Close()
	resp, err := swapv1.NewWalletServiceClient(conn).GetAddress(ctx, &swapv1.GetAddressRequest{})
	if err != nil {
		return "", err
	}
	if resp.GetOffchainAddress() == "" {
		return "", fmt.Errorf("empty offchain address")
	}
	return resp.GetOffchainAddress(), nil
}

// pollSolverOffchain polls the solver's WalletService balance until offchain
// settled >= target or the timeout expires.
func pollSolverOffchain(ctx context.Context, target uint64, timeout time.Duration) error {
	conn, err := grpc.NewClient(e2eGRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	// nolint:errcheck
	defer conn.Close()
	client := swapv1.NewWalletServiceClient(conn)

	deadline := time.Now().Add(timeout)
	for {
		resp, err := client.GetBalance(ctx, &swapv1.GetBalanceRequest{})
		if err == nil && resp.GetOffchainSettled() >= target {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for solver offchain balance >= %d", target)
		}
		time.Sleep(time.Second)
	}
}

// pollSolverAssetBalance polls until the solver's asset_balances shows
// assetID >= amount, or fails the test on timeout.
func pollSolverAssetBalance(t *testing.T, ctx context.Context, assetID string, amount uint64, timeout time.Duration) {
	t.Helper()
	client := dialWalletClient(t)
	deadline := time.Now().Add(timeout)
	for {
		resp, err := client.GetBalance(ctx, &swapv1.GetBalanceRequest{})
		if err == nil {
			if got := resp.GetAssetBalances()[assetID]; got >= amount {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for solver asset %s balance >= %d", assetID, amount)
		}
		time.Sleep(time.Second)
	}
}

// fundSolverAsset sends `amount` units of assetID (with a matching BTC carrier)
// from client to the taker bot, then waits for the solver to report the balance.
func fundSolverAsset(t *testing.T, ctx context.Context, client arksdk.Wallet, assetID string, amount uint64) {
	t.Helper()
	solverAddr := getAddress(t)
	_, err := client.SendOffChain(ctx, []clientTypes.Receiver{{
		To:     solverAddr.OffchainAddress,
		Amount: amount,
		Assets: []clientTypes.Asset{{AssetId: assetID, Amount: amount}},
	}})
	require.NoError(t, err)
	pollSolverAssetBalance(t, ctx, assetID, amount, 30*time.Second)
}

// newFunderWallet builds, inits, and unlocks a throwaway go-sdk wallet on a
// temp datadir for funding the solver. Returns a cleanup that stops the wallet
// and removes the datadir.
func newFunderWallet(ctx context.Context) (arksdk.Wallet, func(), error) {
	dir, err := os.MkdirTemp("", "solverd-e2e-funder-*")
	if err != nil {
		return nil, nil, err
	}
	w, err := arksdk.NewWallet(dir, arksdk.WithoutAutoSettle())
	if err != nil {
		// nolint:errcheck
		os.RemoveAll(dir)
		return nil, nil, err
	}
	if err := w.Init(ctx, arkdURL, "", password); err != nil {
		// nolint:errcheck
		os.RemoveAll(dir)
		return nil, nil, err
	}
	if err := w.Unlock(ctx, password); err != nil {
		// nolint:errcheck
		os.RemoveAll(dir)
		return nil, nil, err
	}
	synced := <-w.IsSynced(ctx)
	if synced.Err != nil {
		// nolint:errcheck
		os.RemoveAll(dir)
		return nil, nil, fmt.Errorf("funder sync: %w", synced.Err)
	}
	cleanup := func() {
		w.Stop()
		// nolint:errcheck
		os.RemoveAll(dir)
	}
	return w, cleanup, nil
}

// faucetOffchainCtx redeems an admin note into the wallet and waits for the
// resulting VTXO (context variant of faucetOffchain).
func faucetOffchainCtx(ctx context.Context, w arksdk.Wallet, amount uint64) error {
	note, err := generateNoteCtx(ctx, amount)
	if err != nil {
		return err
	}
	vtxoCh := w.GetVtxoEventChannel(ctx)
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-waitCtx.Done():
				return
			case ev, ok := <-vtxoCh:
				if !ok {
					return
				}
				if ev.Type == sdktypes.VtxosAdded && len(ev.Vtxos) > 0 {
					return
				}
			}
		}
	}()

	if _, err := w.RedeemNotes(ctx, []string{note}); err != nil {
		return err
	}
	select {
	case <-done:
		return nil
	case <-waitCtx.Done():
		return fmt.Errorf("faucetOffchainCtx: timed out waiting for VtxosAdded")
	}
}

// fundSolver tops up the dockerized solver's offchain BTC balance by sending
// from a throwaway funder wallet to the solver's offchain address (fetched over
// the WalletService RPC), then polls the solver's balance until it lands.
func fundSolver(ctx context.Context) error {
	funder, cleanup, err := newFunderWallet(ctx)
	if err != nil {
		return fmt.Errorf("create funder wallet: %w", err)
	}
	defer cleanup()

	if err := faucetOffchainCtx(ctx, funder, 200000); err != nil {
		return fmt.Errorf("fund funder wallet: %w", err)
	}

	addr, err := solverOffchainAddress(ctx)
	if err != nil {
		return fmt.Errorf("get solver address: %w", err)
	}

	if _, err := funder.SendOffChain(ctx, []clientTypes.Receiver{{To: addr, Amount: 150000}}); err != nil {
		return fmt.Errorf("send to solver: %w", err)
	}

	return pollSolverOffchain(ctx, 100000, 60*time.Second)
}
