package contract

import (
	"context"
	"encoding/hex"
	"fmt"

	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/arkade-os/arkd/pkg/ark-lib/asset"
	"github.com/arkade-os/arkd/pkg/ark-lib/extension"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/arkade-os/arkd/pkg/client-lib/indexer"
	emulatorclient "github.com/arkade-os/emulator/pkg/client"
	arksdk "github.com/arkade-os/go-sdk"
	"github.com/arkade-os/go-sdk/types"
	"github.com/btcsuite/btcd/btcec/v2"
)

// CreateOfferParams holds parameters for creating a swap offer.
type CreateOfferParams struct {
	WantAmount uint64         // sats the maker wants to receive
	WantAsset  *asset.AssetId // nil for BTC
}

// CreateOfferResult contains the result of creating an offer.
type CreateOfferResult struct {
	OfferHex    string
	Packet      extension.Packet
	SwapAddress string
	// MakerPkScript is where the swap fullfilment should go to
	MakerPkScript []byte
}

// OfferStatus represents the status of a VTXO at a swap address.
type OfferStatus struct {
	Txid      string
	VOut      uint32
	Value     uint64
	Spendable bool
}

// CreateOffer creates a new swap offer.
func CreateOffer(
	ctx context.Context,
	params CreateOfferParams,
	arkClient arksdk.Wallet,
	emulatorClient emulatorclient.TransportClient,
) (*CreateOfferResult, error) {
	// Get emulator pubkey
	emulatorInfo, err := emulatorClient.GetInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get emulator info: %w", err)
	}

	emulatorPubKeyBytes, err := hex.DecodeString(emulatorInfo.SignerPublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to decode emulator pubkey: %w", err)
	}
	emulatorPubkey, err := btcec.ParsePubKey(emulatorPubKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse emulator pubkey: %w", err)
	}

	// Derive the maker pkScript and public key from a single default contract,
	// so both reference the same key (HD wallets derive a fresh key per call).
	cm := arkClient.ContractManager()
	makerContract, err := cm.NewContract(ctx, types.ContractTypeDefault)
	if err != nil {
		return nil, fmt.Errorf("failed to create maker contract: %w", err)
	}
	decodedAddr, err := arklib.DecodeAddressV0(makerContract.Address)
	if err != nil {
		return nil, fmt.Errorf("failed to decode maker address: %w", err)
	}
	makerPkScript, err := script.P2TRScript(decodedAddr.VtxoTapKey)
	if err != nil {
		return nil, fmt.Errorf("failed to build maker pkscript: %w", err)
	}
	handler, err := cm.GetHandler(ctx, *makerContract)
	if err != nil {
		return nil, fmt.Errorf("failed to get maker contract handler: %w", err)
	}
	makerKeyRef, err := handler.GetKeyRef(*makerContract)
	if err != nil {
		return nil, fmt.Errorf("failed to get maker public key: %w", err)
	}

	cfg, err := fetchServerConfig(ctx, arkClient.Client())
	if err != nil {
		return nil, err
	}

	// Always include the cancel (mandatory, via MakerPublicKey) and exit paths,
	// matching the client so both derive the same swap address. The exit delay
	// is the server's unilateral exit delay.
	exitDelay := cfg.UnilateralExitDelay
	offer := &Offer{
		WantAmount:     params.WantAmount,
		WantAsset:      params.WantAsset,
		MakerPkScript:  makerPkScript,
		MakerPublicKey: makerKeyRef.PubKey,
		EmulatorPubkey: emulatorPubkey,
		ExitDelay:      &exitDelay,
	}

	// Compute swap address
	vtxoScript, err := offer.VtxoScript(cfg.SignerPubKey)
	if err != nil {
		return nil, fmt.Errorf("failed to build vtxo script: %w", err)
	}
	taprootKey, _, err := vtxoScript.TapTree()
	if err != nil {
		return nil, fmt.Errorf("failed to build taptree: %w", err)
	}

	swapAddr := &arklib.Address{
		HRP:        cfg.Network.Addr,
		Signer:     cfg.SignerPubKey,
		VtxoTapKey: taprootKey,
	}
	swapAddress, err := swapAddr.EncodeV0()
	if err != nil {
		return nil, fmt.Errorf("failed to encode swap address: %w", err)
	}

	swapPkScript, err := swapAddr.GetPkScript()
	if err != nil {
		return nil, err
	}

	offer.SwapPkScript = swapPkScript

	encodedOffer, err := offer.Serialize()
	if err != nil {
		return nil, fmt.Errorf("failed to encode offer: %w", err)
	}

	packet, err := offer.ToPacket()
	if err != nil {
		return nil, fmt.Errorf("failed to create packet: %w", err)
	}

	return &CreateOfferResult{
		OfferHex:      hex.EncodeToString(encodedOffer),
		Packet:        packet,
		SwapAddress:   swapAddress,
		MakerPkScript: makerPkScript,
	}, nil
}

// GetOffers queries VTXOs at a swap address to check offer status.
func GetOffers(
	ctx context.Context,
	swapAddress string,
	indexerClient indexer.Indexer,
) ([]OfferStatus, error) {
	decoded, err := arklib.DecodeAddressV0(swapAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to decode swap address: %w", err)
	}
	pkScript, err := decoded.GetPkScript()
	if err != nil {
		return nil, fmt.Errorf("failed to get swap address pkscript: %w", err)
	}

	vtxosResp, err := indexerClient.GetVtxos(ctx,
		indexer.WithScripts([]string{hex.EncodeToString(pkScript)}),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get vtxos: %w", err)
	}

	statuses := make([]OfferStatus, 0, len(vtxosResp.Vtxos))
	for _, v := range vtxosResp.Vtxos {
		statuses = append(statuses, OfferStatus{
			Txid:      v.Txid,
			VOut:      v.VOut,
			Value:     v.Amount,
			Spendable: !v.Spent,
		})
	}
	return statuses, nil
}
