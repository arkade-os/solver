package swap

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/arkade-os/arkd/pkg/ark-lib/extension"
	emulatorclient "github.com/arkade-os/emulator/pkg/client"
	arksdk "github.com/arkade-os/go-sdk"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/sirupsen/logrus"

	"github.com/arkade-os/solver/pkg/executor"
	"github.com/arkade-os/solver/pkg/swap/contract"
)

// MatchedOffer is the typed intent produced by Match and consumed by Solve.
// It carries the parsed offer plus the matched market and direction so Solve
// doesn't redo lookups.
type MatchedOffer struct {
	Offer     *Offer
	Market    *Market
	Direction Direction
}

// plugin implements executor.Plugin for swap. It's constructed by NewPlugin
// and never escapes the package.
type plugin struct {
	arkClient arksdk.Wallet
	emulator  emulatorclient.TransportClient
	markets   MarketRepository
	prices    *priceCache
	listener  AttemptListener
	log       logrus.FieldLogger
}

// NewPlugin builds a swap executor.Plugin.
func NewPlugin(cfg Config) executor.Plugin {
	cfg = cfg.WithDefault()
	return &plugin{
		arkClient: cfg.SolverClient,
		emulator:  cfg.Emulator,
		markets:   cfg.MarketsRepository,
		prices:    newPriceCache(cfg.PriceFeed, cfg.PriceCacheTTL),
		listener:  cfg.Listener,
		log:       cfg.Log,
	}
}

// Filter applies no server-side CEL filter: swap inspects every tx for an
// ark extension OP_RETURN in Match.
func (p *plugin) Filter() string { return "" }

// Match decodes the tx into a *MatchedOffer and runs the validation gates.
// It returns (nil, false) for any tx that isn't a swap offer, doesn't match
// a configured market, or fails a validation check. A tx that matched a market
// but was then rejected is reported to the AttemptListener; anything rejected
// before that (not an offer, no matching market) is not, since there's no
// market to record it against.
func (p *plugin) Match(ctx context.Context, tx *psbt.Packet) (any, bool) {
	m, err := p.decode(ctx, tx)
	if err != nil {
		p.log.WithError(err).Debug("swap decode failed")
		return nil, false
	}
	if m == nil {
		return nil, false
	}

	checks := []func(context.Context, *MatchedOffer) (string, error){
		p.checkPriceTolerance, p.checkBTCBalance,
	}
	for _, check := range checks {
		reason, err := check(ctx, m)
		if err != nil {
			reason = err.Error()
		}
		if reason != "" {
			p.notify(ctx, m, "", reason)
			return nil, false
		}
	}

	return m, true
}

// Solve atomically settles the matched offer and notifies the
// AttemptListener if one is configured. It returns cleanly on a nil or
// wrong-typed intent.
func (p *plugin) Solve(ctx context.Context, intent any) {
	m, ok := intent.(*MatchedOffer)
	if !ok || m == nil {
		return
	}
	p.fulfill(ctx, m)
}

// decode parses the tx's ark extension into a *MatchedOffer. It returns a nil
// *MatchedOffer (with nil error) when the tx isn't a swap offer or no
// configured market matches; a non-nil error means an unexpected failure that
// should be reported.
func (p *plugin) decode(ctx context.Context, tx *psbt.Packet) (*MatchedOffer, error) {
	if tx == nil || tx.UnsignedTx == nil {
		return nil, nil
	}
	ext, err := extension.NewExtensionFromTx(tx.UnsignedTx)
	if err != nil {
		if errors.Is(err, extension.ErrExtensionNotFound) {
			return nil, nil
		}
		return nil, err
	}
	offer, err := NewOfferFromExtension(tx.UnsignedTx, ext)
	if err != nil {
		return nil, err
	}
	if offer == nil {
		return nil, nil
	}
	if p.markets == nil {
		return nil, nil
	}
	markets, err := p.markets.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list markets: %w", err)
	}
	market, dir := findMatchingMarket(markets, offer)
	if market == nil {
		return nil, nil
	}
	return &MatchedOffer{Offer: offer, Market: market, Direction: dir}, nil
}

