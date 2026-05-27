# solver

`solver` is a Go implementation of a **banco solver bot** for the [Arkade](https://arkadeos.com/) virtual mempool. It ships the `solverd` daemon and the `solver` CLI.

A *maker* posts a swap offer as a VTXO on an Arkade. The solver bot watches the arkd
transaction stream, finds offers that match its configured pairs and price ranges, and fulfills
them atomically via an introspector-signed Arkade transaction.

## Architecture

```
arkd tx stream  ─►  Solver  ─►  Plugin.Match(tx)  ─►  intent  ─►  Plugin.Solve(intent)
                       │
                       └── fans every tx out to all registered plugins
```

A solver bot is a small runtime that subscribes to arkd's transaction stream and
hands every PSBT it sees to one or more **plugins**. A plugin is just two
methods:

```go
type Plugin interface {
    Match(ctx, tx) (intent any, ok bool)   // is this tx relevant? extract what I need
    Solve(ctx, intent)                     // react to it
}
```

`Match` is the cheap filter+decode pass; `Solve` is the (usually slow) reaction.
The runtime (`pkg/solver`) calls `Match` sequentially for each plugin, then
spawns a goroutine for each matched `Solve`. Panics in either are recovered so
one buggy plugin can't take the bot down, and `Run` waits for in-flight solves
to drain on shutdown.

Most plugins read protocol data from the Arkade **extension** TLV in the OP_RETURN
output of the funding tx, so `pkg/solver/builder` provides a typed pipeline
(`Filter → Decode → Validate → Solve`) that hides the OP_RETURN parse. Today
two plugins ship with the daemon:

- **`pkg/banco`** — banco swap solver. Decodes a swap offer, range-checks the
  amount and price, and fulfills via the introspector.
- **`pkg/preimage`** — preimage-gated claim solver. Decrypts an ECIES payload
  attached to the funding tx and claims the VTXO when the arkade-script matches.

Each enabled plugin owns its own `Solver` and arkd subscription, so adding a
new protocol means writing a new `Plugin` and wiring it in `cmd/solverd`. See
[`pkg/solver/README.md`](pkg/solver/README.md) for the plugin authoring guide.

## Packages

### `pkg/contract`

Wire-protocol primitives for the banco swap.

- `Offer` — typed banco swap offer, encoded as a TLV payload inside an Arkade
  extension packet (`PacketType = 0x03`). Methods: `Serialize`, `ToPacket`,
  `FulfillScript`, and `VtxoScript` (builds the swap taproot tree from the
  maker, introspector, and signer keys).
- `DeserializeOffer` / `FindBancoOffer` — decode an offer from raw bytes or
  pull one out of an Arkade extension.
- `CreateOffer` — maker-side helper: queries the introspector for its signer
  key, derives the maker address from the Arkade client, assembles an `Offer`,
  and returns the hex-encoded offer + extension packet + swap address to
  fund (`CreateOfferParams` / `CreateOfferResult`).
- `GetOffers` — queries the indexer for VTXOs sitting at a swap address, used
  by a maker to check whether its offer is still live (`[]OfferStatus`).
- `FulfillOffer` — taker-side atomic swap: builds the Arkade transaction that
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

A second solver plugin: preimage-gated VTXO claims. The bot is **stateless**
— a maker fetches the bot's encryption pubkey via the `GetSolverPubKey`
RPC, ECIES-encrypts `(preimage || arkade_script)` to that key, and attaches
the ciphertext + plaintext taptree as an Arkade extension TLV packet
(`PacketType = 0x04`) on the funding tx. The bot watches arkd's tx
stream, parses the extension, decrypts on the fly, validates, and
claims. No registration, no DB, no per-claim persistence. v1 only
supports the `enforcePayTo` arkade-script shape (single-output,
full-amount-to-receiver). The maker-side helper `preimage.CreateClaim`
builds the address + packet from the local primitives.

All `pkg/` packages are intended to be importable by other projects and do not
depend on any `internal/` code.

---

## Binaries

### `solverd`

Daemon that boots a solver, a SQLite-backed wallet, the gRPC+REST API, and the
web UI. Configured entirely through environment variables:

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `SOLVER_ARK_URL` | ✓ | — | arkd gRPC endpoint |
| `SOLVER_WALLET_SEED` | ✓ | — | wallet seed (hex) |
| `SOLVER_INTROSPECTOR_URL` | ✓ | — | introspector endpoint |
| `SOLVER_WALLET_PASSWORD` | | — | wallet unlock password |
| `SOLVER_DATADIR` | | `$HOME/.solverd` | data directory (SQLite DB lives here) |
| `SOLVER_GRPC_PORT` | | `7070` | gRPC listener |
| `SOLVER_HTTP_PORT` | | `7071` | HTTP REST + web UI listener |
| `SOLVER_LOG_LEVEL` | | `4` (Info) | logrus level |
| `SOLVER_BANCO_ENABLED` | | `true` | enable the banco swap plugin |
| `SOLVER_PREIMAGE_ENABLED` | | `false` | enable the preimage-claim plugin |

At least one plugin must be enabled. Each enabled plugin owns its own solver
and arkd subscription.

### `solver`

CLI client for the HTTP API. Points at `http://localhost:7071` by default
(`--server` or `SOLVER_SERVER` to override). Commands:

```
solver pair add     --pair BTC/<asset> --min … --max … --price-feed …
solver pair update  …
solver pair remove  --pair …
solver pair list
solver status
solver balance
solver address
```

## Building

```sh
make build          # builds ./solverd and ./solver
make docker         # builds the solverd image
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
the solverd-side stack and fund the arkd wallet without touching nigiri.
