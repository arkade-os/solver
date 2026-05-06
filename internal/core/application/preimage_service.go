package application

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/ArkLabsHQ/introspector/pkg/arkade"
	introclient "github.com/ArkLabsHQ/introspector/pkg/client"
	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	arksdk "github.com/arkade-os/go-sdk"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/sirupsen/logrus"

	"github.com/arkade-os/bancod/internal/core/ports"
	"github.com/arkade-os/bancod/pkg/preimage"
	"github.com/arkade-os/bancod/pkg/solver"
	"github.com/arkade-os/bancod/pkg/solver/arkdsource"
)

type PreimageServiceConfig struct {
	ArkClient    arksdk.ArkClient
	Introspector introclient.TransportClient
	Repository   ports.PreimageRepository
	Log          logrus.FieldLogger
}

type RegisterClaimInput struct {
	Preimage     []byte
	ArkadeScript []byte
	Taptree      []string
}

// PreimageService runs the solver loop and exposes the API methods used by
// the gRPC handler.
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
	if cfg.Repository == nil {
		return nil, fmt.Errorf("PreimageServiceConfig.Repository must not be nil")
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
		Repository:          svc.cfg.Repository,
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

// RegisterClaim validates input, derives the claim address, stores credentials.
func (svc *PreimageService) RegisterClaim(ctx context.Context, input RegisterClaimInput) (string, error) {
	if len(input.Preimage) == 0 {
		return "", fmt.Errorf("preimage must not be empty")
	}
	if len(input.ArkadeScript) == 0 {
		return "", fmt.Errorf("arkade_script must not be empty")
	}
	if len(input.Taptree) == 0 {
		return "", fmt.Errorf("taptree must not be empty")
	}

	if _, err := preimage.ValidateArkadeScript(input.ArkadeScript); err != nil {
		return "", fmt.Errorf("invalid arkade_script: %w", err)
	}

	vtxoScript := &script.TapscriptsVtxoScript{}
	if err := vtxoScript.Decode(input.Taptree); err != nil {
		return "", fmt.Errorf("decode taptree: %w", err)
	}

	expectedTweaked := arkade.ComputeArkadeScriptPublicKey(
		svc.introspectorPubkey, arkade.ArkadeScriptHash(input.ArkadeScript),
	)
	if !taptreeContainsClaimClosure(vtxoScript, svc.serverPubkey, expectedTweaked) {
		return "", fmt.Errorf("taptree does not contain a claim closure for the given arkade_script")
	}

	tapKey, _, err := vtxoScript.TapTree()
	if err != nil {
		return "", fmt.Errorf("compute taptree key: %w", err)
	}
	pkScript, err := script.P2TRScript(tapKey)
	if err != nil {
		return "", fmt.Errorf("compute pkScript: %w", err)
	}
	addr := &arklib.Address{
		HRP:        svc.network.Addr,
		Signer:     svc.serverPubkey,
		VtxoTapKey: tapKey,
	}
	claimAddress, err := addr.EncodeV0()
	if err != nil {
		return "", fmt.Errorf("encode claim address: %w", err)
	}

	creds := preimage.ClaimCredentials{
		Preimage:     input.Preimage,
		ClaimAddress: claimAddress,
		PkScript:     pkScript,
		ArkadeScript: input.ArkadeScript,
		Taptree:      input.Taptree,
	}

	if err := svc.cfg.Repository.Add(ctx, creds); err != nil {
		return "", fmt.Errorf("store claim: %w", err)
	}
	svc.log.WithField("claim_address", claimAddress).Info("preimage claim registered")
	return claimAddress, nil
}

func (svc *PreimageService) ListClaims(ctx context.Context) ([]preimage.ClaimInfo, error) {
	return svc.cfg.Repository.List(ctx)
}

func (svc *PreimageService) DeleteClaim(ctx context.Context, claimAddress string) error {
	if claimAddress == "" {
		return fmt.Errorf("claim_address must not be empty")
	}
	decoded, err := arklib.DecodeAddressV0(claimAddress)
	if err != nil {
		return fmt.Errorf("decode claim address: %w", err)
	}
	pkScript, err := script.P2TRScript(decoded.VtxoTapKey)
	if err != nil {
		return fmt.Errorf("compute pkScript: %w", err)
	}
	return svc.cfg.Repository.Delete(ctx, pkScript)
}

// taptreeContainsClaimClosure compares pubkeys via x-only Schnorr serialization
// because closure pubkeys lose Y-parity across the encode/decode boundary.
func taptreeContainsClaimClosure(
	vtxoScript *script.TapscriptsVtxoScript,
	serverPubKey, expectedTweaked *btcec.PublicKey,
) bool {
	wantServer := schnorr.SerializePubKey(serverPubKey)
	wantTweaked := schnorr.SerializePubKey(expectedTweaked)
	for _, c := range vtxoScript.Closures {
		cmc, ok := c.(*script.ConditionMultisigClosure)
		if !ok {
			continue
		}
		if len(cmc.PubKeys) != 2 {
			continue
		}
		k0 := schnorr.SerializePubKey(cmc.PubKeys[0])
		k1 := schnorr.SerializePubKey(cmc.PubKeys[1])
		match := (bytes.Equal(k0, wantServer) && bytes.Equal(k1, wantTweaked)) ||
			(bytes.Equal(k0, wantTweaked) && bytes.Equal(k1, wantServer))
		if match {
			return true
		}
	}
	return false
}
