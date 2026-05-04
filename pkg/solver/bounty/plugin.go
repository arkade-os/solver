package bounty

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"slices"
	"sync"
	"time"

	introclient "github.com/ArkLabsHQ/introspector/pkg/client"
	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/arkade-os/arkd/pkg/ark-lib/extension"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/arkade-os/arkd/pkg/client-lib/indexer"
	arksdk "github.com/arkade-os/go-sdk"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/wire"
	"github.com/sirupsen/logrus"

	"github.com/arkade-os/bancod/pkg/solver"
	"github.com/arkade-os/bancod/pkg/solver/builder"
)

// Default batching parameters.
const (
	DefaultBatchSize    = 10
	DefaultBatchTimeout = 5 * time.Second
	// MaxClaimRetries bounds how many times a single flush will rebuild + re-mine
	// after the introspector / arkd reports a subset of inputs as already spent.
	// Each retry drops the offending entries and re-mines the survivors.
	MaxClaimRetries = 3
)

// Config configures a bounty plugin instance.
type Config struct {
	ArkClient          arksdk.ArkClient
	Introspector       introclient.TransportClient
	IntrospectorPubkey *btcec.PublicKey
	BatchSize          int
	BatchTimeout       time.Duration
	Log                logrus.FieldLogger
}

// MatchedBounty is the typed intent produced by the plugin's decode stage and
// consumed by the batcher.
type MatchedBounty struct {
	Difficulty       uint8
	ReceiverPkScript []byte
	FundingTxid      string
	Outpoint         wire.OutPoint
	Amount           uint64
	VtxoScript       *script.TapscriptsVtxoScript
}

// NewPlugin constructs a bounty solver plugin. ctx is the long-lived plugin
// context: cancelling it terminates the background flushLoop. The returned
// solver.Plugin is wired through the standard solver/builder pipeline.
func NewPlugin(ctx context.Context, cfg Config) (solver.Plugin, error) {
	if cfg.ArkClient == nil {
		return nil, fmt.Errorf("ark client must not be nil")
	}
	if cfg.Introspector == nil {
		return nil, fmt.Errorf("introspector client must not be nil")
	}
	if cfg.IntrospectorPubkey == nil {
		return nil, fmt.Errorf("introspector pubkey must not be nil")
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = DefaultBatchSize
	}
	if cfg.BatchTimeout <= 0 {
		cfg.BatchTimeout = DefaultBatchTimeout
	}
	if cfg.Log == nil {
		cfg.Log = logrus.New()
	}

	configData, err := cfg.ArkClient.GetConfigData(ctx)
	if err != nil {
		return nil, fmt.Errorf("get config data: %w", err)
	}
	checkpointTapscript, err := hex.DecodeString(configData.CheckpointTapscript)
	if err != nil {
		return nil, fmt.Errorf("decode checkpoint tapscript: %w", err)
	}

	p := &plugin{
		cfg:                 cfg,
		serverPubKey:        configData.SignerPubKey,
		network:             configData.Network,
		checkpointTapscript: checkpointTapscript,
		log:                 cfg.Log,
		buffer:              make(map[wire.OutPoint]*MatchedBounty),
		flushTrigger:        make(chan struct{}, 1),
	}

	go p.flushLoop(ctx)

	return builder.ForExtension(p.decode).
		Validate(p.checkVtxoSpendable).
		Solve(p.enqueue).
		WithLogger(p.log).
		Build(), nil
}

type plugin struct {
	cfg                 Config
	serverPubKey        *btcec.PublicKey
	network             arklib.Network
	checkpointTapscript []byte
	log                 logrus.FieldLogger

	mu     sync.Mutex
	buffer map[wire.OutPoint]*MatchedBounty // dedup by outpoint

	flushTrigger chan struct{}
}

