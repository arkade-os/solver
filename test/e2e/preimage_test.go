package e2e_test

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/arkade-os/arkd/pkg/client-lib/indexer"
	clientTypes "github.com/arkade-os/arkd/pkg/client-lib/types"
	arksdk "github.com/arkade-os/go-sdk"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	bancov1 "github.com/arkade-os/bancod/api-spec/protobuf/gen/go/bancod/v1"
	"github.com/arkade-os/bancod/pkg/preimage"
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

	res, err := preimage.CreateClaim(preimage.CreateClaimParams{
		Preimage:     preimg,
		ReceiverPk:   receiverPk,
		SolverPubKey: solverPub,
		ServerPubKey: cfgData.SignerPubKey,
		IntroPubKey:  introPub,
		Network:      cfgData.Network,
	})
	require.NoError(t, err)

	const amount uint64 = 10_000

	wg := &sync.WaitGroup{}
	wg.Add(1)
	var incomingErr error
	go func() {
		defer wg.Done()
		_, incomingErr = maker.NotifyIncomingFunds(ctx, res.ClaimAddress)
	}()

	sendOffChainWithExtension(t, maker, clientTypes.Receiver{
		To:     res.ClaimAddress,
		Amount: amount,
	}, res.Packet)
	wg.Wait()
	require.NoError(t, incomingErr)

	v := pollForVtxoAt(t, ctx, maker.Indexer(), receiverPk, 30*time.Second)
	require.Equal(t, amount, v.Amount, "receiver should be paid the full input value")
	t.Log("preimage claim: receiver paid")
}

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

	res, err := preimage.CreateClaim(preimage.CreateClaimParams{
		Preimage:     preimg,
		ReceiverPk:   receiverPk,
		SolverPubKey: solverPub,
		ServerPubKey: cfgData.SignerPubKey,
		IntroPubKey:  introPub,
		Network:      cfgData.Network,
	})
	require.NoError(t, err)

	body, err := res.Packet.Serialize()
	require.NoError(t, err)
	pkt, err := preimage.DeserializeClaim(body)
	require.NoError(t, err)
	badScript := []byte{0xde, 0xad, 0xbe, 0xef}
	plaintext := append([]byte{}, preimg...)
	plaintext = appendVarintLen(plaintext, len(badScript))
	plaintext = append(plaintext, badScript...)
	tamperedCt, err := preimage.Encrypt(solverPub, plaintext)
	require.NoError(t, err)
	pkt.Ciphertext = tamperedCt
	tamperedPacket, err := pkt.ToPacket()
	require.NoError(t, err)

	const amount uint64 = 10_000

	wg := &sync.WaitGroup{}
	wg.Add(1)
	var incomingErr error
	go func() {
		defer wg.Done()
		_, incomingErr = maker.NotifyIncomingFunds(ctx, res.ClaimAddress)
	}()
	sendOffChainWithExtension(t, maker, clientTypes.Receiver{
		To:     res.ClaimAddress,
		Amount: amount,
	}, tamperedPacket)
	wg.Wait()
	require.NoError(t, incomingErr)

	time.Sleep(15 * time.Second)
	require.Empty(t, vtxosForScript(t, ctx, maker, receiverPk),
		"tampered arkade script must not result in a claim")
}

func freshPreimage(t *testing.T) []byte {
	t.Helper()
	priv, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	out := priv.Serialize()
	require.Len(t, out, 32)
	return out
}

func appendVarintLen(buf []byte, n int) []byte {
	tmp := make([]byte, binary.MaxVarintLen64)
	written := binary.PutUvarint(tmp, uint64(n))
	return append(buf, tmp[:written]...)
}

func vtxosForScript(t *testing.T, ctx context.Context, c arksdk.ArkClient, pk []byte) []clientTypes.Vtxo {
	t.Helper()
	resp, err := c.Indexer().GetVtxos(ctx,
		indexer.WithScripts([]string{hex.EncodeToString(pk)}),
		indexer.WithSpendableOnly(),
	)
	require.NoError(t, err)
	return resp.Vtxos
}
