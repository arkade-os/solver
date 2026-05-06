package sqlitedb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/arkade-os/bancod/internal/core/ports"
	"github.com/arkade-os/bancod/internal/infrastructure/db/sqlite/sqlc"
	"github.com/arkade-os/bancod/pkg/preimage"
)

// PreimageRepository is the SQLite-backed ports.PreimageRepository.
//
// Get is served from an in-memory cache (warmed at construction) so the
// plugin's hot path never hits SQLite. Add/Delete write SQLite first, then
// update the cache under a write lock.
type PreimageRepository struct {
	queries *sqlc.Queries

	mu    sync.RWMutex
	cache map[string]preimage.ClaimCredentials
}

func NewPreimageRepository(ctx context.Context, db *sql.DB) (*PreimageRepository, error) {
	repo := &PreimageRepository{
		queries: sqlc.New(db),
		cache:   make(map[string]preimage.ClaimCredentials),
	}
	if err := repo.warmCache(ctx); err != nil {
		return nil, err
	}
	return repo, nil
}

func (r *PreimageRepository) warmCache(ctx context.Context) error {
	rows, err := r.queries.ListAllPreimageClaims(ctx)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, row := range rows {
		taptree, err := decodeTaptree(row.Taptree)
		if err != nil {
			return fmt.Errorf("decode taptree (pkScript=%x): %w", row.PkScript, err)
		}
		r.cache[string(row.PkScript)] = preimage.ClaimCredentials{
			Preimage:     row.Preimage,
			ClaimAddress: row.ClaimAddress,
			PkScript:     row.PkScript,
			ArkadeScript: row.ArkadeScript,
			Taptree:      taptree,
		}
	}
	return nil
}

func (r *PreimageRepository) Get(_ context.Context, pkScript []byte) (preimage.ClaimCredentials, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	creds, ok := r.cache[string(pkScript)]
	return creds, ok, nil
}

// Add is idempotent on duplicate pk_script (INSERT OR REPLACE).
func (r *PreimageRepository) Add(ctx context.Context, creds preimage.ClaimCredentials) error {
	encodedTaptree, err := encodeTaptree(creds.Taptree)
	if err != nil {
		return fmt.Errorf("encode taptree: %w", err)
	}
	if err := r.queries.InsertPreimageClaim(ctx, sqlc.InsertPreimageClaimParams{
		PkScript:     creds.PkScript,
		ClaimAddress: creds.ClaimAddress,
		Preimage:     creds.Preimage,
		ArkadeScript: creds.ArkadeScript,
		Taptree:      encodedTaptree,
		CreatedAt:    time.Now().Unix(),
	}); err != nil {
		return err
	}
	r.mu.Lock()
	r.cache[string(creds.PkScript)] = creds
	r.mu.Unlock()
	return nil
}

// encodeTaptree / decodeTaptree shuttle the []string taptree through SQLite's
// BLOB column via JSON.
func encodeTaptree(scripts []string) ([]byte, error) {
	return json.Marshal(scripts)
}

func decodeTaptree(blob []byte) ([]string, error) {
	if len(blob) == 0 {
		return nil, nil
	}
	var scripts []string
	if err := json.Unmarshal(blob, &scripts); err != nil {
		return nil, err
	}
	return scripts, nil
}

// Delete returns ports.ErrClaimNotFound if no row matched.
func (r *PreimageRepository) Delete(ctx context.Context, pkScript []byte) error {
	rows, err := r.queries.DeletePreimageClaim(ctx, pkScript)
	if err != nil {
		return err
	}
	if rows == 0 {
		return ports.ErrClaimNotFound
	}
	r.mu.Lock()
	delete(r.cache, string(pkScript))
	r.mu.Unlock()
	return nil
}

func (r *PreimageRepository) List(ctx context.Context) ([]preimage.ClaimInfo, error) {
	rows, err := r.queries.ListPreimageClaims(ctx)
	if err != nil {
		return nil, err
	}
	infos := make([]preimage.ClaimInfo, 0, len(rows))
	for _, row := range rows {
		infos = append(infos, preimage.ClaimInfo{ClaimAddress: row.ClaimAddress})
	}
	return infos, nil
}
