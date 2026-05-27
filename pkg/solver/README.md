# `pkg/solver` — Ark solver runtime + plugin authoring toolkit

> Audience: this README is the canonical entry point for any agent (human or LLM) editing code under `pkg/solver/`. It is dense by design. Read it top-to-bottom before writing or modifying a plugin; every section maps to a concrete invariant the runtime relies on.

## TL;DR

A solver bot subscribes to arkd's transaction stream and reacts to txs that match a protocol it cares about. This package provides:

1. A tx source (`arkdsource`) that turns the arkd gRPC stream into `<-chan *psbt.Packet`.
2. A runtime (`solver.Solver`) that fans incoming txs out to one or more plugins, recovers panics, and waits for in-flight work on shutdown.
3. A typed plugin-authoring toolkit (`builder`) that hides the OP_RETURN parse and exposes a stage-based pipeline (Filter → Decode → Validate → Solve) for protocol-specific code.
4. A predicate library (`txmatch`) for cheap structural filters over `*psbt.Packet`.

Banco (`pkg/banco`) is the canonical consumer; read its `plugin.go` alongside this README.

## Package map

```
pkg/solver/
├── solver.go              Solver runtime: Plugin interface + Run loop
├── arkdsource/            arkd gRPC tx stream -> <-chan *psbt.Packet
├── txmatch/               Pure predicates over *psbt.Packet
└── builder/               Builder[T] + ForExtension factory
```

Dependency rule: `pkg/solver` and `pkg/solver/txmatch` MUST stay free of `arksdk` / `arkd/pkg/ark-lib`. Anything that touches arkd lives in a sibling subpackage (`arkdsource`, `builder`).

---

## Core types

### `solver.Plugin`

```go
type Plugin interface {
    Match(ctx context.Context, tx *psbt.Packet) (intent any, ok bool)
    Solve(ctx context.Context, intent any)
}
```

- `Match` decides whether a tx is relevant. Return `(intent, true)` to accept; `(_, false)` to drop.
- `Solve` reacts to an accepted `intent`. Runs in its own goroutine. May be slow.
- `intent` is `any` because the runtime is type-erased; in practice plugins use the typed `builder.Builder[T]` so the type assertion happens transparently inside `Solve`.

DO NOT implement this interface manually unless the `builder` package genuinely cannot express your plugin. Direct implementation bypasses panic recovery, type-safe Solve dispatch, and the standard pipeline ordering.

### `solver.Solver`

```go
type Solver struct { /* ... */ }

func New(plugins ...Plugin) *Solver
func (s *Solver) WithLogger(log logrus.FieldLogger) *Solver
func (s *Solver) Run(ctx context.Context, txs <-chan *psbt.Packet) error
```

`Run` semantics:

- Reads txs sequentially.
- For every tx, calls each plugin's `Match` in registration order.
- Every accepted tx spawns `go p.Solve(ctx, intent)` — Solves run concurrently, but Match calls are sequential.
- Panics in `Match` and `Solve` are recovered and reported via the configured logger (Error level with stack). The solver does NOT crash on plugin panics.
- `Run` only returns after all in-flight Solve goroutines finish. Two exit conditions:
  - `txs` channel closes → returns `nil` (after drain)
  - `ctx` cancels → returns `ctx.Err()` (after drain)
- `WithLogger(nil)` disables panic logging entirely (panics still recovered, just silent). Default is `logrus.StandardLogger()`.

### `arkdsource.Subscribe`

```go
func Subscribe(ctx context.Context, c arksdk.ArkClient, log logrus.FieldLogger) <-chan *psbt.Packet
```

Returns a channel that closes when:
- ctx cancels
- the upstream gRPC stream errors at subscribe time
- the upstream stream closes

Per-event decode errors are logged at Warn and skipped — the consumer only ever sees successfully-parsed packets. `nil` log falls back to the standard logger.

---

## Authoring a plugin (canonical recipe)

The 90% case: your protocol stamps a packet into the ark OP_RETURN extension. Use `builder.ForExtension`. The framework handles the cheap "has ark extension" pre-filter and the OP_RETURN parse; your decoder receives the parsed `extension.Extension` and pulls out whatever TLVs it cares about.

### Pattern A — extension-based plugin (banco's case, the common path)

```go
import (
    "github.com/arkade-os/arkd/pkg/ark-lib/extension"
    "github.com/arkade-os/solver/pkg/solver"
    "github.com/arkade-os/solver/pkg/solver/builder"
)

type Intent struct { /* typed fields */ }

func NewPlugin(...) solver.Plugin {
    p := &myPlugin{...}
    return builder.ForExtension(p.decode).
        Validate(p.checkX).
        Validate(p.checkY).
        Solve(p.fulfill).
        WithLogger(p.log).
        Build()
}

func (p *myPlugin) decode(ctx context.Context, tx *psbt.Packet, ext extension.Extension) (*Intent, error) {
    payload := myproto.Find(ext)            // your TLV (or ext.GetPacketByType(tag))
    if payload == nil {
        return nil, builder.ErrSkip
    }
    asset := ext.GetAssetPacket()           // sibling TLVs available too
    // build *Intent; return builder.ErrSkip if not for us.
}
```

