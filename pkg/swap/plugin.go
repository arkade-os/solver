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
		prices:    newPriceCache(),
		listener:  cfg.Listener,
		log:       cfg.Log,
	}
}

// Filter narrows the subscription server-side to txs carrying a swap offer
// packet; Match still re-parses and validates.
func (p *plugin) Filter() string {
	return fmt.Sprintf("has(tx.extension) && hasPacket(tx.extension, %d)", contract.PacketType)
}

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
		p.checkPriceTolerance, p.checkBalance,
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
	feedPrice, err := p.prices.get(
		ctx, m.Market.PriceFeed, m.Market.PricePath, m.Market.EffectivePriceTTL(),
	)
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

// checkBalance ensures we hold enough of the wanted asset offchain to honor the
// offer — BTC from OffchainBalance, any other asset from AssetBalances. It
// returns the rejection reason, or "" when the offer passes.
func (p *plugin) checkBalance(ctx context.Context, m *MatchedOffer) (string, error) {
	bal, err := p.arkClient.Balance(ctx)
	if err != nil {
		return "", fmt.Errorf("get balance: %w", err)
	}
	want := m.Offer.WantAmount

	if m.Offer.WantAsset == nil {
		if bal.OffchainBalance.Total < want {
			return fmt.Sprintf(
				"insufficient offchain BTC balance: have %d want %d",
				bal.OffchainBalance.Total, want,
			), nil
		}
		return "", nil
	}

	assetID := m.Offer.WantAssetStr()
	have := bal.AssetBalances[assetID]
	if have < want {
		return fmt.Sprintf(
			"insufficient %s balance: have %d want %d", assetID, have, want,
		), nil
	}
	return "", nil
}

func (p *plugin) fulfill(ctx context.Context, m *MatchedOffer) {
	takerAddr, err := p.arkClient.NewOffchainAddress(ctx)
	if err != nil {
		p.notify(ctx, m, "", fmt.Sprintf("fulfillment failed: get taker address: %s", err))
		return
	}

	res, err := p.arkClient.TxHandler().HandleTx(func() (any, error) {
		subCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		tracked := make(chan error, 1)
		go func() {
			_, err := p.arkClient.NotifyIncomingFunds(subCtx, takerAddr)
			if err == nil {
				time.Sleep(20 * time.Millisecond) // give time for go-sdk db to update
			}
			tracked <- err
		}()

		res, err := contract.FulfillOffer(
			ctx, m.Offer.Offer, takerAddr, p.arkClient, p.emulator,
		)
		if err != nil {
			return nil, err
		}

		select {
		case err := <-tracked:
			if err != nil {
				return nil, err
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return res, nil
	})
	if err != nil {
		p.notify(ctx, m, "", fmt.Sprintf("fulfillment failed: %s", err))
		return
	}

	if r, ok := res.(*contract.FulfillResult); ok {
		p.notify(ctx, m, r.ArkTxid, "")
		return
	}

	p.log.Warnf("unexpected fullfilment return type %T: skip notification", res)
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
