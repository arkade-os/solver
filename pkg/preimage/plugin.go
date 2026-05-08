package preimage

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"

	"github.com/ArkLabsHQ/introspector/pkg/arkade"
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

type Config struct {
	ArkClient     arksdk.ArkClient
	Introspector  introclient.TransportClient
	SolverPrivKey *btcec.PrivateKey

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
	if cfg.SolverPrivKey == nil {
		return nil, fmt.Errorf("solver privkey must not be nil")
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

	return builder.ForExtension(p.decode).
		Validate(p.checkVtxoSpendable).
		Solve(p.claim).
		WithLogger(p.log).
		Build(), nil
}

type plugin struct {
	cfg Config
	log logrus.FieldLogger
}

func (p *plugin) decode(
	_ context.Context, tx *psbt.Packet, ext extension.Extension,
) (*MatchedClaim, error) {
	if tx == nil || tx.UnsignedTx == nil {
		return nil, builder.ErrSkip
	}

	pkt, err := FindClaim(ext)
	if err != nil {
		p.log.WithError(err).Debug("preimage extension parse failed")
		return nil, builder.ErrSkip
	}
	if pkt == nil {
		return nil, builder.ErrSkip
	}

	plaintext, err := Decrypt(p.cfg.SolverPrivKey, pkt.Ciphertext)
	if err != nil {
		p.log.WithError(err).Debug("preimage decrypt failed")
		return nil, builder.ErrSkip
	}
	preimg, arkadeScript, err := SplitSecretPayload(plaintext)
	if err != nil {
		p.log.WithError(err).Debug("preimage payload parse failed")
		return nil, builder.ErrSkip
	}
	if _, err := ValidateArkadeScript(arkadeScript); err != nil {
		p.log.WithError(err).Debug("preimage arkade script invalid")
		return nil, builder.ErrSkip
	}

	vtxoScript := &script.TapscriptsVtxoScript{}
	if err := vtxoScript.Decode(pkt.Taptree); err != nil {
		p.log.WithError(err).Debug("preimage taptree decode failed")
		return nil, builder.ErrSkip
	}
	expectedTweaked := arkade.ComputeArkadeScriptPublicKey(
		p.cfg.IntrospectorPubKey, arkade.ArkadeScriptHash(arkadeScript),
	)
	if _, err := findClaimClosure(vtxoScript, p.cfg.ServerPubKey, expectedTweaked); err != nil {
		p.log.WithError(err).Debug("preimage taptree missing expected closure")
		return nil, builder.ErrSkip
	}

	tapKey, _, err := vtxoScript.TapTree()
	if err != nil {
		p.log.WithError(err).Debug("preimage taptree taproot key failed")
		return nil, builder.ErrSkip
	}
	expectedPk, err := script.P2TRScript(tapKey)
	if err != nil {
		p.log.WithError(err).Debug("preimage P2TR script failed")
		return nil, builder.ErrSkip
	}

	for i, out := range tx.UnsignedTx.TxOut {
		if !bytes.Equal(out.PkScript, expectedPk) {
			continue
		}
		hash := tx.UnsignedTx.TxHash()
		return &MatchedClaim{
			Outpoint: wire.OutPoint{Hash: hash, Index: uint32(i)},
			Amount:   uint64(out.Value),
			Credentials: ClaimCredentials{
				Preimage:     preimg,
				ArkadeScript: arkadeScript,
				Taptree:      pkt.Taptree,
				PkScript:     expectedPk,
			},
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
}
