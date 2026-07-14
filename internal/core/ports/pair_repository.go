package ports

import (
	"context"
	"errors"

	"github.com/arkade-os/solver/pkg/swap"
)

var (
	ErrPairNotFound = errors.New("pair not found")
	ErrPairExists   = errors.New("pair already exists")
)

// PairRepository extends swap.PairRepository with CRUD operations
type PairRepository interface {
	swap.PairRepository
	Add(ctx context.Context, pair swap.Pair) error
	Update(ctx context.Context, pair swap.Pair) error
	Remove(ctx context.Context, pairName string) error
}
