package application

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"encoding/hex"
	introclient "github.com/ArkLabsHQ/introspector/pkg/client"
	arksdk "github.com/arkade-os/go-sdk"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/sirupsen/logrus"

	"github.com/arkade-os/bancod/pkg/solver"
	"github.com/arkade-os/bancod/pkg/solver/arkdsource"
	"github.com/arkade-os/bancod/pkg/solver/bounty"
)

// BountyServiceConfig wires the BountyService.
type BountyServiceConfig struct {
	ArkClient    arksdk.ArkClient
	Introspector introclient.TransportClient
	BatchSize    int
	BatchTimeout time.Duration
	Log          logrus.FieldLogger
}

// BountyService owns the bounty plugin lifecycle (its own solver + arkdsource
// subscription + flushLoop). Mirrors TakerService's shape but is stateless on
// the application side — bounty has no pairs/trades to persist.
type BountyService struct {
	cfg                BountyServiceConfig
	introspectorPubkey *btcec.PublicKey
	log                logrus.FieldLogger

	cancel  context.CancelFunc
	done    chan struct{}
	mu      sync.RWMutex
	running bool
}

// NewBountyService validates config and pre-fetches the introspector signer
// pubkey (needed by the bounty plugin and by CreateBounty calls).
func NewBountyService(ctx context.Context, cfg BountyServiceConfig) (*BountyService, error) {
	if cfg.ArkClient == nil {
		return nil, fmt.Errorf("BountyServiceConfig.ArkClient must not be nil")
	}
	if cfg.Introspector == nil {
		return nil, fmt.Errorf("BountyServiceConfig.Introspector must not be nil")
	}
	if cfg.Log == nil {
		cfg.Log = logrus.StandardLogger()
	}
	info, err := cfg.Introspector.GetInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("get introspector info: %w", err)
	}
	raw, err := hex.DecodeString(info.SignerPublicKey)
	if err != nil {
		return nil, fmt.Errorf("decode introspector pubkey: %w", err)
	}
	pub, err := btcec.ParsePubKey(raw)
	if err != nil {
		return nil, fmt.Errorf("parse introspector pubkey: %w", err)
	}
	return &BountyService{cfg: cfg, introspectorPubkey: pub, log: cfg.Log}, nil
}

// IntrospectorPubkey exposes the cached pubkey, useful for callers that want
// to issue bounties via this service's settings.
func (svc *BountyService) IntrospectorPubkey() *btcec.PublicKey {
	return svc.introspectorPubkey
}

// Start spawns the solver run goroutine, subscribed to arkd's tx stream.
// The plugin's background flushLoop is started inside bounty.NewPlugin and
// shares the same ctx — so Stop() shuts both down together.
func (svc *BountyService) Start() error {
	ctx, cancel := context.WithCancel(context.Background())

	plugin, err := bounty.NewPlugin(ctx, bounty.Config{
		ArkClient:          svc.cfg.ArkClient,
		Introspector:       svc.cfg.Introspector,
		IntrospectorPubkey: svc.introspectorPubkey,
		BatchSize:          svc.cfg.BatchSize,
		BatchTimeout:       svc.cfg.BatchTimeout,
		Log:                svc.log,
	})
	if err != nil {
		cancel()
		return fmt.Errorf("build bounty plugin: %w", err)
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
			svc.log.WithError(err).Error("bounty solver run exited")
		}
	}()
	return nil
}

// Stop signals shutdown and waits for the run goroutine + flushLoop to exit.
func (svc *BountyService) Stop() {
	if svc.cancel != nil {
		svc.cancel()
		<-svc.done
	}
}

// Status reports whether the solver loop is active.
func (svc *BountyService) Status() Status {
	svc.mu.RLock()
	defer svc.mu.RUnlock()
	return Status{Running: svc.running}
}

func (svc *BountyService) setRunning(v bool) {
	svc.mu.Lock()
	svc.running = v
	svc.mu.Unlock()
}

// CreateBounty is a thin wrapper that injects the cached introspector pubkey
// so callers don't have to fetch it themselves.
func (svc *BountyService) CreateBounty(
	ctx context.Context, difficulty uint8, receiverPkScript []byte, amount uint64,
) (*bounty.CreateResult, error) {
	return bounty.CreateBounty(ctx, bounty.CreateParams{
		Difficulty:         difficulty,
		ReceiverPkScript:   receiverPkScript,
		Amount:             amount,
		IntrospectorPubkey: svc.introspectorPubkey,
	}, svc.cfg.ArkClient)
}
