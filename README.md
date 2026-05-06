# bancod

`bancod` is a Go implementation of a **banco solver bot** for the [Arkade](https://arkadeos.com/) virtual mempool.

A *maker* posts a swap offer as a VTXO on an Ark network. The solver bot watches the arkd transaction
stream, finds offers that match its configured pairs and price ranges, and fulfills them atomically
via an introspector-signed Ark transaction.

## Packages

### `pkg/contract`

Wire-protocol primitives for the banco swap.

- `Offer` — typed banco swap offer, encoded as a TLV payload inside an Ark
  extension packet (`PacketType = 0x03`). Methods: `Serialize`, `ToPacket`,
  `FulfillScript`, and `VtxoScript` (builds the swap taproot tree from the
  maker, introspector, and signer keys).
- `DeserializeOffer` / `FindBancoOffer` — decode an offer from raw bytes or
  pull one out of an Ark extension.
- `CreateOffer` — maker-side helper: queries the introspector for its signer
  key, derives the maker address from the ark client, assembles an `Offer`,
  and returns the hex-encoded offer + extension packet + swap address to
  fund (`CreateOfferParams` / `CreateOfferResult`).
- `GetOffers` — queries the indexer for VTXOs sitting at a swap address, used
  by a maker to check whether its offer is still live (`[]OfferStatus`).
- `FulfillOffer` — taker-side atomic swap: builds the Ark transaction that
  spends the swap VTXO to the maker's pkScript (paying `WantAmount`/`WantAsset`)
  and returns change to the taker, signs it with the introspector, and submits
  it (`FulfillResult`).

### `pkg/solver`

Generic plugin-based solver runtime. Consumes a stream of PSBT packets and
dispatches each one to its registered plugins.

- `Plugin` interface — `Match(ctx, *psbt.Packet) (intent any, ok bool)` decides
  whether a tx is interesting; `Solve(ctx, intent)` reacts to a match.
- `Solver` / `New(plugins ...Plugin)` — runtime wrapping one or more plugins.
- `Run(ctx, <-chan *psbt.Packet) error` — drains the channel sequentially,
  fans matches out to `Solve` goroutines. Returns `ctx.Err()` on cancel,
  `nil` when the channel closes.

### `pkg/banco`

The banco-specific solver plugin and its supporting types — the building block
for a taker bot.

- `Plugin` / `NewPlugin(Config)` — implements `solver.Plugin` for the banco
  swap protocol: decodes the offer from a tx, looks up a matching configured
  pair, range-checks `WantAmount`, validates price within 1% of the feed, and
  fulfills via `contract.FulfillOffer`.
- `Config` — dependencies: `arksdk.ArkClient`, introspector client,
  `PairRepository`, `PriceFeed`, optional `FulfillmentListener`, optional
  `logrus.FieldLogger`, and `PriceCacheTTL` (default 5 minutes).
- `Offer` / `NewOffer(*wire.MsgTx)` — wraps `contract.Offer` with `FundingTxid`,
  `DepositAsset`, and `DepositAmount` extracted from the funding tx. Helpers:
  `IsBTCDeposit`, `DepositAssetStr`, `WantAssetStr`, `ComputePrice`.
- `Pair` / `PairRepository` — trading pair definition (`base/quote`, min/max
  amount, decimals, price-feed URL, invert flag) and the read-only repository
  interface used by the plugin.
- `PriceFeed` — pluggable price source; the plugin wraps it in an internal
  TTL cache.
- `FulfillmentEvent` / `FulfillmentListener` — emitted after every successful
  fulfillment; the daemon wires a listener that persists trades to SQLite.
- `SubscribeArkd` — helper that returns a `<-chan *psbt.Packet` from arkd's
  transaction stream, suitable for feeding into `Solver.Run`.

### `pkg/preimage`

A second solver plugin: preimage-gated VTXO claims. A maker registers
`(preimage, arkade_script, taptree)` via the `PreimageService` gRPC API; the
bot computes the claim address, stores the credentials in SQLite (with an
in-memory cache for the hot path), watches arkd's tx stream, and auto-claims
the moment a tx funds the registered address. v1 only supports the
`enforcePayTo` arkade-script shape (single-output, full-amount-to-receiver).

All `pkg/` packages are intended to be importable by other projects and do not
depend on any `internal/` code.

---

## Binaries

### `bancod`

Daemon that boots a solver, a SQLite-backed wallet, the gRPC+REST API, and the
web UI. Configured entirely through environment variables:

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `BANCOD_ARK_URL` | ✓ | — | arkd gRPC endpoint |
| `BANCOD_WALLET_SEED` | ✓ | — | wallet seed (hex) |
| `BANCOD_INTROSPECTOR_URL` | ✓ | — | introspector endpoint |
| `BANCOD_WALLET_PASSWORD` | | — | wallet unlock password |
| `BANCOD_DATADIR` | | `$HOME/.bancod` | data directory (SQLite DB lives here) |
| `BANCOD_GRPC_PORT` | | `7070` | gRPC listener |
| `BANCOD_HTTP_PORT` | | `7071` | HTTP REST + web UI listener |
| `BANCOD_LOG_LEVEL` | | `4` (Info) | logrus level |
| `BANCOD_BANCO_ENABLED` | | `true` | enable the banco swap plugin |
| `BANCOD_PREIMAGE_ENABLED` | | `false` | enable the preimage-claim plugin |

At least one plugin must be enabled. Each enabled plugin owns its own solver
and arkd subscription.

### `banco`

CLI client for the HTTP API. Points at `http://localhost:7071` by default
(`--server` or `BANCO_SERVER` to override). Commands:

```
banco pair add     --pair BTC/<asset> --min … --max … --price-feed …
banco pair update  …
banco pair remove  --pair …
banco pair list
banco status
banco balance
banco address
```

## Building

```sh
make build          # builds ./bancod and ./banco
make docker         # builds the bancod image
make proto          # regenerates api-spec/protobuf/gen
make sqlc           # regenerates internal/infrastructure/db/sqlite/sqlc
make lint
make test           # unit tests
```

## Integration tests

End-to-end tests run against a local nigiri + arkd stack:

```sh
make setup-test-env     # boot nigiri + arkd + introspector, fund arkd wallet
make integrationtest    # run ./test/e2e/...
make teardown-test-env
```

If nigiri is already running (e.g. in CI, where the `vulpemventures/nigiri-github-action`
sets it up), use `make docker-run` and `make docker-stop` instead — they bring up
the bancod-side stack and fund the arkd wallet without touching nigiri.