If the tx has no extension, the framework skips it before your decoder is invoked. Single-TLV plugins call `ext.GetPacketByType(tag)` inside `decode`; multi-TLV plugins (banco) read multiple sibling packets from the same `ext`.

### Pattern B — non-extension plugin (rare)

For plugins that don't read the OP_RETURN extension at all (e.g. address-watching, HTLC pattern matching), use the bare `builder.For[T]` and supply your own filters:

```go
return builder.For[*Intent]().
    Filter(txmatch.HasOutput(txmatch.OutputScript(myAddr))).
    Decode(p.decode).
    Solve(p.fulfill).
    Build()
```

---

## Pipeline semantics

Every Match call runs this sequence:

```
filters... (AND, short-circuit on first false)
   ↓
decode      (T or error)
   ↓
validators  (in order, short-circuit on (false, nil) or any error)
   ↓
intent passed to runtime; Solve dispatched in its own goroutine.
```

### Stage rules

- **Filter** (`txmatch.Predicate`): pure boolean, NO I/O. Cheap. Multiple filters are AND-composed in registration order. They run for every tx in the stream — keep them fast.
- **Decode** (`func(ctx, tx) (T, error)`): the typed-extraction stage. Required.
  - Return `(zero, builder.ErrSkip)` to silently drop (this tx isn't yours).
  - Return `(zero, otherErr)` to drop with a Debug log.
  - Return `(intent, nil)` to proceed.
  - The factory entry point `ForExtension` pre-loads this stage with the OP_RETURN parse; you supply only the protocol-specific decoder that consumes the parsed extension.
- **Validate** (`func(ctx, T) (bool, error)`): zero or more typed gates over the intent.
  - `(true, nil)` — proceed
  - `(false, nil)` — silently drop (intent is well-formed but doesn't pass policy)
  - `(_, err)` — drop with a Debug log; `ErrSkip` is also recognized as silent
  - Validators run in registration order. Order them by cost: cheapest first.
- **Solve** (`func(ctx, T)`): terminal action. Required. Runs in its own goroutine. Errors are NOT propagated — log them yourself.

### Error sentinel

```go
var builder.ErrSkip = errors.New("builder: skip")
```

Use this whenever "this tx isn't relevant" is the right answer. It produces no log noise. Any non-`ErrSkip` error is logged at Debug (when a logger is configured) and treated as a skip.

### When Build panics

`Build()` panics with a clear message if `Decode` or `Solve` is missing. These are programmer errors and should fail loudly at startup, not silently produce a no-op plugin.

---

## Predicates (`txmatch`)

Pure functions `Predicate = func(*psbt.Packet) bool`. All constructors return false on `nil` packet or `nil` `UnsignedTx` — safe to call in any context.

### Composition

```go
txmatch.All(p1, p2, p3)   // AND; empty list = true
txmatch.Any(p1, p2, p3)   // OR;  empty list = false
txmatch.Not(p)
txmatch.Always(true|false)
```

### Tx-shape predicates

```go
txmatch.HasOutputCount(n)
txmatch.HasInputCount(n)
txmatch.HasLockTime(lt)
txmatch.HasUnknownGlobalField(key []byte)   // PSBT global unknown KV
```

### Output predicates (functional options)

```go
txmatch.HasOutput(opts...)          // any output passes (or the indexed one if OutputIndex is set)

// Options:
txmatch.OutputIndex(i)              // restrict to one specific index
txmatch.OutputAmount(sat)           // exact match
txmatch.OutputAmountAtLeast(min)
txmatch.OutputAmountAtMost(max)
txmatch.OutputAmountInRange(min, max)
txmatch.OutputScript(scriptBytes)   // exact match (defensively copied)
txmatch.OutputScriptMatching(func([]byte) bool)
```

### Input predicates

```go
txmatch.HasInput(opts...)
txmatch.InputIndex(i)
txmatch.InputSpending(wire.OutPoint{...})
txmatch.InputSequence(seq)
```

### Curated arkd-aware predicate

```go
builder.HasArkExtension()           // any output's pkScript is an ark extension OP_RETURN
```

Cheap — one `extension.IsExtension(script)` call per output, no full deserialize. Pre-loaded by `ForExtension`; you don't normally need to add it manually.

---

## Wiring example (cmd/solverd/main.go)

Reference shape — every solver bot looks like this:

```go
arkClient    := /* arksdk client */
emulator := /* emulator client */

plugin := banco.NewPlugin(banco.Config{ ... })   // returns solver.Plugin
s      := solver.New(plugin).WithLogger(log)

ctx, cancel := context.WithCancel(...)
txs := arkdsource.Subscribe(ctx, arkClient, log) // <-chan *psbt.Packet

go func() {
    if err := s.Run(ctx, txs); err != nil && !errors.Is(err, context.Canceled) {
        log.WithError(err).Error("solver run exited")
    }
}()

// later: cancel() and wait. Run drains in-flight Solves before returning.
```

`solver.Solver` accepts multiple plugins (`solver.New(p1, p2, p3)`); every plugin sees every tx, in registration order.

---

## Invariants the runtime guarantees

1. **`Match` is called sequentially** for each tx, in plugin-registration order. You can rely on Match not being called concurrently for the same plugin.
2. **`Solve` is called concurrently** with other Solves (potentially across plugins). If your Solve mutates shared state, you own the synchronization.
3. **Panic recovery is unconditional** — `Match` or `Solve` panicking will not crash the bot. The panic is logged with stack and the tx is dropped.
4. **Graceful shutdown drains in-flight Solves** — `Solver.Run` does not return until every Solve goroutine it dispatched has completed. Cancel ctx and wait.
5. **`nil` txs are silently skipped** — the source can emit them defensively, the runtime ignores them before Match is called.
6. **`ErrSkip` is silent** — every other non-nil error is logged at Debug. Use ErrSkip for "not for me", reserve other errors for "something unexpected went wrong."

## Common pitfalls

- **Putting I/O in a Filter.** Filters run on every tx in the stream. A Filter doing an RPC call will throttle the entire bot. Move I/O to Decode or Validate.
- **Returning `(false, err)` from a validator when you mean "silent skip".** That logs an error at Debug. Return `(false, nil)` or `(_, builder.ErrSkip)` instead.
- **Forgetting that Solve runs in a goroutine.** Don't expect ordering across Solves. Don't share mutable state without locks. A long-running Solve does not block Match.
- **Implementing `solver.Plugin` directly.** Loses panic recovery's typed safety, makes pipeline ordering implicit, makes validators non-reusable. Use `builder` unless you have a concrete reason not to.
- **Using a `txmatch` Predicate in a context where you also need to extract the matched piece.** Predicates are boolean only — they tell you "yes" or "no", not "here's the matched output." If you need extraction, do the lookup inside Decode where you can return a typed value.
- **Reusing a `*psbt.Packet` across goroutines and mutating it.** The runtime fans the pointer to multiple plugins. Treat it as read-only.
- **Modifying `Solver` to support per-tx state passing.** Tempting for shared extension parsing. Don't — see "Future directions" below.

## Future directions (not built yet — flag if you're touching these)

- **Per-tx context caching for parsed extensions.** When N plugins all use `ForExtension`, the OP_RETURN is parsed N times per tx. The fix is a context-attached cache keyed by tx; preserve API compatibility by hiding it inside `builder` factories. Implement when there are 3+ ark plugins.
- **Lifecycle / outpoint dispatch.** "Notify me when this VTXO is spent" or "fire when an HTLC reaches its timelock" — orthogonal to TLV-based dispatch. Different package (`pkg/solver/lifecycle`?), don't shoehorn into `builder`.
- **Type-discriminated handlers.** `builder.For[T extension.Packet](decode)` where the tag is recovered from `T.Type()`. Only worth it if banco's typed payloads adopt the `extension.Packet` interface.

## Pattern lineage

The `builder` pipeline is a degenerate case of **Railway-Oriented Programming** / Kleisli composition over Result. We deliberately did NOT build a full Step/Then combinator library — Go's generics make N-ary type-evolving composition syntactically expensive, and the compositional payoff (reusable parser fragments shared across plugins) hasn't materialized in practice. If you're tempted to add a `pipeline` package, first verify there's a real cross-plugin reuse case; until then, the Builder shape is the right cut.

---

## Files quick-reference

- `solver.go` — `Plugin`, `Solver`, `Run`, panic recovery, graceful shutdown.
- `arkdsource/arkdsource.go` — `Subscribe(ctx, ArkClient, log) <-chan *psbt.Packet`.
- `txmatch/txmatch.go` — `Predicate` + `All/Any/Not` + `HasOutput/HasInput/Has*` + functional options.
- `builder/builder.go` — `Builder[T]`, stages, `ErrSkip`, `Build()`.
- `builder/extension.go` — `ForExtension[T]`, `HasArkExtension()`.

Tests for each package live alongside the source. Read the test file before changing public behavior — the contracts are encoded there.
