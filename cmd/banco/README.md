# banco

A small CLI that performs the **maker** side of a single swap against a solver
market: you deposit one asset and receive the other.

> [!WARNING]
> **Experimental, test-only tooling. Use at your own risk.**
> banco spins up a throwaway wallet and moves real funds through it. There are
> no guarantees. If the process dies mid-swap, the **recovery key** it prints at
> startup is the only way to get your funds back — keep it until the run
> finishes. Do not use amounts you are not prepared to lose.

## How it works

1. banco creates an ephemeral wallet and prints a **recovery key** and a
   **deposit address** (also as a QR code).
2. You send the asset you want to *spend* to that address.
3. banco prices the swap from the feed, publishes an offer, and waits for a
   taker (the solver) to fulfill it.
4. Once fulfilled, it forwards the received asset to your `--payout` address so
   nothing is left in the ephemeral wallet.

## Build

```
go build -o banco ./cmd/banco
```

## Direction: market + side (same model as the solver)

`--market {base}/{quote}` is the market in its **canonical form, exactly as the
solver lists it** (each side `BTC` or a hex asset ID). `--side` then picks which
way you trade it:

- `--side sell` → **deposit base, receive quote** (sell the base asset).
- `--side buy`  → **deposit quote, receive base** (buy the base asset).

So for a market `BTC/<asset>`:

- Sell BTC for the asset: `--market BTC/<asset> --side sell`.
- Buy BTC with the asset: `--market BTC/<asset> --side buy`.

You never reorder the pair — keep it canonical and flip `--side`.

## Price and fee

- `--price-feed` / `--price-path`: the same feed URL and JSON pointer the market
  card advertises. It is always read as **quote-per-base** for the canonical
  pair — the same convention the solver uses. banco orients it for your `--side`
  itself, so there is no invert flag.
- `--fee-bps`: set this to the market's `fee_bps`. banco trims the requested
  amount by the fee so the offer clears the solver's price check. **Omitting it
  when the market charges a fee will get your offer rejected.**

## Flags

| Flag | Required | Default | Purpose |
|------|----------|---------|---------|
| `--market` | yes | — | canonical `{base}/{quote}`, as the solver lists it |
| `--side` | yes | — | `sell` (deposit base) or `buy` (deposit quote) |
| `--price-feed` | yes | — | price feed URL, read as quote-per-base |
| `--price-path` | no | guess from host | JSON pointer to the price, e.g. `/bitcoin/usd` |
| `--fee-bps` | no | 0 | market maker fee in bps; trims the ask so it clears |
| `--payout` | yes | — | offchain address that receives the result |
| `--arkd` | no | `localhost:7070` | arkd server address |
| `--emulator` | no | `localhost:7173` | emulator address; `host:port` = gRPC, `http(s)://` = REST |
| `--count` | no | 1 | run N swaps in parallel, splitting the deposit evenly |
| `--timeout` | no | 0 (forever) | give up waiting for a taker after this long |

## Example

Buy BTC with `<asset>` on a `BTC/<asset>` market:

```
./banco \
  --market BTC/<asset-id> \
  --side buy \
  --price-feed "https://api.coingecko.com/api/v3/simple/price?ids=bitcoin&vs_currencies=usd" \
  --price-path /bitcoin/usd \
  --fee-bps 30 \
  --arkd https://<your-arkd-host> \
  --emulator https://<your-emulator-host> \
  --timeout 20m \
  --payout <your-ark-address>
```

Then send your `<asset>` to the printed address. banco offers it for BTC and
forwards the BTC to `--payout`.

## Recovery & failures

- **Recovery key** (printed at startup): the master fallback. If banco dies, or
  a forward fails, import this key into a wallet to recover everything.
- **Unfulfilled after `--timeout`**: the deposit sits at the swap address. banco
  prints the address and offer hex needed to reclaim it with the recovery key.
- **`--count N`**: all payouts are swept to `--payout` in one send after every
  swap finishes; any leftover from a failed swap is returned there too.