// decode runs in Match. Pure CPU.
func (p *plugin) decode(
	_ context.Context, tx *psbt.Packet, ext extension.Extension,
) (*MatchedBounty, error) {
	pkt, err := FindAnnouncement(ext)
	if err != nil {
		// Malformed announcement payload: silent skip — stream may carry junk.
		return nil, builder.ErrSkip
	}
	if pkt == nil {
		return nil, builder.ErrSkip
	}

	vtxoScript, err := VtxoScript(
		pkt.Difficulty, pkt.ReceiverPkScript, p.serverPubKey, p.cfg.IntrospectorPubkey,
	)
	if err != nil {
		return nil, fmt.Errorf("rebuild vtxo script: %w", err)
	}
	tapKey, _, err := vtxoScript.TapTree()
	if err != nil {
		return nil, fmt.Errorf("rebuild taptree: %w", err)
	}
	bountyPkScript, err := script.P2TRScript(tapKey)
	if err != nil {
		return nil, fmt.Errorf("build bounty pkScript: %w", err)
	}

	for i, out := range tx.UnsignedTx.TxOut {
		if !bytes.Equal(out.PkScript, bountyPkScript) {
			continue
		}
		hash := tx.UnsignedTx.TxHash()
		return &MatchedBounty{
			Difficulty:       pkt.Difficulty,
			ReceiverPkScript: pkt.ReceiverPkScript,
			FundingTxid:      hash.String(),
			Outpoint:         wire.OutPoint{Hash: hash, Index: uint32(i)},
			Amount:           uint64(out.Value),
			VtxoScript:       vtxoScript,
		}, nil
	}
	// Extension was present but no matching bounty output in this tx — junk.
	return nil, builder.ErrSkip
}

// checkVtxoSpendable runs in Validate. Cheap RPC guard against doing work for
// a bounty that's already been claimed by another bot.
func (p *plugin) checkVtxoSpendable(ctx context.Context, m *MatchedBounty) (bool, error) {
	pkScript, err := p.bountyPkScriptOf(m)
	if err != nil {
		return false, err
	}
	resp, err := p.cfg.ArkClient.Indexer().GetVtxos(ctx,
		indexer.WithScripts([]string{hex.EncodeToString(pkScript)}),
		indexer.WithSpendableOnly(),
	)
	if err != nil {
		return false, err
	}
	return len(resp.Vtxos) > 0, nil
}

// enqueue runs in Solve. Buffers the matched bounty (dedup by outpoint) and
// signals a flush if the buffer reaches BatchSize.
func (p *plugin) enqueue(_ context.Context, m *MatchedBounty) {
	p.mu.Lock()
	if _, dup := p.buffer[m.Outpoint]; dup {
		p.mu.Unlock()
		return
	}
	p.buffer[m.Outpoint] = m
	triggered := len(p.buffer) >= p.cfg.BatchSize
	p.mu.Unlock()
	if triggered {
		select {
		case p.flushTrigger <- struct{}{}:
		default:
		}
	}
}

// flushLoop runs as a long-lived goroutine. Triggers on either timer tick or
// flushTrigger signal. Exits when ctx cancels.
//
// Go 1.23+ Timer semantics: Stop/Reset don't require draining the channel.
func (p *plugin) flushLoop(ctx context.Context) {
	timer := time.NewTimer(p.cfg.BatchTimeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			p.flush(ctx)
		case <-p.flushTrigger:
			timer.Stop()
			p.flush(ctx)
		}
		timer.Reset(p.cfg.BatchTimeout)
	}
}

// flush atomically drains the buffer, re-validates spendability, and runs a
// single mixed-difficulty claim. The claim retries internally on partial
// rejection (some inputs got claimed by competitors mid-mine).
func (p *plugin) flush(ctx context.Context) {
	p.mu.Lock()
	buffer := p.buffer
	p.buffer = make(map[wire.OutPoint]*MatchedBounty)
	p.mu.Unlock()
	if len(buffer) == 0 {
		return
	}
	survivors := make([]*MatchedBounty, 0, len(buffer))
	for _, m := range buffer {
		ok, err := p.checkVtxoSpendable(ctx, m)
		if err != nil {
			p.log.WithError(err).WithField("outpoint", m.Outpoint.String()).
				Debug("bounty spendability check errored, dropping")
			continue
		}
		if !ok {
			continue
		}
		survivors = append(survivors, m)
	}
	if len(survivors) == 0 {
		return
	}
	if err := p.claimWithRetry(ctx, survivors); err != nil {
		p.log.WithError(err).WithField("size", len(survivors)).
			Warn("bounty batch claim failed")
	}
}

