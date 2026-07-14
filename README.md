# solver

A **solver bot** for [Arkade Intents](https://arkadeos.com/). It watches an
Arkade for swap offers, and automatically fills the ones that match the markets
and prices you configure.

This is a guide to setting one up. It ships two binaries:

- `solverd` — the daemon that runs the bot, wallet, and API.
- `solver` — the CLI you use to operate it (add markets, fund the wallet, check trades).

---

## 1. Before you start

You need two services reachable from wherever you run `solverd`:

- an **arkd** gRPC endpoint (the Arkade the bot trades on),
- an **emulator** endpoint (co-signs the swap transactions).

You also need a **wallet seed** — 32 bytes of hex. Generate one however you
like, e.g.:

```sh
openssl rand -hex 32
```

Keep it safe: it controls the bot's funds.

## 2. Build

With Go installed:

```sh
make build      # produces ./solverd and ./solver
```

Or run the daemon in a container:

```sh
make docker     # builds the solverd image
```

## 3. Start the daemon

`solverd` is configured entirely through environment variables:

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `SOLVER_ARK_URL` | ✓ | — | arkd gRPC endpoint |
| `SOLVER_WALLET_SEED` | ✓ | — | wallet seed (32-byte hex) |
| `SOLVER_EMULATOR_URL` | ✓ | — | emulator endpoint |
| `SOLVER_WALLET_PASSWORD` | | — | password that unlocks the wallet |
| `SOLVER_EXPLORER_URL` | | — | block explorer URL (optional) |
| `SOLVER_DATADIR` | | `$HOME/.solverd` | data directory (wallet + SQLite DB) |
| `SOLVER_GRPC_PORT` | | `7170` | gRPC listener |
| `SOLVER_HTTP_PORT` | | `7171` | HTTP API + web UI listener |
| `SOLVER_LOG_LEVEL` | | `4` (Info) | log verbosity |

```sh
SOLVER_ARK_URL=arkd.example.com:443 \
SOLVER_EMULATOR_URL=emulator.example.com:7173 \
SOLVER_WALLET_SEED=$(openssl rand -hex 32) \
SOLVER_WALLET_PASSWORD=changeme \
./solverd
```

On first run it initializes the wallet from the seed and creates the data
directory; on later runs it just unlocks and resumes. Leave it running.

## 4. Point the CLI at the daemon

The CLI talks to the daemon's HTTP API. Set it once so you don't repeat
`--server` on every command:

```sh
export SOLVER_SERVER=http://localhost:7171   # match SOLVER_HTTP_PORT
```

Check it's up:

```sh
solver status
```

## 5. Fund the wallet

Show the bot's addresses:

```sh
solver address
```

Send BTC to the **boarding (onchain)** address, then pull it into the Arkade so
it's spendable off-chain:

```sh
solver settle          # joins a batch round to confirm boarding funds
solver balance
```

To move funds around later:

```sh
solver send --to ark1... --amount 100000            # send BTC off-chain
solver send --to ark1... --amount 5000 --asset <hex asset id>
solver exit --to bc1... --amount 100000             # collaboratively exit BTC on-chain
```

Wallet-spending commands ask for the password (or read `SOLVER_PASSWORD`).

## 6. Add a market

A market tells the bot which swaps to fill and at what price. The bot only
fills offers whose price is within your tolerance of the price feed.

```sh
solver pair add \
  --pair BTC/<asset id> \
  --min 10000 \
  --max 1000000 \
  --price-feed https://feed.example.com/btc-asset \
  --slippage-bps 100        # ±1% (default)
```

- `--min` / `--max` — bounds on the **want** amount, in the quote asset's base units.
- `--price-feed` — URL the bot polls for the reference price.
- `--invert-price` — set if your feed quotes the pair the other way round.
- `--slippage-bps` — max deviation from the feed price, in basis points (100 = 1%).

That's it — with a funded wallet and at least one market, the bot is live and
will fill matching offers as they appear.

## 7. Operate it

```sh
solver pair list                     # markets you've configured
solver pair get    --pair BTC/<asset>
solver pair update --pair BTC/<asset> --max 2000000   # only the given flags change
solver pair remove --pair BTC/<asset>

solver balance                       # funds by asset
solver trades                        # fills, most recent first
solver trades --limit 20
solver status
```

Add `--json` (`-j`) to any command for raw output you can pipe into scripts.

A web UI is also served on the HTTP port (`http://localhost:7171`).

---

## Development

```sh
make run              # run solverd against the local test stack
make init-solverd     # fund it, mint a test asset, register pairs (after `make run`)
make test             # unit tests
make lint
```

End-to-end tests run against a local nigiri + arkd stack:

```sh
make setup-test-env      # boot nigiri + arkd + emulator, fund arkd wallet
make integrationtest     # run ./test/e2e/...
make teardown-test-env
```

If nigiri is already running (e.g. in CI), use `make docker-run` / `make
docker-stop` instead — they bring up the solverd-side stack without touching
nigiri.

The bot is plugin-based: each protocol it supports is a small `Plugin`. See
[`pkg/executor/README.md`](pkg/executor/README.md) for the plugin authoring guide.
