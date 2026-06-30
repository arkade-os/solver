package ports

import (
	"context"
	"errors"

	"github.com/arkade-os/solver/pkg/banco"
)

var ErrPairNotFound = errors.New("pair not found")

// PairRepository extends banco.PairRepository with CRUD operations
type PairRepository interface {
	banco.PairRepository 
	Add(ctx context.Context, pair banco.Pair) error
	Update(ctx context.Context, pair banco.Pair) error
	Remove(ctx context.Context, pairName string) error
}
