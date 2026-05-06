package ports

import (
	"context"
	"errors"

	"github.com/arkade-os/bancod/pkg/preimage"
)

// ErrClaimNotFound is returned by PreimageRepository.Delete on a missing key.
var ErrClaimNotFound = errors.New("claim not found")

// PreimageRepository extends preimage.Repository with CRUD operations.
type PreimageRepository interface {
	preimage.Repository

	Add(ctx context.Context, creds preimage.ClaimCredentials) error
	Delete(ctx context.Context, pkScript []byte) error
	List(ctx context.Context) ([]preimage.ClaimInfo, error)
}
