package ports

import (
	"context"
	"errors"

	"github.com/arkade-os/solver/pkg/swap"
)

var (
	ErrMarketNotFound = errors.New("market not found")
	ErrMarketExists   = errors.New("market already exists")
)

// MarketRepository extends swap.MarketRepository with CRUD operations.
type MarketRepository interface {
	swap.MarketRepository
	Add(ctx context.Context, m swap.Market) error
	Update(ctx context.Context, m swap.Market) error
	Remove(ctx context.Context, base, quote string) error
}
