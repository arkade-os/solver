package application

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"

	"github.com/arkade-os/solver/pkg/discovery"
)

// BuildDiscoveryCard renders the solver's discovery card from the configured
// pairs, optionally signed. It also returns the registry path hint
// solvers/<network>/<name>.json — the card itself carries no network, the
// registry partitions by directory — with the network taken from arkd.
func (svc *Service) BuildDiscoveryCard(ctx context.Context, sign bool) ([]byte, string, error) {
	if svc.cfg == nil || svc.cfg.SolverName == "" {
		return nil, "", fmt.Errorf("SOLVER_NAME is not set: it names the card (solvers/<network>/<name>.json)")
	}
	name := svc.cfg.SolverName

	pairs, err := svc.pairRepo.List(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("list pairs: %w", err)
	}

	var opts discovery.Options
	if sign {
		key, err := svc.discoveryKey()
		if err != nil {
			return nil, "", err
		}
		opts.SecretKey = key
	}

	card, err := discovery.BuildCard(name, pairs, opts)
	if err != nil {
		return nil, "", err
	}

	network := "unknown"
	if cfgData, err := svc.arkClient.GetConfigData(ctx); err == nil && cfgData != nil {
		network = registryNetwork(cfgData.Network.Name)
	}
	path := fmt.Sprintf("solvers/%s/%s.json", network, name)

	return card, path, nil
}

// discoveryKey resolves the card-signing key: SOLVER_DISCOVERY_SECRET_KEY
// when set, otherwise derived from the wallet seed at the dedicated
// discovery BIP32 path.
func (svc *Service) discoveryKey() (*btcec.PrivateKey, error) {
	if svc.cfg.DiscoverySecretKey != "" {
		return discovery.ParseSecretKey(svc.cfg.DiscoverySecretKey)
	}
	seed, err := hex.DecodeString(svc.cfg.WalletSeed)
	if err != nil {
		return nil, fmt.Errorf("derive discovery key: wallet seed is not hex")
	}
	return discovery.DeriveSecretKey(seed)
}

// registryNetwork maps arkd's network name onto the registry's directory
// partitioning (same as ArkLabsHQ/asset-registry): arkd calls mainnet
// "bitcoin"; every other name is used as-is.
func registryNetwork(arkdName string) string {
	if arkdName == "bitcoin" {
		return "mainnet"
	}
	if arkdName == "" {
		return "unknown"
	}
	return arkdName
}
