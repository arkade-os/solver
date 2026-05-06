package banco

import (
	"context"
	"fmt"
	"time"

	introclient "github.com/ArkLabsHQ/introspector/pkg/client"
	"github.com/arkade-os/arkd/pkg/ark-lib/extension"
	arksdk "github.com/arkade-os/go-sdk"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/sirupsen/logrus"

	"github.com/arkade-os/bancod/pkg/banco/contract"
	"github.com/arkade-os/bancod/pkg/solver"
	"github.com/arkade-os/bancod/pkg/solver/builder"
)

// MatchedOffer is the typed intent produced by the banco Plugin's decode
// stage and consumed by Solve. It carries the parsed offer plus the matched
// pair so Solve doesn't redo lookups.
type MatchedOffer struct {
	Offer *Offer
	Pair  *Pair
}

// plugin holds the runtime state shared across builder stages. It's
// constructed by NewPlugin and never escapes the package.
type plugin struct {
	arkClient    arksdk.ArkClient
	introspector introclient.TransportClient
	pairs        PairRepository
	prices       *priceCache
	listener     FulfillmentListener
	log          logrus.FieldLogger
}

// NewPlugin builds a banco solver.Plugin. Authoring uses builder.ForExtension
// because banco needs multi-TLV access (offer payload + asset packet); the
// framework handles OP_RETURN parsing and we only write protocol-specific
// decode + validators + solve.
func NewPlugin(cfg Config) solver.Plugin {
	cfg = cfg.WithDefault()
	p := &plugin{
		arkClient:    cfg.SolverClient,
		introspector: cfg.Introspector,
		pairs:        cfg.PairsRepository,
		prices:       newPriceCache(cfg.PriceFeed, cfg.PriceCacheTTL),
		listener:     cfg.Listener,
		log:          cfg.Log,
	}
	solverPlugin := builder.ForExtension(p.decode).
		Validate(p.checkAmountInRange).
		Validate(p.checkPriceTolerance).
		Validate(p.checkBTCBalance).
		Solve(p.fulfill).
		WithLogger(p.log).
		Build()

	return solverPlugin
}

// decode turns a tx + parsed extension into a *MatchedOffer. Returns
// builder.ErrSkip when the tx isn't a banco offer or no configured pair
// matches.
func (p *plugin) decode(ctx context.Context, tx *psbt.Packet, ext extension.Extension) (*MatchedOffer, error) {
	offer, err := NewOfferFromExtension(tx.UnsignedTx, ext)
	if err != nil {
		return nil, err
	}
	if offer == nil {
		return nil, builder.ErrSkip
	}
	if p.pairs == nil {
		return nil, builder.ErrSkip
	}
	pairs, err := p.pairs.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list pairs: %w", err)
	}
	pair := findMatchingPair(pairs, offer)
	if pair == nil {
		return nil, builder.ErrSkip
	}
	return &MatchedOffer{Offer: offer, Pair: pair}, nil
}

// checkAmountInRange silently rejects offers outside the configured pair's
// MinAmount/MaxAmount window.
func (p *plugin) checkAmountInRange(_ context.Context, m *MatchedOffer) (bool, error) {
	if m.Offer.WantAmount < m.Pair.MinAmount || m.Offer.WantAmount > m.Pair.MaxAmount {
		return false, nil
	}
	return true, nil
}

// checkPriceTolerance rejects offers whose price deviates more than 1% from
// the feed. Logs (Warn) when the price feed is unavailable or stale.
func (p *plugin) checkPriceTolerance(ctx context.Context, m *MatchedOffer) (bool, error) {
	feedPrice, err := p.prices.get(ctx, m.Pair.PriceFeed)
	if err != nil && feedPrice == 0 {
		p.log.WithError(err).Warn("price feed unavailable, skipping offer")
		return false, nil
	}
	if err != nil {
		p.log.WithError(err).Warn("using stale price feed")
	}
	if m.Pair.InvertPrice {
		feedPrice = 1.0 / feedPrice
	}
	offerPrice, ok := m.Offer.ComputePrice(m.Pair)
	if !ok {
		return false, nil
	}
	return validatePrice(offerPrice, feedPrice), nil
}

// checkBTCBalance ensures we hold enough offchain BTC to honor a BTC-deposit
// offer. Asset deposits skip this check.
func (p *plugin) checkBTCBalance(ctx context.Context, m *MatchedOffer) (bool, error) {
	if m.Offer.WantAsset != nil {
		return true, nil
	}
	bal, err := p.arkClient.Balance(ctx)
	if err != nil {
		return false, fmt.Errorf("get balance: %w", err)
	}
	if bal.OffchainBalance.Total < m.Offer.WantAmount {
		p.log.Warnf(
			"insufficient offchain balance: have %d want %d",
			bal.OffchainBalance.Total, m.Offer.WantAmount,
		)
		return false, nil
	}
	return true, nil
}

// fulfill is the terminal action — atomically settles the matched offer and
// notifies the FulfillmentListener if one is configured.
func (p *plugin) fulfill(ctx context.Context, m *MatchedOffer) {
	result, err := contract.FulfillOffer(ctx, m.Offer.Offer, p.arkClient, p.introspector)
	if err != nil {
		p.log.WithError(err).Warn("fulfillment failed")
		return
	}
	if p.listener == nil {
		return
	}
	p.listener.OnFulfill(ctx, FulfillmentEvent{
		Pair:          m.Pair.Pair,
		DepositAsset:  m.Offer.DepositAssetStr(),
		DepositAmount: m.Offer.DepositAmount,
		WantAsset:     m.Offer.WantAssetStr(),
		WantAmount:    m.Offer.WantAmount,
		OfferTxid:     m.Offer.FundingTxid,
		FulfillTxid:   result.ArkTxid,
		Timestamp:     time.Now().UTC(),
	})
}

// findMatchingPair returns the first pair whose base+quote both match
// the offer's deposit and want assets.
func findMatchingPair(pairs []Pair, o *Offer) *Pair {
	depositAsset := o.DepositAssetStr()
	wantAsset := o.WantAssetStr()
	for i := range pairs {
		pair := &pairs[i]
		if pair.Base() != depositAsset {
			continue
		}
		if pair.Quote() != wantAsset {
			continue
		}
		return pair
	}
	return nil
}
