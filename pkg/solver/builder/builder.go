// Package builder is the plugin-author-facing toolkit for assembling
// solver.Plugins out of typed stages. The generic Builder[T] composes
// Filter, Decode, Validate and Solve steps with proper short-circuit
// semantics; ForExtension and ForTLV are ark-aware factory entry points
// that pre-load the structural plumbing (OP_RETURN parse + optional TLV
// scan) so plugin authors only write protocol-specific logic.
package builder

import (
	"context"
	"errors"

	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/sirupsen/logrus"

	"github.com/arkade-os/solver/pkg/solver"
	"github.com/arkade-os/solver/pkg/solver/txmatch"
)

// ErrSkip is the silent-miss sentinel. Returning ErrSkip from Decode or a
// Validate stage drops the tx without logging. Any other non-nil error is
// reported via the configured logger (at Debug) and the tx is dropped.
var ErrSkip = errors.New("builder: skip")

type (
	// DecodeFunc turns a tx into a typed intent. Return ErrSkip if the tx
	// isn't for this plugin.
	DecodeFunc[T any] func(ctx context.Context, tx *psbt.Packet) (T, error)

	// ValidateFunc gates a typed intent. Return (false, nil) for a silent
	// skip, (true, nil) to continue, or (_, err) to report and skip.
	ValidateFunc[T any] func(ctx context.Context, intent T) (bool, error)

	// SolveFunc is the terminal action invoked once all gates have passed.
	SolveFunc[T any] func(ctx context.Context, intent T)
)

// Builder assembles a solver.Plugin in stages. Stages run in this order
// for every tx: filters → decode → validators → solve. Filters run
// cheaply on every tx in the stream so they go first; validators may
// be expensive (I/O, RPCs) and run only after decode succeeds.
type Builder[T any] struct {
	celFilter  string
	filters    []txmatch.Predicate
	decode     DecodeFunc[T]
	validators []ValidateFunc[T]
	solve      SolveFunc[T]
	log        logrus.FieldLogger
}

// For starts a new Builder for plugins that produce intents of type T.
// Generic plugins use this directly; ark-extension plugins should prefer
// ForExtension or ForTLV which return a Builder pre-loaded with the
// extension-parsing stages.
func For[T any]() *Builder[T] { return &Builder[T]{} }

// Filter adds a structural predicate evaluated before decode. All filters
// must pass (AND) for the tx to proceed. Cheap by convention — heavy work
// belongs in Decode/Validate.
func (b *Builder[T]) Filter(p txmatch.Predicate) *Builder[T] {
	if p != nil {
		b.filters = append(b.filters, p)
	}
	return b
}

// Decode sets the typed-extraction stage. Required.
func (b *Builder[T]) Decode(f DecodeFunc[T]) *Builder[T] {
	b.decode = f
	return b
}

// Validate appends a typed validator. Validators run in registration order
// and short-circuit on the first miss/error.
func (b *Builder[T]) Validate(f ValidateFunc[T]) *Builder[T] {
	if f != nil {
		b.validators = append(b.validators, f)
	}
	return b
}

// Solve sets the terminal action. Required.
func (b *Builder[T]) Solve(f SolveFunc[T]) *Builder[T] {
	b.solve = f
	return b
}

// WithLogger sets the logger used to report non-skip errors at Debug.
// A nil logger silences error reports (errors are still treated as a skip).
func (b *Builder[T]) WithLogger(log logrus.FieldLogger) *Builder[T] {
	b.log = log
	return b
}

// WithFilter sets the CEL expression applied server-side by the solver
// runtime when subscribing to the upstream tx stream for this plugin.
// Empty string (the default) means no server-side filter.
func (b *Builder[T]) WithFilter(cel string) *Builder[T] {
	b.celFilter = cel
	return b
}

// Build produces a solver.Plugin from the configured stages. Panics if
// Decode or Solve are missing — these are programmer errors and shouldn't
// be silently tolerated.
func (b *Builder[T]) Build() solver.Plugin {
	if b.decode == nil {
		panic("builder: Decode is required")
	}
	if b.solve == nil {
		panic("builder: Solve is required")
	}
	return &plugin[T]{
		celFilter:  b.celFilter,
		filters:    append([]txmatch.Predicate(nil), b.filters...),
		decode:     b.decode,
		validators: append([]ValidateFunc[T](nil), b.validators...),
		solve:      b.solve,
		log:        b.log,
	}
}

type plugin[T any] struct {
	celFilter  string
	filters    []txmatch.Predicate
	decode     DecodeFunc[T]
	validators []ValidateFunc[T]
	solve      SolveFunc[T]
	log        logrus.FieldLogger
}

func (p *plugin[T]) Filter() string { return p.celFilter }

func (p *plugin[T]) Match(ctx context.Context, tx *psbt.Packet) (any, bool) {
	var zero T
	for _, f := range p.filters {
		if !f(tx) {
			return zero, false
		}
	}
	intent, err := p.decode(ctx, tx)
	if err != nil {
		p.report(err, "builder: decode failed")
		return zero, false
	}
	for _, v := range p.validators {
		ok, err := v(ctx, intent)
		if err != nil {
			p.report(err, "builder: validate failed")
			return zero, false
		}
		if !ok {
			return zero, false
		}
	}
	return intent, true
}

func (p *plugin[T]) Solve(ctx context.Context, intent any) {
	typed, ok := intent.(T)
	if !ok {
		return
	}
	p.solve(ctx, typed)
}

func (p *plugin[T]) report(err error, msg string) {
	if errors.Is(err, ErrSkip) || p.log == nil {
		return
	}
	p.log.WithError(err).Debug(msg)
}