// checkPriceTolerance rejects offers whose price deviates more than the
// market's slippage from the feed. It returns the rejection reason, or "" when
// the offer passes. Logs (Warn) when the price feed is stale.
func (p *plugin) checkPriceTolerance(ctx context.Context, m *MatchedOffer) (string, error) {
	feedPrice, err := p.prices.get(ctx, m.Market.PriceFeed)
	if err != nil && feedPrice == 0 {
		return fmt.Sprintf("price feed unavailable: %s", err), nil
	}
	if err != nil {
		p.log.WithError(err).Warn("using stale price feed")
	}
	offerPrice, ok := m.Market.ComputePrice(m.Offer.DepositAmount, m.Offer.WantAmount, m.Direction)
	if !ok {
		return "offer price is not computable", nil
	}
	if !validatePrice(offerPrice, feedPrice, m.Market.EffectiveSlippageBps(), m.Direction) {
		return fmt.Sprintf(
			"offer price %g outside %d bps tolerance of feed price %g",
			offerPrice, m.Market.EffectiveSlippageBps(), feedPrice,
		), nil
	}
	return "", nil
}

// checkBTCBalance ensures we hold enough offchain BTC to honor a BTC-deposit
// offer. Asset deposits skip this check. It returns the rejection reason, or
// "" when the offer passes.
func (p *plugin) checkBTCBalance(ctx context.Context, m *MatchedOffer) (string, error) {
	if m.Offer.WantAsset != nil {
		return "", nil
	}
	bal, err := p.arkClient.Balance(ctx)
	if err != nil {
		return "", fmt.Errorf("get balance: %w", err)
	}
	if bal.OffchainBalance.Total < m.Offer.WantAmount {
		return fmt.Sprintf(
			"insufficient offchain balance: have %d want %d",
			bal.OffchainBalance.Total, m.Offer.WantAmount,
		), nil
	}
	return "", nil
}

// fulfill is the terminal action — atomically settles the matched offer.
func (p *plugin) fulfill(ctx context.Context, m *MatchedOffer) {
	result, err := contract.FulfillOffer(ctx, m.Offer.Offer, p.arkClient, p.emulator)
	if err != nil {
		p.notify(ctx, m, "", fmt.Sprintf("fulfillment failed: %s", err))
		return
	}
	p.notify(ctx, m, result.ArkTxid, "")
}

// notify reports the outcome of an attempt on a matched offer to the
// AttemptListener if one is configured. reason is empty on success.
func (p *plugin) notify(ctx context.Context, m *MatchedOffer, fulfillTxid, reason string) {
	if reason != "" {
		p.log.WithField("offerTxid", m.Offer.FundingTxid).
			Warnf("offer not fulfilled: %s", reason)
	}
	if p.listener == nil {
		return
	}
	p.listener.OnAttempt(ctx, FulfillmentAttempt{
		Market:        m.Market.ID(),
		DepositAsset:  m.Offer.DepositAssetStr(),
		DepositAmount: m.Offer.DepositAmount,
		WantAsset:     m.Offer.WantAssetStr(),
		WantAmount:    m.Offer.WantAmount,
		OfferTxid:     m.Offer.FundingTxid,
		FulfillTxid:   fulfillTxid,
		Error:         reason,
		Timestamp:     time.Now().UTC(),
	})
}

// findMatchingMarket returns the first market (and direction) the offer matches.
func findMatchingMarket(markets []Market, o *Offer) (*Market, Direction) {
	deposit := o.DepositAssetStr()
	want := o.WantAssetStr()
	for i := range markets {
		if dir := markets[i].Match(deposit, want, o.WantAmount); dir != NoMatch {
			return &markets[i], dir
		}
	}
	return nil, NoMatch
}
