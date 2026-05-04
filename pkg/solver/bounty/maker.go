package bounty

import (
	"context"
	"fmt"
	"reflect"
	"slices"

	clientlib "github.com/arkade-os/arkd/pkg/client-lib"
	clientTypes "github.com/arkade-os/arkd/pkg/client-lib/types"
	arksdk "github.com/arkade-os/go-sdk"
	"github.com/btcsuite/btcd/btcec/v2"
)

// CreateParams configure a new bounty funding tx built by Alice.
type CreateParams struct {
	Difficulty         uint8            // 1-32 leading zero bytes
	ReceiverPkScript   []byte           // 34-byte P2TR; receiver gets Amount-MinerFeeSats
	Amount             uint64           // sats locked into the bounty; must be > MinerFeeSats + DustLimitSats
	IntrospectorPubkey *btcec.PublicKey // x-only; caller fetches once via introClient.GetInfo
}

// CreateResult describes the funding artifact returned by CreateBounty.
type CreateResult struct {
	BountyAddress string
	FundingTxid   string
}

// CreateBounty funds a new bounty. Alice picks (difficulty, receiver, amount),
// the helper assembles the VTXO script, derives the bounty address, and submits
// an offchain tx that funds it while attaching the bounty announcement TLV via
// the SDK's WithExtraPacket option (so the tx surfaces as an ArkTx event in the
// transaction stream — bots subscribing to arkd will see it and react).
func CreateBounty(
	ctx context.Context,
	params CreateParams,
	arkClient arksdk.ArkClient,
) (*CreateResult, error) {
	if params.Difficulty == 0 || params.Difficulty > 32 {
		return nil, fmt.Errorf("difficulty out of range [1, 32]: %d", params.Difficulty)
	}
	if _, err := p2trWitnessProgram(params.ReceiverPkScript); err != nil {
		return nil, fmt.Errorf("invalid receiverPkScript: %w", err)
	}
	if params.Amount <= MinerFeeSats+DustLimitSats {
		return nil, fmt.Errorf(
			"amount %d must exceed miner fee %d + dust %d",
			params.Amount, MinerFeeSats, DustLimitSats,
		)
	}
	if params.IntrospectorPubkey == nil {
		return nil, fmt.Errorf("introspector pubkey must not be nil")
	}

	cfg, err := arkClient.GetConfigData(ctx)
	if err != nil {
		return nil, fmt.Errorf("get config data: %w", err)
	}
	bountyAddress, err := Address(
		params.Difficulty, params.ReceiverPkScript, cfg.SignerPubKey, params.IntrospectorPubkey, cfg.Network,
	)
	if err != nil {
		return nil, err
	}

	announce := &Announcement{
		Difficulty:       params.Difficulty,
		ReceiverPkScript: slices.Clone(params.ReceiverPkScript),
	}
	announcePkt, err := announce.ToExtensionPacket()
	if err != nil {
		return nil, fmt.Errorf("encode announcement: %w", err)
	}

	inner, err := embeddedClientLib(arkClient)
	if err != nil {
		return nil, err
	}
	res, err := inner.SendOffChain(ctx,
		[]clientTypes.Receiver{{To: bountyAddress, Amount: params.Amount}},
		clientlib.WithExtraPacket(announcePkt),
	)
	if err != nil {
		return nil, fmt.Errorf("send funding tx: %w", err)
	}
	if res == nil || res.Txid == "" {
		return nil, fmt.Errorf("funding tx submission returned no txid")
	}
	return &CreateResult{BountyAddress: bountyAddress, FundingTxid: res.Txid}, nil
}

// embeddedClientLib extracts the inner clientlib.ArkClient embedded inside the
// go-sdk wrapper. The wrapper's outer SendOffChain doesn't accept SendOption,
// so we have to reach for the embedded interface to attach an extension packet.
// Mirrors the pattern used in test/e2e/utils_test.go's sendOffChainWithExtension.
func embeddedClientLib(c arksdk.ArkClient) (clientlib.ArkClient, error) {
	v := reflect.ValueOf(c)
	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil, fmt.Errorf("ark client is nil")
		}
		v = v.Elem()
	}
	field := v.FieldByName("ArkClient")
	if !field.IsValid() {
		return nil, fmt.Errorf("go-sdk arkClient: embedded ArkClient field not found")
	}
	inner, ok := field.Interface().(clientlib.ArkClient)
	if !ok {
		return nil, fmt.Errorf("go-sdk arkClient: embedded value is not clientlib.ArkClient")
	}
	return inner, nil
}
