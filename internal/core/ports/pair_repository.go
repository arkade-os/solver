package ports

import (
	"context"
	"errors"

	"github.com/arkade-os/solver/pkg/banco"
)

// ErrPairNotFound is returned by PairRepository.Update when the pair does
// not exist. Callers should map this to a NotFound API error.
var ErrPairNotFound = errors.New("pair not found")

// PairRepository extends banco.PairRepository with CRUD operations
// for managing trading pairs. The embedded banco.PairRepository provides
// the read-only List method used by the solver engine.
type PairRepository interface {
	banco.PairRepository // List(ctx context.Context) ([]banco.Pair, error)
	Add(ctx context.Context, pair banco.Pair) error
	Update(ctx context.Context, pair banco.Pair) error
	Remove(ctx context.Context, pairName string) error
}
