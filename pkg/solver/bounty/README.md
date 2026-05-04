# `pkg/solver/bounty` — PoW-gated payment bounty plugin

A reference solver plugin shipped inside the toolkit. Originally drafted as a "fake HTLC" sandbox; pivoted into a CPU-lottery payment-routing primitive.

## What it does

1. **Alice posts a bounty.** She picks a `(difficulty, receiverPkScript, amount)` and funds a single VTXO at a deterministic address. Inside the same funding tx, an OP_RETURN extension carries an `Announcement` packet (`difficulty + receiverPkScript`).

2. **The bot watches the arkd stream.** On every tx, it parses the extension, decodes the announcement, reconstructs the VTXO script, and verifies a spendable VTXO exists at the computed address.

3. **The bot batches.** Discovered bounties land in a per-difficulty bucket; flush triggers on `BatchSize` entries or `BatchTimeout` elapsed.

4. **The bot mines.** Each batch becomes a single claim tx whose extension OP_RETURN carries an introspector packet (one entry per bounty input) plus a `MiningNonce` packet. The bot iterates the nonce until the tx's own txid begins with `difficulty` zero bytes.

5. **The introspector enforces.** For each input, an arkade enforcement script asserts:
   - `OP_TXID[:difficulty] == 0…0` (PoW)
   - `output[currentInputIndex].pkScript == receiverPkScript`
   - `output[currentInputIndex].value == input.value - MinerFeeSats`
   
   When all entries pass, the introspector signs its half of every input. arkd signs its half. Tx finalizes.

The receiver gets `amount - MinerFeeSats` sats, the bot keeps `MinerFeeSats * batch_size` sats as an aggregated tip, and one mined txid amortizes the PoW across the whole batch.

`MinerFeeSats` is pinned to the Ark dust limit (`330`) — that's the smallest fee that keeps a singleton claim's tip output non-dust under arkd's standardness rules. Batched claims (N > 1) yield a `N * 330`-sat tip that scales linearly.

## Closure

Single closure per bounty VTXO — no `Condition`, no refund, no exit. The introspector's tweaked key is the entire gate.

```go
&script.MultisigClosure{
    PubKeys: []*btcec.PublicKey{
        serverPubKey,
        arkade.ComputeArkadeScriptPublicKey(introspectorPubKey, ArkadeScriptHash(payToAndPoW)),
    },
}
```

## Wire format

Both packets live inside the standard Ark extension OP_RETURN.

**`Announcement` (Alice's funding tx, packet type `0x10`):**

| tag | name | size |
|---|---|---|
| `0x01` | `difficulty` | 1 |
| `0x02` | `receiverPkScript` | 34 |

**`MiningNonce` (bot's claim tx, packet type `0x11`):**

| tag | name | size |
|---|---|---|
| `0x01` | `nonce` | 8 |

## API surface

```go
// Maker (Alice).
bounty.CreateBounty(ctx, params, arkClient) (*CreateResult, error)

// Bot.
bounty.NewPlugin(ctx, cfg) (solver.Plugin, error)

// Direct claim (test/CLI hook).
bounty.BuildBatchClaim(batch, difficulty, takerPkScript, checkpointTapscript) (*psbt.Packet, []*psbt.Packet, ext, idx, err)
bounty.SubmitBatchClaim(ctx, arkClient, introClient, arkTx, checkpoints) (txid, err)

// Mining helper (pure, no I/O).
bounty.Mine(ctx, tx, baseExt, extOutputIdx, difficulty) ([]byte, error)
```

## Configuration via the daemon

`cmd/bancod` opts in via:

| env var | default | meaning |
|---|---|---|
| `BANCOD_BOUNTY_ENABLED` | `false` | enable the bounty plugin |
| `BANCOD_BOUNTY_BATCH_SIZE` | `10` | per-difficulty size trigger |
| `BANCOD_BOUNTY_BATCH_TIMEOUT` | `5s` | time trigger |

When enabled, the daemon constructs a `BountyService` (in `internal/core/application/bounty_service.go`) that owns the plugin's solver loop and the plugin's background flush goroutine. Both shut down together when the daemon receives SIGINT/SIGTERM.

## Limitations (deliberate, v1)

- No persistence — pending bounties in the bot's buffer are dropped at shutdown.
- No retry on submit failure — a failed batch loses its survivors.
- No mixed-difficulty batching — one claim tx per difficulty.
- BTC-only — no asset support.
- No fee-tunability — `MinerFeeSats` is a protocol constant pinned to the dust limit.
- No refund path — Alice has no application-level recovery if no bot ever shows up. (She holds the entire script knowledge though, so she can self-claim.)
