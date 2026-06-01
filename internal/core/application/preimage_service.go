package application

import (
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/sirupsen/logrus"
)

type PreimageServiceConfig struct {
	SolverPrivKey  *btcec.PrivateKey // ECIES decryption key (derived from wallet seed)
	EmulatorPubKey *btcec.PublicKey
	Log            logrus.FieldLogger
}

// PreimageService exposes preimage plugin metadata for API clients.
type PreimageService struct {
	cfg PreimageServiceConfig
	log logrus.FieldLogger
}

func NewPreimageService(cfg PreimageServiceConfig) (*PreimageService, error) {
	if cfg.SolverPrivKey == nil {
		return nil, fmt.Errorf("PreimageServiceConfig.SolverPrivKey must not be nil")
	}
	if cfg.EmulatorPubKey == nil {
		return nil, fmt.Errorf("PreimageServiceConfig.EmulatorPubKey must not be nil")
	}
	if cfg.Log == nil {
		cfg.Log = logrus.StandardLogger()
	}

	return &PreimageService{cfg: cfg, log: cfg.Log}, nil
}

// SolverPubKey returns the encryption pubkey clients must use to ECIES-encrypt
// the secret payload.
func (svc *PreimageService) SolverPubKey() *btcec.PublicKey {
	return svc.cfg.SolverPrivKey.PubKey()
}

// EmulatorPubKey returns the bot's configured emulator pubkey,
// fetched at service construction time via Emulator.GetInfo().
func (svc *PreimageService) EmulatorPubKey() *btcec.PublicKey {
	return svc.cfg.EmulatorPubKey
}
