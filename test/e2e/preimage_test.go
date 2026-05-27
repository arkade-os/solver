package e2e_test

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/arkade-os/arkd/pkg/ark-lib/extension"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/arkade-os/arkd/pkg/ark-lib/txutils"
	"github.com/arkade-os/arkd/pkg/client-lib/indexer"
	clientTypes "github.com/arkade-os/arkd/pkg/client-lib/types"
	arksdk "github.com/arkade-os/go-sdk"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	bancov1 "github.com/arkade-os/solver/api-spec/protobuf/gen/go/bancod/v1"
	"github.com/arkade-os/solver/pkg/preimage"
)

func dialPreimageClient(t *testing.T) bancov1.PreimageServiceClient {
	t.Helper()
	conn, err := grpc.NewClient(e2eGRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return bancov1.NewPreimageServiceClient(conn)
}

func fetchSolverPubKey(t *testing.T, client bancov1.PreimageServiceClient) *btcec.PublicKey {
	t.Helper()
	resp, err := client.GetSolverPubKey(t.Context(), &bancov1.GetSolverPubKeyRequest{})
	require.NoError(t, err)
	raw, err := hex.DecodeString(resp.SolverPubKey)
	require.NoError(t, err)
	pub, err := btcec.ParsePubKey(raw)
	require.NoError(t, err)
	return pub
}

// TestPreimageFundAndClaim: maker funds a VHTLC at claimAddr with a valid
// preimage packet. The preimage bot must observe the funding tx, claim by
// revealing the preimage, and pay receiverPk.
func TestPreimageFundAndClaim(t *testing.T) {
	ctx := t.Context()

	intro := newIntroClient(t)
	introPub := fetchIntroPubkey(t, intro)

	maker := setupArkClient(t)
	faucetOffchain(t, maker, 0.001)

	bot := dialPreimageClient(t)
	solverPub := fetchSolverPubKey(t, bot)

	cfgData, err := maker.GetConfigData(ctx)
	require.NoError(t, err)

	receiverPk := freshTaprootPkScript(t)
	preimg := freshPreimage(t)

	claimAddr, claimPacket, encodedTapTree := buildPreimageVTXO(
		t, preimg, receiverPk, solverPub,
		cfgData.SignerPubKey, introPub, cfgData.Network,
	)

	const amount uint64 = 10_000

	// sendOffChainToVHTLC builds + finalizes the funding tx synchronously
	// via Client().SubmitTx + FinalizeTx, so after it returns the swap
	// VTXO is observable on arkd. No need to subscribe to maker events:
	// the maker is the SENDER, not the receiver.
	sendOffChainToVHTLC(t, maker, claimAddr, amount, encodedTapTree, claimPacket)

	v := pollForVtxoAt(t, ctx, maker.Indexer(), receiverPk, 30*time.Second)
	require.Equal(t, amount, v.Amount, "receiver should be paid the full input value")
}

// TestPreimageInvalidArkadeScript: same setup as above but the arkade_script
// in the packet is tampered. The bot must NOT claim — the receiver pkScript
// must remain empty after a grace period.
func TestPreimageInvalidArkadeScript(t *testing.T) {
	ctx := t.Context()

	intro := newIntroClient(t)
	introPub := fetchIntroPubkey(t, intro)

	maker := setupArkClient(t)
	faucetOffchain(t, maker, 0.001)

	bot := dialPreimageClient(t)
	solverPub := fetchSolverPubKey(t, bot)

	cfgData, err := maker.GetConfigData(ctx)
	require.NoError(t, err)

	receiverPk := freshTaprootPkScript(t)
	preimg := freshPreimage(t)

	claimAddr, claimPacket, encodedTapTree := buildPreimageVTXO(
		t, preimg, receiverPk, solverPub,
		cfgData.SignerPubKey, introPub, cfgData.Network,
	)

	// The new packet shape carries the arkade_script in plaintext, so
	// "invalid arkade script" testing is a plaintext swap — no
	// re-encryption needed.
	body, err := claimPacket.Serialize()
	require.NoError(t, err)
	pkt, err := preimage.DeserializeClaim(body)
	require.NoError(t, err)
	pkt.ArkadeScript = []byte{0xde, 0xad, 0xbe, 0xef}
	tamperedPacket, err := pkt.ToPacket()
	require.NoError(t, err)

	const amount uint64 = 10_000

	sendOffChainToVHTLC(t, maker, claimAddr, amount, encodedTapTree, tamperedPacket)

	// Grace period: give the bot enough time to attempt + fail a claim.
	time.Sleep(15 * time.Second)
	require.Empty(t, vtxosForScript(t, ctx, maker, receiverPk),
		"tampered arkade script must not result in a claim")
}

func buildPreimageVTXO(
	t *testing.T,
	preimg []byte,
	receiverPk []byte,
	solverPub, serverPub, introPub *btcec.PublicKey,
	network arklib.Network,
) (string, extension.Packet, []byte) {
	t.Helper()
	closure, err := preimage.CovenantClaimClosure(
		btcutil.Hash160(preimg), receiverPk, serverPub, introPub,
	)
	require.NoError(t, err)
	pkt, err := preimage.BuildPacket(preimg, solverPub, receiverPk)
	require.NoError(t, err)
	vs := &script.TapscriptsVtxoScript{Closures: []script.Closure{closure}}
	tapscripts, err := vs.Encode()
	require.NoError(t, err)
	tapKey, _, err := vs.TapTree()
	require.NoError(t, err)
	encodedTapTree, err := txutils.TapTree(tapscripts).Encode()
	require.NoError(t, err)
	addr, err := (&arklib.Address{
		HRP:        network.Addr,
		Signer:     serverPub,
		VtxoTapKey: tapKey,
	}).EncodeV0()
	require.NoError(t, err)
	return addr, pkt, encodedTapTree
}

func freshPreimage(t *testing.T) []byte {
	t.Helper()
	priv, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	out := priv.Serialize()
	require.Len(t, out, 32)
	return out
}

func vtxosForScript(t *testing.T, ctx context.Context, c arksdk.Wallet, pk []byte) []clientTypes.Vtxo {
	t.Helper()
	resp, err := c.Indexer().GetVtxos(ctx,
		indexer.WithScripts([]string{hex.EncodeToString(pk)}),
		indexer.WithSpendableOnly(),
	)
	require.NoError(t, err)
	return resp.Vtxos
}