// claimWithRetry tries to claim the batch up to MaxClaimRetries times. On a
// partial-spent rejection from arkd it drops the offending entries and re-mines
// the survivors; on any other error it gives up.
func (p *plugin) claimWithRetry(ctx context.Context, batch []*MatchedBounty) error {
	for attempt := 0; attempt < MaxClaimRetries; attempt++ {
		if len(batch) == 0 {
			return fmt.Errorf("no survivors after attempt %d", attempt)
		}
		err := p.claimBatch(ctx, batch)
		if err == nil {
			return nil
		}
		spent := parseSpentOutpoints(err)
		if len(spent) == 0 {
			// Not a partial-spent error — give up, return the original.
			return err
		}
		before := len(batch)
		batch = slices.DeleteFunc(batch, func(m *MatchedBounty) bool {
			return spent[m.Outpoint]
		})
		p.log.
			WithField("attempt", attempt+1).
			WithField("dropped", before-len(batch)).
			WithField("survivors", len(batch)).
			Warn("bounty batch partial spent, retrying with survivors")
	}
	return fmt.Errorf("exhausted %d claim retries", MaxClaimRetries)
}

// claimBatch resolves the bot's destination, builds the claim tx, mines for
// the max difficulty in the batch (which automatically satisfies all lower
// per-entry checks), and submits via the introspector.
func (p *plugin) claimBatch(ctx context.Context, batch []*MatchedBounty) error {
	takerAddr, err := p.cfg.ArkClient.NewOffchainAddress(ctx)
	if err != nil {
		return fmt.Errorf("get taker address: %w", err)
	}
	takerDecoded, err := arklib.DecodeAddressV0(takerAddr)
	if err != nil {
		return fmt.Errorf("decode taker address: %w", err)
	}
	takerPkScript, err := script.P2TRScript(takerDecoded.VtxoTapKey)
	if err != nil {
		return fmt.Errorf("build taker pkScript: %w", err)
	}

	arkTx, checkpoints, baseExt, extOutputIdx, err := BuildBatchClaim(
		batch, takerPkScript, p.checkpointTapscript,
	)
	if err != nil {
		return fmt.Errorf("build batch claim: %w", err)
	}

	mineDifficulty := maxDifficulty(batch)
	if _, err := Mine(ctx, arkTx, baseExt, extOutputIdx, mineDifficulty); err != nil {
		return fmt.Errorf("mine: %w", err)
	}

	arkTxid, err := SubmitBatchClaim(ctx, p.cfg.ArkClient, p.cfg.Introspector, arkTx, checkpoints)
	if err != nil {
		return err
	}
	p.log.
		WithField("size", len(batch)).
		WithField("difficulty", mineDifficulty).
		WithField("txid", arkTxid).
		Info("bounty batch claim submitted")
	return nil
}

// bountyPkScriptOf computes the bounty's funding pkScript from the matched intent.
func (p *plugin) bountyPkScriptOf(m *MatchedBounty) ([]byte, error) {
	tapKey, _, err := m.VtxoScript.TapTree()
	if err != nil {
		return nil, fmt.Errorf("vtxo taptree: %w", err)
	}
	return script.P2TRScript(tapKey)
}

// maxDifficulty returns the highest Difficulty across the batch. Mining a txid
// that satisfies the max also satisfies every lower per-entry check.
func maxDifficulty(batch []*MatchedBounty) uint8 {
	var max uint8
	for _, m := range batch {
		if m.Difficulty > max {
			max = m.Difficulty
		}
	}
	return max
}
