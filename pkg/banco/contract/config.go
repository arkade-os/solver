package contract

import (
	"context"
	"encoding/hex"
	"fmt"

	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/arkade-os/arkd/pkg/client-lib/client"
	"github.com/btcsuite/btcd/btcec/v2"
)

// serverConfig holds the subset of the server info used to build banco
// contracts, decoded into the rich types the contract logic expects.
type serverConfig struct {
	Network             arklib.Network
	SignerPubKey        *btcec.PublicKey
	UnilateralExitDelay arklib.RelativeLocktime
	CheckpointTapscript string
}

// fetchServerConfig retrieves the server info via the transport client and
// converts it into a serverConfig. Since go-sdk v0.10.0 no longer exposes
// GetConfigData on the Wallet, config data is derived from Client().GetInfo().
func fetchServerConfig(ctx context.Context, cli client.Client) (*serverConfig, error) {
	info, err := cli.GetInfo(ctx)
	if err != nil {
		return nil, err
	}

	buf, err := hex.DecodeString(info.SignerPubKey)
	if err != nil {
		return nil, fmt.Errorf("failed to decode signer pubkey: invalid format")
	}
	signerKey, err := btcec.ParsePubKey(buf)
	if err != nil {
		return nil, fmt.Errorf("failed to parse signer pubkey: %w", err)
	}

	exitDelay := arklib.RelativeLocktime{
		Type:  arklib.LocktimeTypeSecond,
		Value: uint32(info.UnilateralExitDelay),
	}
	if info.UnilateralExitDelay < 512 {
		exitDelay.Type = arklib.LocktimeTypeBlock
	}

	return &serverConfig{
		Network:             networkFromString(info.Network),
		SignerPubKey:        signerKey,
		UnilateralExitDelay: exitDelay,
		CheckpointTapscript: info.CheckpointTapscript,
	}, nil
}

func networkFromString(net string) arklib.Network {
	switch net {
	case arklib.Bitcoin.Name:
		return arklib.Bitcoin
	case arklib.BitcoinTestNet.Name:
		return arklib.BitcoinTestNet
	case arklib.BitcoinSigNet.Name:
		return arklib.BitcoinSigNet
	case arklib.BitcoinMutinyNet.Name:
		return arklib.BitcoinMutinyNet
	case arklib.BitcoinRegTest.Name:
		return arklib.BitcoinRegTest
	default:
		return arklib.Bitcoin
	}
}
