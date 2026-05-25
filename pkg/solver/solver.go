package solver

import (
	"context"
	"runtime/debug"
	"sync"

	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/sirupsen/logrus"
)

// Plugin is the protocol-specific contract a Solver runs.
type Plugin interface {
	// Filter returns the CEL expression applied server-side to the upstream
	// tx stream. Empty string means "no filter" (full stream).
	Filter() string

	// Match decodes/filters tx.
	//   ok=false  -> ignore tx (not for us)
	//   ok=true   -> hand intent to Solve
	Match(ctx context.Context, tx *psbt.Packet) (intent any, ok bool)

	// Solve reacts to a matched intent.
	Solve(ctx context.Context, intent any)
}

// Source produces a stream of *psbt.Packet matching the given CEL filter.
// An empty filter means no filtering. The returned channel is closed when
// ctx is canceled or the upstream stream ends. Subscribe returns an error
// only if the subscription itself can't be established; transport errors
// after that close the channel instead.
type Source interface {
	Subscribe(ctx context.Context, filter string) (<-chan *psbt.Packet, error)
}

// Solver is the runtime that drives Plugins against a tx stream.
type Solver struct {
	plugins []Plugin
	log     logrus.FieldLogger
}

// New wraps Plugins in a Solver runtime.
func New(plugins ...Plugin) *Solver {
	return &Solver{plugins: plugins, log: logrus.StandardLogger()}
}

// WithLogger returns a copy of s using the given logger for panic reports.
// Pass nil to disable panic logging entirely (panics are still recovered).
func (s *Solver) WithLogger(log logrus.FieldLogger) *Solver {
	cp := *s
	cp.log = log
	return &cp
}

// Run subscribes each Plugin to Source with its own CEL filter and
// dispatches incoming txs to Match -> (Solve if ok). Each plugin runs on
// its own subscription so server-side filtering can drop unrelated txs
// before they ever reach us. Solves run concurrently; Run waits for all
// in-flight Solves before returning, so callers get clean shutdown
// semantics. Panics inside Match or Solve are recovered and logged so
// one buggy plugin can't crash the bot.
//
//   - returns ctx.Err() when ctx is canceled (after in-flight Solves drain)
//   - returns nil when every per-plugin stream has closed (or no plugin
//     subscribed successfully)
//
// A Subscribe error for a single plugin is logged and that plugin is
// skipped; the rest continue.
func (s *Solver) Run(ctx context.Context, source Source) error {
	var wg sync.WaitGroup

	for _, p := range s.plugins {
		txs, err := source.Subscribe(ctx, p.Filter())
		if err != nil {
			if s.log != nil {
				s.log.WithError(err).Error("plugin subscribe failed")
			}
			continue
		}
		wg.Add(1)
		go func(p Plugin, txs <-chan *psbt.Packet) {
			defer wg.Done()
			s.consume(ctx, p, txs)
		}(p, txs)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-ctx.Done():
		<-done
		return ctx.Err()
	case <-done:
		return nil
	}
}

// consume drives one plugin's tx stream until ctx is canceled or the
// stream closes. Solves are launched concurrently and waited on via wg
// so they drain before consume returns.
func (s *Solver) consume(ctx context.Context, p Plugin, txs <-chan *psbt.Packet) {
	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		select {
		case <-ctx.Done():
			return
		case tx, ok := <-txs:
			if !ok {
				return
			}
			if tx == nil {
				continue
			}
			intent, matched := s.safeMatch(ctx, p, tx)
			if !matched {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.safeSolve(ctx, p, intent)
			}()
		}
	}
}

// safeMatch runs p.Match with panic recovery.
func (s *Solver) safeMatch(ctx context.Context, p Plugin, tx *psbt.Packet) (intent any, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			s.logPanic(r, "plugin Match panicked")
			intent, ok = nil, false
		}
	}()
	return p.Match(ctx, tx)
}

// safeSolve runs p.Solve with panic recovery.
func (s *Solver) safeSolve(ctx context.Context, p Plugin, intent any) {
	defer func() {
		if r := recover(); r != nil {
			s.logPanic(r, "plugin Solve panicked")
		}
	}()
	p.Solve(ctx, intent)
}

// logPanic emits a panic report unless the solver was configured without a logger.
func (s *Solver) logPanic(r any, msg string) {
	if s.log == nil {
		return
	}
	s.log.WithField("panic", r).WithField("stack", string(debug.Stack())).Error(msg)
}
