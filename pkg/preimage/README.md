# `pkg/preimage` — Preimage-claim solver plugin

A `solver.Plugin` that claims preimage-gated VTXOs the moment their
funding tx is broadcast. The maker encrypts a 32-byte preimage to the
solver's ECIES key and stamps it (plus the receiver script) into the
ark OP_RETURN extension; this plugin decrypts, sweeps the VTXO, and
forwards the claim to the emulator.

## Match (Match → Decode)

A tx is picked up when **all** of the following hold:

1. It carries an ark OP_RETURN extension (pre-filter from `builder.ForExtension`).
2. The extension contains a `ClaimPacket` (`PacketType = 0x04`) via `FindClaim`.
3. `Decrypt(solverPrivKey, packet.Ciphertext)` succeeds and yields a 32-byte preimage.
4. `ValidateArkadeScript(packet.ArkadeScript)` passes — the plaintext arkade
   script must be a well-formed `enforcePayTo(receiverPk)`.
5. Some tx output has:
   - A non-empty `POutput.TaprootTapTree` (BIP-371) decodable as a `TapscriptsVtxoScript`.
   - A `ConditionMultisigClosure` whose two keys are exactly
     `(serverPubKey, emulatorTweakedKey(arkadeScript, emulatorPubKey))`.
   - A `PkScript` matching the P2TR derived from that vtxo script's taptree.

Failures at any step are silent skips (`builder.ErrSkip` or `continue`).

Produced intent: `MatchedClaim{Outpoint, Amount, Credentials{Preimage, ArkadeScript, Taptree, PkScript}}`.

## Validate gates

| Gate | Source | Reject when |
|---|---|---|
| `checkVtxoSpendable` | `arkClient.Indexer().GetVtxos(WithScripts, WithSpendableOnly)` | The funding VTXO isn't yet (or no longer) spendable for our script |

Single gate by design — match already encodes most of the eligibility.

## Solve

1. `BuildClaim(matched, checkpointTapscript, serverPubKey, emulatorPubKey)`
   constructs the unsigned Ark tx + checkpoint(s):
   - Receiver output `(matched.Amount, receiverPkScript)` from the arkade script.
   - Emulator extension output carrying the plaintext arkade script.
   - Single VTXO input revealing the `ConditionMultisigClosure` script + control block.
   - `ConditionWitnessField` set to the decrypted preimage on the input
     of both ark tx and every checkpoint.
2. `SubmitClaim(ctx, arkClient, emulator, arkTx, checkpoints)` b64-signs
   each PSBT via `arkClient.SignTransaction` and forwards to the
   emulator's `SubmitTx`. Returns the finalized ark txid.

Build errors are logged at Warn and the claim is dropped — Solve does
not retry.

## Filter

`Filter()` returns `""` — no server-side CEL filter. Move to a tag-scoped
expression (extension type `0x04`) once arkd's CEL grammar lands.

## Wiring

```go
plugin, err := preimage.NewPlugin(ctx, preimage.Config{
    ArkClient:           arkClient,           // arksdk.Wallet — signs + queries indexer
    Emulator:        emulatorClient,         // submits the claim bundle
    SolverPrivKey:       solverPriv,          // ECIES privkey; derived from wallet seed via HMAC-SHA256
    EmulatorPubKey:  emulatorPubkey,  // fetched from Emulator.GetInfo
    ServerPubKey:        configData.SignerPubKey,
    CheckpointTapscript: checkpointBytes,     // from arkClient.GetConfigData
    Network:             configData.Network,
    Log:                 log,
})
s := solver.New(plugin).WithLogger(log)
```

The application-level service (`internal/core/application/preimage_service.go`)
fetches the emulator/server pubkeys at startup, owns the run loop,
and exposes `SolverPubKey()` / `EmulatorPubKey()` so makers can encrypt
preimages against the right keys.

## Files quick-reference

- `plugin.go` — `NewPlugin`, decode + `checkVtxoSpendable` + `claim` wired via `builder.ForExtension`.
- `packet.go` — `ClaimPacket` TLV codec (`PacketType = 0x04`) and `FindClaim`.
- `claim.go` — `MatchedClaim`, `BuildClaim`, `SubmitClaim`, closure-search helpers.
- `contract.go` — `ValidateArkadeScript`, `emulatorTweakedKey` (taproot tweak that binds the emulator key to the receiver script).
- `crypto.go` — `Encrypt` / `Decrypt`: ECIES over secp256k1 with HKDF-SHA256 → AES-256-GCM, ephemeral compressed-pubkey prefix.
- `maker.go` — `BuildPacket` helper used by funders to encrypt a preimage to the solver and produce the matching `extension.Packet`.
- `repository.go` — defines `ClaimCredentials` (the field-for-field input contract for `BuildClaim`). Despite the filename, no persistence interface yet.
