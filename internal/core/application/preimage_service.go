package application

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	introclient "github.com/ArkLabsHQ/introspector/pkg/client"
	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	arksdk "github.com/arkade-os/go-sdk"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/sirupsen/logrus"

	"github.com/arkade-os/bancod/pkg/preimage"
	"github.com/arkade-os/bancod/pkg/solver"
	"github.com/arkade-os/bancod/pkg/solver/arkdsource"
)

type PreimageServiceConfig struct {
	ArkClient     arksdk.ArkClient
	Introspector  introclient.TransportClient
	SolverPrivKey *btcec.PrivateKey // ECIES decryption key (derived from wallet seed)
	Log           logrus.FieldLogger
}

// PreimageService runs the preimage solver loop and exposes the solver pubkey
// for clients to encrypt against.
type PreimageService struct {
	cfg                PreimageServiceConfig
	introspectorPubkey *btcec.PublicKey
	serverPubkey       *btcec.PublicKey
	checkpointScript   []byte
	network            arklib.Network
	log                logrus.FieldLogger

	cancel  context.CancelFunc
	done    chan struct{}
	mu      sync.RWMutex
	running bool
}

func NewPreimageService(ctx context.Context, cfg PreimageServiceConfig) (*PreimageService, error) {
	if cfg.ArkClient == nil {
		return nil, fmt.Errorf("PreimageServiceConfig.ArkClient must not be nil")
	}
	if cfg.Introspector == nil {
		return nil, fmt.Errorf("PreimageServiceConfig.Introspector must not be nil")
	}
	if cfg.SolverPrivKey == nil {
		return nil, fmt.Errorf("PreimageServiceConfig.SolverPrivKey must not be nil")
	}
	if cfg.Log == nil {
		cfg.Log = logrus.StandardLogger()
	}

	info, err := cfg.Introspector.GetInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("get introspector info: %w", err)
	}
	rawIntro, err := hex.DecodeString(info.SignerPublicKey)
	if err != nil {
		return nil, fmt.Errorf("decode introspector pubkey: %w", err)
	}
	introPub, err := btcec.ParsePubKey(rawIntro)
	if err != nil {
		return nil, fmt.Errorf("parse introspector pubkey: %w", err)
	}

	configData, err := cfg.ArkClient.GetConfigData(ctx)
	if err != nil {
		return nil, fmt.Errorf("get ark config: %w", err)
	}
	checkpointBytes, err := hex.DecodeString(configData.CheckpointTapscript)
	if err != nil {
		return nil, fmt.Errorf("decode checkpoint tapscript: %w", err)
	}

	return &PreimageService{
		cfg:                cfg,
		introspectorPubkey: introPub,
		serverPubkey:       configData.SignerPubKey,
		checkpointScript:   checkpointBytes,
		network:            configData.Network,
		log:                cfg.Log,
	}, nil
}

func (svc *PreimageService) Start() error {
	ctx, cancel := context.WithCancel(context.Background())

	plugin, err := preimage.NewPlugin(ctx, preimage.Config{
		ArkClient:           svc.cfg.ArkClient,
		Introspector:        svc.cfg.Introspector,
		SolverPrivKey:       svc.cfg.SolverPrivKey,
		IntrospectorPubKey:  svc.introspectorPubkey,
		ServerPubKey:        svc.serverPubkey,
		CheckpointTapscript: svc.checkpointScript,
		Network:             svc.network,
		Log:                 svc.log,
	})
	if err != nil {
		cancel()
		return fmt.Errorf("build preimage plugin: %w", err)
	}

	s := solver.New(plugin).WithLogger(svc.log)
	txs := arkdsource.Subscribe(ctx, svc.cfg.ArkClient, svc.log)

	svc.cancel = cancel
	svc.done = make(chan struct{})
	svc.setRunning(true)

	go func() {
		defer close(svc.done)
		defer svc.setRunning(false)
		if err := s.Run(ctx, txs); err != nil && !errors.Is(err, context.Canceled) {
			svc.log.WithError(err).Error("preimage solver run exited")
		}
	}()
	return nil
}

func (svc *PreimageService) Stop() {
	if svc.cancel != nil {
		svc.cancel()
		<-svc.done
	}
}

func (svc *PreimageService) Status() Status {
	svc.mu.RLock()
	defer svc.mu.RUnlock()
	return Status{Running: svc.running}
}

func (svc *PreimageService) setRunning(v bool) {
	svc.mu.Lock()
	svc.running = v
	svc.mu.Unlock()
}

// SolverPubKey returns the encryption pubkey clients must use to ECIES-encrypt
// the secret payload.
func (svc *PreimageService) SolverPubKey() *btcec.PublicKey {
	return svc.cfg.SolverPrivKey.PubKey()
}

// IntrospectorPubKey returns the bot's configured introspector pubkey,
// fetched at service construction time via Introspector.GetInfo().
func (svc *PreimageService) IntrospectorPubKey() *btcec.PublicKey {
	return svc.introspectorPubkey
}
