package application

import (
	"context"
	"crypto/subtle"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/arkade-os/arkd/pkg/ark-lib/asset"
	clientTypes "github.com/arkade-os/arkd/pkg/client-lib/types"
)

var ErrInvalidPassword = fmt.Errorf("invalid wallet password")

type Balance struct {
	OnchainSpendable uint64
	OnchainLocked    uint64
	OffchainTotal    uint64
	// AssetBalances maps asset id -> amount (asset's smallest unit) held offchain.
	AssetBalances map[string]uint64
}

type Address struct {
	OffchainAddress string
	BoardingAddress string
}

type AssetInfo struct {
	AssetID  string
	Ticker   string
	Name     string
	IconURL  string
	Decimals uint32
	Balance  uint64
}

func (svc *Service) ListAssets(ctx context.Context) ([]AssetInfo, error) {
	bal, err := svc.GetBalance(ctx)
	if err != nil {
		return nil, err
	}

	assets := make([]AssetInfo, 0, len(bal.AssetBalances))
	for id, amount := range bal.AssetBalances {
		info := AssetInfo{AssetID: id, Balance: amount}
		if svc.indexer != nil {
			if meta, err := svc.indexer.GetAsset(ctx, id); err == nil && meta != nil {
				applyAssetMetadata(&info, meta.Metadata)
			} else if err != nil {
				svc.log.WithError(err).Warnf("failed to resolve metadata for asset %s", id)
			}
		}
		assets = append(assets, info)
	}

	sort.Slice(assets, func(i, j int) bool {
		ki, kj := assets[i].Ticker, assets[j].Ticker
		if ki == "" {
			ki = assets[i].AssetID
		}
		if kj == "" {
			kj = assets[j].AssetID
		}
		return ki < kj
	})
	return assets, nil
}

func (svc *Service) SendOffchain(
	ctx context.Context, password, address, assetID string, amount uint64,
) (string, error) {
	if err := svc.verifyPassword(password); err != nil {
		return "", err
	}

	receiver := clientTypes.Receiver{To: address}
	if isBTC(assetID) {
		receiver.Amount = amount
	} else {
		// The SDK fills the BTC carrier to the dust minimum for asset receivers.
		receiver.Assets = []clientTypes.Asset{{AssetId: assetID, Amount: amount}}
	}

	txid, err := svc.arkClient.SendOffChain(ctx, []clientTypes.Receiver{receiver})
	if err != nil {
		return "", fmt.Errorf("failed to send: %w", err)
	}
	return txid, nil
}

func (svc *Service) CollaborativeExit(
	ctx context.Context, password, address string, amount uint64,
) (string, error) {
	if err := svc.verifyPassword(password); err != nil {
		return "", err
	}

	txid, err := svc.arkClient.CollaborativeExit(ctx, address, amount)
	if err != nil {
		return "", fmt.Errorf("failed to exit: %w", err)
	}
	return txid, nil
}

func (svc *Service) Settle(ctx context.Context, password string) (string, error) {
	if err := svc.verifyPassword(password); err != nil {
		return "", err
	}
	txid, err := svc.arkClient.Settle(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to settle: %w", err)
	}
	return txid, nil
}

func (svc *Service) DumpSeed(ctx context.Context, password string) (string, error) {
	if err := svc.verifyPassword(password); err != nil {
		return "", err
	}
	seed, err := svc.arkClient.Dump(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to dump wallet seed: %w", err)
	}
	return seed, nil
}

func (svc *Service) verifyPassword(password string) error {
	if svc.cfg == nil {
		return fmt.Errorf("password verification unavailable")
	}
	expected := svc.cfg.WalletPassword
	if subtle.ConstantTimeCompare([]byte(password), []byte(expected)) != 1 {
		return ErrInvalidPassword
	}
	return nil
}

func isBTC(assetID string) bool {
	return assetID == "" || assetID == "BTC"
}

func applyAssetMetadata(info *AssetInfo, metadata []asset.Metadata) {
	for _, md := range metadata {
		key := strings.ToLower(string(md.Key))
		val := string(md.Value)
		switch key {
		case "ticker", "symbol":
			info.Ticker = val
		case "name":
			info.Name = val
		case "icon", "icon_url", "logo":
			info.IconURL = val
		case "decimals":
			if n, err := strconv.Atoi(val); err == nil && n >= 0 {
				info.Decimals = uint32(n) //nolint:gosec
			}
		}
	}
}
