package e2e_test

import (
	"context"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/ArkLabsHQ/introspector/pkg/arkade"
	"github.com/arkade-os/arkd/pkg/client-lib/indexer"
	clientTypes "github.com/arkade-os/arkd/pkg/client-lib/types"
	arksdk "github.com/arkade-os/go-sdk"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/txscript"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	bancov1 "github.com/arkade-os/bancod/api-spec/protobuf/gen/go/bancod/v1"
	"github.com/arkade-os/bancod/pkg/preimage"
)

// dialPreimageClient opens a gRPC connection to the test bancod server and
// returns a typed PreimageService client. The connection is closed during
// test cleanup.
func dialPreimageClient(t *testing.T) bancov1.PreimageServiceClient {
	t.Helper()
	conn, err := grpc.NewClient(e2eGRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return bancov1.NewPreimageServiceClient(conn)
}

// preimageArtifacts holds the inputs for a RegisterClaim call plus the
// pre-computed claim address and receiver pkScript. Tests build this client-
// side and submit it through the gRPC API.
type preimageArtifacts struct {
	preimage     []byte
	arkadeScript []byte
	taptree      []string
	claimAddress string
	receiverPk   []byte
}

// buildPreimageArtifacts assembles a fresh HTLC client-side: random preimage,
// fresh receiver pkScript, taptree built via the same primitives the bot uses
// (since they live in `pkg/preimage`, the maker can use them directly).
func buildPreimageArtifacts(
	t *testing.T,
	ctx context.Context,
	maker arksdk.ArkClient,
	introPub *btcec.PublicKey,
) preimageArtifacts {
	t.Helper()

	receiverPk := freshTaprootPkScript(t)

	// 32-byte preimage from a fresh random source so each run is unique.
	priv, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	preimageBytes := priv.Serialize()
	require.Len(t, preimageBytes, 32)
	preimageHash := btcutil.Hash160(preimageBytes)

	cfgData, err := maker.GetConfigData(ctx)
	require.NoError(t, err)

	vtxoScript, err := preimage.VtxoScript(preimageHash, receiverPk, cfgData.SignerPubKey, introPub)
	require.NoError(t, err)
	taptree, err := vtxoScript.Encode()
	require.NoError(t, err)

	arkadeScript := mustEnforcePayTo(t, receiverPk)

	addr, err := preimage.Address(preimageHash, receiverPk, cfgData.SignerPubKey, introPub, cfgData.Network)
	require.NoError(t, err)

	return preimageArtifacts{
		preimage:     preimageBytes,
		arkadeScript: arkadeScript,
		taptree:      taptree,
		claimAddress: addr,
		receiverPk:   receiverPk,
	}
}

// mustEnforcePayTo rebuilds the arkade enforcement script with the same
// opcode sequence as pkg/preimage's unexported enforcePayTo. Used by tests
// because the helper isn't part of the public surface.
func mustEnforcePayTo(t *testing.T, receiverPkScript []byte) []byte {
	t.Helper()
	require.Len(t, receiverPkScript, 34)
	witnessProgram := receiverPkScript[2:]

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
	out, err := b.Script()
	require.NoError(t, err)
	return out
}

// TestPreimage_RegisterAndClaim — happy path. A maker dials the bot's gRPC
// API as a real client would, registers a claim, funds the returned address;
// the bot's solver loop detects the funding tx, builds + submits the claim
// via the introspector, and the receiver pkScript ends up holding a
// spendable VTXO of the full input value.
//
// Requires `make setup-test-env`.
func TestPreimage_RegisterAndClaim(t *testing.T) {
	if testing.Short() {
		t.Skip("requires live nigiri+arkd+introspector stack")
	}

	ctx := t.Context()

	intro := newIntroClient(t)
	introPub := fetchIntroPubkey(t, intro)

	maker := setupArkClient(t)
	faucetOffchain(t, maker, 0.001)

	bot := dialPreimageClient(t)
	artifacts := buildPreimageArtifacts(t, ctx, maker, introPub)

	resp, err := bot.RegisterClaim(ctx, &bancov1.RegisterClaimRequest{
		Preimage:     artifacts.preimage,
		ArkadeScript: artifacts.arkadeScript,
		Taptree:      artifacts.taptree,
	})
	require.NoError(t, err)
	require.Equal(t, artifacts.claimAddress, resp.ClaimAddress)

	// Fund the claim address. The bot's solver loop will see the funding tx,
	// match its output to the registered pkScript, and submit a claim that
	// pays the receiver.
	const amount uint64 = 10_000

	wg := &sync.WaitGroup{}
	wg.Add(1)
	var incomingErr error
	go func() {
		defer wg.Done()
		_, incomingErr = maker.NotifyIncomingFunds(ctx, resp.ClaimAddress)
	}()

	_, err = maker.SendOffChain(ctx, []clientTypes.Receiver{{
		To:     resp.ClaimAddress,
		Amount: amount,
	}})
	require.NoError(t, err)
	wg.Wait()
	require.NoError(t, incomingErr)

	// Wait for the bot to claim — the receiver pkScript should hold a VTXO
	// of the full input value.
	v := pollForVtxoAt(t, ctx, maker.Indexer(), artifacts.receiverPk, 30*time.Second)
	require.Equal(t, amount, v.Amount, "receiver should be paid the full input value")
	t.Log("preimage claim: receiver paid")
}

// TestPreimage_DeleteClaim_PreventsClaim — register, delete, then fund. The
// bot must NOT claim the funding tx because the registration was canceled.
//
// Requires `make setup-test-env`.
func TestPreimage_DeleteClaim_PreventsClaim(t *testing.T) {
	if testing.Short() {
		t.Skip("requires live nigiri+arkd+introspector stack")
	}

	ctx := t.Context()

	intro := newIntroClient(t)
	introPub := fetchIntroPubkey(t, intro)

	maker := setupArkClient(t)
	faucetOffchain(t, maker, 0.001)

	bot := dialPreimageClient(t)
	artifacts := buildPreimageArtifacts(t, ctx, maker, introPub)

	resp, err := bot.RegisterClaim(ctx, &bancov1.RegisterClaimRequest{
		Preimage:     artifacts.preimage,
		ArkadeScript: artifacts.arkadeScript,
		Taptree:      artifacts.taptree,
	})
	require.NoError(t, err)

	// Cancel the registration before funding.
	_, err = bot.DeleteClaim(ctx, &bancov1.DeleteClaimRequest{ClaimAddress: resp.ClaimAddress})
	require.NoError(t, err)

	const amount uint64 = 10_000
	wg := &sync.WaitGroup{}
	wg.Add(1)
	var incomingErr error
	go func() {
		defer wg.Done()
		_, incomingErr = maker.NotifyIncomingFunds(ctx, resp.ClaimAddress)
	}()
	_, err = maker.SendOffChain(ctx, []clientTypes.Receiver{{
		To:     resp.ClaimAddress,
		Amount: amount,
	}})
	require.NoError(t, err)
	wg.Wait()
	require.NoError(t, incomingErr)

	// Wait long enough that a misbehaving bot would have claimed.
	time.Sleep(15 * time.Second)

	indexerResp, err := maker.Indexer().GetVtxos(ctx,
		indexer.WithScripts([]string{hex.EncodeToString(artifacts.receiverPk)}),
		indexer.WithSpendableOnly(),
	)
	require.NoError(t, err)
	require.Empty(t, indexerResp.Vtxos, "deleted registration must not produce a claim")
}

// TestPreimage_ListClaims — register two distinct claims via the gRPC API,
// list and verify both addresses surface; secret material must NOT appear in
// ListClaims.
//
// Requires `make setup-test-env`.
func TestPreimage_ListClaims(t *testing.T) {
	if testing.Short() {
		t.Skip("requires live nigiri+arkd+introspector stack")
	}

	ctx := t.Context()

	intro := newIntroClient(t)
	introPub := fetchIntroPubkey(t, intro)

	maker := setupArkClient(t)
	faucetOffchain(t, maker, 0.001)

	bot := dialPreimageClient(t)

	a1 := buildPreimageArtifacts(t, ctx, maker, introPub)
	a2 := buildPreimageArtifacts(t, ctx, maker, introPub)

	resp1, err := bot.RegisterClaim(ctx, &bancov1.RegisterClaimRequest{
		Preimage: a1.preimage, ArkadeScript: a1.arkadeScript, Taptree: a1.taptree,
	})
	require.NoError(t, err)
	resp2, err := bot.RegisterClaim(ctx, &bancov1.RegisterClaimRequest{
		Preimage: a2.preimage, ArkadeScript: a2.arkadeScript, Taptree: a2.taptree,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = bot.DeleteClaim(context.Background(), &bancov1.DeleteClaimRequest{ClaimAddress: resp1.ClaimAddress})
		_, _ = bot.DeleteClaim(context.Background(), &bancov1.DeleteClaimRequest{ClaimAddress: resp2.ClaimAddress})
	})

	listResp, err := bot.ListClaims(ctx, &bancov1.ListClaimsRequest{})
	require.NoError(t, err)

	addrs := make(map[string]bool)
	for _, ci := range listResp.Claims {
		addrs[ci.ClaimAddress] = true
	}
	require.True(t, addrs[resp1.ClaimAddress], "addr1 missing from list")
	require.True(t, addrs[resp2.ClaimAddress], "addr2 missing from list")
}

// TestPreimage_RejectsBadArkadeScript — an arkade_script that doesn't match
// `enforcePayTo(receiver)` exactly must be rejected at registration time.
// The gRPC layer maps the validation error to InvalidArgument.
func TestPreimage_RejectsBadArkadeScript(t *testing.T) {
	if testing.Short() {
		t.Skip("requires live nigiri+arkd+introspector stack")
	}

	ctx := t.Context()

	intro := newIntroClient(t)
	introPub := fetchIntroPubkey(t, intro)

	maker := setupArkClient(t)
	faucetOffchain(t, maker, 0.001)

	bot := dialPreimageClient(t)
	artifacts := buildPreimageArtifacts(t, ctx, maker, introPub)

	// Append a stray opcode so the bytes-equal check fails.
	bad := append([]byte{}, artifacts.arkadeScript...)
	bad = append(bad, txscript.OP_NOP)

	_, err := bot.RegisterClaim(ctx, &bancov1.RegisterClaimRequest{
		Preimage:     artifacts.preimage,
		ArkadeScript: bad,
		Taptree:      artifacts.taptree,
	})
	require.Error(t, err, "v1 must reject arkade_scripts that don't match enforcePayTo exactly")
	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.InvalidArgument, st.Code())
}
