package preimage

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"

	introclient "github.com/ArkLabsHQ/introspector/pkg/client"
	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/arkade-os/arkd/pkg/ark-lib/extension"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/arkade-os/arkd/pkg/ark-lib/txutils"
	"github.com/arkade-os/arkd/pkg/client-lib/indexer"
	arksdk "github.com/arkade-os/go-sdk"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/wire"
	"github.com/sirupsen/logrus"

	"github.com/arkade-os/solver/pkg/solver"
	"github.com/arkade-os/solver/pkg/solver/builder"
)

type Config struct {
	ArkClient     arksdk.Wallet
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

	preimg, err := Decrypt(p.cfg.SolverPrivKey, pkt.Ciphertext)
	if err != nil {
		p.log.WithError(err).Debug("preimage decrypt failed")
		return nil, builder.ErrSkip
	}
	if len(preimg) != 32 {
		p.log.Debugf("decrypted preimage has wrong length %d (want 32)", len(preimg))
		return nil, builder.ErrSkip
	}
	if _, err := ValidateArkadeScript(pkt.ArkadeScript); err != nil {
		p.log.WithError(err).Debug("preimage arkade script invalid")
		return nil, builder.ErrSkip
	}
	expectedTweaked := introspectorTweakedKey(pkt.ArkadeScript, p.cfg.IntrospectorPubKey)

	for i, out := range tx.UnsignedTx.TxOut {
		if i >= len(tx.Outputs) {
			break
		}
		po := tx.Outputs[i]
		if len(po.TaprootTapTree) == 0 {
			continue
		}

		scripts, err := txutils.DecodeTapTree(po.TaprootTapTree)
		if err != nil {
			p.log.WithError(err).Debug("preimage taptree decode failed")
			continue
		}

		vs := &script.TapscriptsVtxoScript{}
		if err := vs.Decode(scripts); err != nil {
			p.log.WithError(err).Debug("preimage taptree.Decode failed")
			continue
		}
		if _, err := findClaimClosure(vs, p.cfg.ServerPubKey, expectedTweaked); err != nil {
			continue
		}
		tapKey, _, err := vs.TapTree()
		if err != nil {
			continue
		}
		expectedPk, err := script.P2TRScript(tapKey)
		if err != nil {
			continue
		}
		if !bytes.Equal(out.PkScript, expectedPk) {
			continue
		}

		return &MatchedClaim{
			Outpoint: wire.OutPoint{Hash: tx.UnsignedTx.TxHash(), Index: uint32(i)},
			Amount:   uint64(out.Value),
			Credentials: ClaimCredentials{
				Preimage:     preimg,
				ArkadeScript: pkt.ArkadeScript,
				Taptree:      scripts,
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
	log := p.log.WithField("outpoint", m.Outpoint.String())

	log.WithField("amount", m.Amount).
		WithField("arkade_script_hex", hex.EncodeToString(m.Credentials.ArkadeScript)).
		WithField("pk_script_hex", hex.EncodeToString(m.Credentials.PkScript)).
		WithField("taptree_leaves", len(m.Credentials.Taptree)).
		WithField("preimage_len", len(m.Credentials.Preimage)).
		Debug("preimage claim: matched, building ark tx")

	arkTx, checkpoints, err := BuildClaim(
		m, p.cfg.CheckpointTapscript, p.cfg.ServerPubKey, p.cfg.IntrospectorPubKey,
	)
	if err != nil {
		log.WithError(err).Warn("preimage claim build failed")
		return
	}

	arkTxB64, b64Err := arkTx.B64Encode()
	if b64Err != nil {
		log.WithError(b64Err).Warn("preimage claim: failed to b64-encode unsigned ark tx (debug)")
		arkTxB64 = "<encode-error>"
	}
	cpTxids := make([]string, len(checkpoints))
	cpB64 := make([]string, len(checkpoints))
	for i, cp := range checkpoints {
		cpTxids[i] = cp.UnsignedTx.TxHash().String()
		s, err := cp.B64Encode()
		if err != nil {
			cpB64[i] = "<encode-error>"
		} else {
			cpB64[i] = s
		}
	}
	log.WithField("ark_txid", arkTx.UnsignedTx.TxHash().String()).
		WithField("unsigned_ark_tx_b64", arkTxB64).
		WithField("num_checkpoints", len(checkpoints)).
		WithField("checkpoint_txids", cpTxids).
		WithField("unsigned_checkpoints_b64", cpB64).
		Debug("preimage claim: built ark tx + checkpoints")

	txid, err := SubmitClaim(ctx, p.cfg.ArkClient, p.cfg.Introspector, arkTx, checkpoints)
	if err != nil {
		log.WithError(err).
			WithField("ark_txid", arkTx.UnsignedTx.TxHash().String()).
			WithField("checkpoint_txids", cpTxids).
			Warn("preimage claim submit failed")
		return
	}

	log.WithField("amount", m.Amount).
		WithField("txid", txid).
		Info("preimage claim submitted")
}
