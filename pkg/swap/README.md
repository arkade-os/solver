# `pkg/swap` — Swap solver plugin

A `solver.Plugin` that fulfills swap offers stamped into the ark
OP_RETURN extension of broadcast Ark txs. The taker (this bot) deposits
its asset into the offer's swap output, atomically receiving the maker's
deposit in return.

## Match (Match → Decode)

A tx is picked up when **all** of the following hold:

1. It carries an ark OP_RETURN extension (pre-filter from `builder.ForExtension`).
2. The extension contains a swap offer TLV packet (`contract.PacketType = 0x03`)
   parsed by `contract.FindSwapOffer` / `NewOfferFromExtension`.
3. A tx output's `PkScript` matches the offer's declared `SwapPkScript`
   (locates the swap output and its value).
4. A configured `Market` in `MarketsRepository` matches the offer's
   `(DepositAsset, WantAsset)` in one of its two directions, and the want
   amount is within that direction's bounds (`Market.Match`). A `Market`
   is bidirectional: sell-base (deposit base, want quote — gated by
   `Min/MaxQuoteAmount`) or buy-base (deposit quote, want base — gated by
   `Min/MaxBaseAmount`). A direction with `Max*Amount == 0` is disabled.

Failures at any step return `builder.ErrSkip` — silent drop.

Produced intent: `MatchedOffer{Offer *Offer, Market *Market, Direction Direction}`.

## Validate gates

Order matches `plugin.go` (cheapest first). The amount range check is folded
into `Market.Match` during decode, so a matched offer is already in-range.

| Gate | Source | Reject when |
|---|---|---|
| `checkPriceTolerance` | price feed (`PriceFeed`) | offer price (quote-per-base) deviates more than `ToleranceBps` (default 10 = 0.1%) from the feed; stale feed logs Warn but does not auto-reject |
| `checkBTCBalance` | `arkClient.Balance` | want side is BTC and offchain balance < `WantAmount`; skipped if want is a non-BTC asset |

All gates return `(false, nil)` for silent drops — they reject by policy,
not by error.

## Solve

`contract.FulfillOffer(ctx, offer, arkClient, emulator)` atomically:

1. Constructs the taker's Ark tx that spends into the offer's
   `SwapPkScript` (the maker's receive output) and emits the taker's
   matching outputs.
2. Signs and submits via the emulator. On success, returns
   `result.ArkTxid`.
3. If a `FulfillmentListener` is configured, emits a `FulfillmentEvent`
   with both txids and the resolved market ID — `TradeListener` persists
   this to `trades`.

Solve runs in its own goroutine (per `solver.Solver` contract) — Solve
errors are logged, not propagated.

## Filter

`Filter()` returns `""` — no server-side CEL filter. The full tx stream
is consumed and `Match` does the discrimination. Switch to a tag-scoped
CEL expression (e.g. extension type `0x03`) once arkd's filter grammar
lands.

## Wiring

```go
plugin := swap.NewPlugin(swap.Config{
    SolverClient:      arkClient,     // arksdk.Wallet — signs + holds funds
    Emulator:          emulatorClient, // submits fulfilled bundles
    MarketsRepository: marketRepo,     // configured markets (CRUD via TakerService)
    PriceFeed:         priceFeed,      // CoinGecko-backed by default
    Listener:          tradeListener,  // persists FulfillmentEvent → trades table
    Log:               log,
})
plugins = append(plugins, plugin)
```

The application-level service (`internal/core/application/swap_service.go`)
exposes market/trade CRUD; `cmd/solverd` wires the plugin into the shared solver
runtime. `pkg/swap` only owns match/validate/solve.

## Files quick-reference

- `plugin.go` — `NewPlugin`, decode + validators + fulfill wired via `builder.ForExtension`.
- `offer.go` — `Offer` struct + `NewOfferFromExtension` (locates swap output, resolves deposit asset).
- `contract/offer.go` — TLV codec for the offer packet (`PacketType = 0x03`) and `FindSwapOffer`.
- `contract/maker.go` / `contract/taker.go` — offer construction + fulfillment.
- `market.go` — `Market` (bidirectional base/quote market), `Direction`, `Match`, `ComputePrice` + repository contract.
- `price.go` — `PriceFeed` interface + cached lookup with tolerance check.
- `events.go` — `FulfillmentEvent` + `FulfillmentListener`.
- `config.go` — `Config` + defaults.
