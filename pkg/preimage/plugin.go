package preimage

import (
	"context"
	"encoding/hex"
	"fmt"

	introclient "github.com/ArkLabsHQ/introspector/pkg/client"
	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/arkade-os/arkd/pkg/client-lib/indexer"
	arksdk "github.com/arkade-os/go-sdk"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/wire"
	"github.com/sirupsen/logrus"

	"github.com/arkade-os/bancod/pkg/solver"
	"github.com/arkade-os/bancod/pkg/solver/builder"
)

type Config struct {
	ArkClient    arksdk.ArkClient
	Introspector introclient.TransportClient
	Repository   Repository

	IntrospectorPubKey  *btcec.PublicKey
	ServerPubKey        *btcec.PublicKey
	CheckpointTapscript []byte
	Network             arklib.Network
	Log                 logrus.FieldLogger
}

func NewPlugin(_ context.Context, cfg Config) (solver.Plugin, error) {
	if cfg.ArkClient == nil {
		return nil, fmt.Errorf("ark client must not be nil")
	}
	if cfg.Introspector == nil {
		return nil, fmt.Errorf("introspector client must not be nil")
	}
	if cfg.Repository == nil {
		return nil, fmt.Errorf("repository must not be nil")
	}
	if cfg.IntrospectorPubKey == nil {
		return nil, fmt.Errorf("introspector pubkey must not be nil")
	}
	if cfg.ServerPubKey == nil {
		return nil, fmt.Errorf("server pubkey must not be nil")
	}
	if len(cfg.CheckpointTapscript) == 0 {
		return nil, fmt.Errorf("checkpoint tapscript must not be empty")
	}
	if cfg.Log == nil {
		cfg.Log = logrus.New()
	}

	p := &plugin{cfg: cfg, log: cfg.Log}

	return builder.For[*MatchedClaim]().
		Decode(p.decode).
		Validate(p.checkVtxoSpendable).
		Solve(p.claim).
		WithLogger(p.log).
		Build(), nil
}

type plugin struct {
	cfg Config
	log logrus.FieldLogger
}

func (p *plugin) decode(ctx context.Context, tx *psbt.Packet) (*MatchedClaim, error) {
	if tx == nil || tx.UnsignedTx == nil {
		return nil, builder.ErrSkip
	}
	for i, out := range tx.UnsignedTx.TxOut {
		creds, ok, err := p.cfg.Repository.Get(ctx, out.PkScript)
		if err != nil {
			p.log.WithError(err).Debug("preimage repository Get errored, skipping output")
			continue
		}
		if !ok {
			continue
		}
		hash := tx.UnsignedTx.TxHash()
		return &MatchedClaim{
			Outpoint:    wire.OutPoint{Hash: hash, Index: uint32(i)},
			Amount:      uint64(out.Value),
			Credentials: creds,
		}, nil
	}
	return nil, builder.ErrSkip
}

func (p *plugin) checkVtxoSpendable(ctx context.Context, m *MatchedClaim) (bool, error) {
	resp, err := p.cfg.ArkClient.Indexer().GetVtxos(ctx,
		indexer.WithScripts([]string{hex.EncodeToString(m.Credentials.PkScript)}),
		indexer.WithSpendableOnly(),
	)
	if err != nil {
		return false, err
	}
	return len(resp.Vtxos) > 0, nil
}

func (p *plugin) claim(ctx context.Context, m *MatchedClaim) {
	arkTx, checkpoints, err := BuildClaim(
		m, p.cfg.CheckpointTapscript, p.cfg.ServerPubKey, p.cfg.IntrospectorPubKey,
	)
	if err != nil {
		p.log.WithError(err).WithField("outpoint", m.Outpoint.String()).
			Warn("preimage claim build failed")
		return
	}

	txid, err := SubmitClaim(ctx, p.cfg.ArkClient, p.cfg.Introspector, arkTx, checkpoints)
	if err != nil {
		p.log.WithError(err).WithField("outpoint", m.Outpoint.String()).
			Warn("preimage claim submit failed")
		return
	}

	p.log.
		WithField("outpoint", m.Outpoint.String()).
		WithField("amount", m.Amount).
		WithField("txid", txid).
		Info("preimage claim submitted")

	if delErr := p.removeFromRepo(ctx, m.Credentials.PkScript); delErr != nil {
		p.log.WithError(delErr).WithField("outpoint", m.Outpoint.String()).
			Warn("preimage registry delete failed (claim already submitted)")
	}
}

// removeFromRepo type-asserts to a deleter; no-op for read-only repos (tests).
func (p *plugin) removeFromRepo(ctx context.Context, pkScript []byte) error {
	deleter, ok := p.cfg.Repository.(interface {
		Delete(ctx context.Context, pkScript []byte) error
	})
	if !ok {
		return nil
	}
	return deleter.Delete(ctx, pkScript)
}
