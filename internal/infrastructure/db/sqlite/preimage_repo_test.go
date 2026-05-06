package sqlitedb

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arkade-os/bancod/internal/core/ports"
	"github.com/arkade-os/bancod/pkg/preimage"
)

func openTestPreimageRepo(t *testing.T) *PreimageRepository {
	t.Helper()
	db, err := OpenDB(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	repo, err := NewPreimageRepository(t.Context(), db)
	require.NoError(t, err)
	return repo
}

func sampleCreds(idx byte) preimage.ClaimCredentials {
	pkScript := append([]byte{0x51, 0x20}, bytes.Repeat([]byte{idx}, 32)...)
	return preimage.ClaimCredentials{
		Preimage:     bytes.Repeat([]byte{0x42, idx}, 16),
		ClaimAddress: "tark1placeholderaddress",
		PkScript:     pkScript,
		ArkadeScript: bytes.Repeat([]byte{idx, 0xab}, 16),
		Taptree:      []string{fmt.Sprintf("%02xab", idx), fmt.Sprintf("%02xcd", idx)},
	}
}

func TestPreimageRepository_AddGet_RoundTrip(t *testing.T) {
	repo := openTestPreimageRepo(t)
	ctx := t.Context()
	creds := sampleCreds(0xA1)

	require.NoError(t, repo.Add(ctx, creds))

	got, ok, err := repo.Get(ctx, creds.PkScript)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, creds, got)
}

func TestPreimageRepository_GetMissing_NoError(t *testing.T) {
	repo := openTestPreimageRepo(t)
	_, ok, err := repo.Get(t.Context(), []byte("not present"))
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestPreimageRepository_AddOverwrites(t *testing.T) {
	repo := openTestPreimageRepo(t)
	ctx := t.Context()

	creds1 := sampleCreds(0x01)
	creds2 := creds1
	creds2.Preimage = []byte("updated preimage")

	require.NoError(t, repo.Add(ctx, creds1))
	require.NoError(t, repo.Add(ctx, creds2))

	got, ok, err := repo.Get(ctx, creds1.PkScript)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, []byte("updated preimage"), got.Preimage)
}

func TestPreimageRepository_Delete(t *testing.T) {
	repo := openTestPreimageRepo(t)
	ctx := t.Context()
	creds := sampleCreds(0x02)

	require.NoError(t, repo.Add(ctx, creds))
	require.NoError(t, repo.Delete(ctx, creds.PkScript))

	_, ok, err := repo.Get(ctx, creds.PkScript)
	require.NoError(t, err)
	assert.False(t, ok)

	err = repo.Delete(ctx, creds.PkScript)
	assert.True(t, errors.Is(err, ports.ErrClaimNotFound))
}

func TestPreimageRepository_List(t *testing.T) {
	repo := openTestPreimageRepo(t)
	ctx := t.Context()

	c1 := sampleCreds(0x03)
	c1.ClaimAddress = "tark1addr1"
	c2 := sampleCreds(0x04)
	c2.ClaimAddress = "tark1addr2"

	require.NoError(t, repo.Add(ctx, c1))
	require.NoError(t, repo.Add(ctx, c2))

	infos, err := repo.List(ctx)
	require.NoError(t, err)
	require.Len(t, infos, 2)
	addrs := []string{infos[0].ClaimAddress, infos[1].ClaimAddress}
	assert.Contains(t, addrs, "tark1addr1")
	assert.Contains(t, addrs, "tark1addr2")
}

func TestPreimageRepository_CacheWarmedAtStartup(t *testing.T) {
	db, err := OpenDB(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	creds := sampleCreds(0x05)
	encodedTaptree, err := encodeTaptree(creds.Taptree)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(),
		`INSERT INTO preimage_claim (pk_script, claim_address, preimage, arkade_script, taptree, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		creds.PkScript, creds.ClaimAddress, creds.Preimage, creds.ArkadeScript, encodedTaptree, int64(0),
	)
	require.NoError(t, err)

	repo, err := NewPreimageRepository(t.Context(), db)
	require.NoError(t, err)

	got, ok, err := repo.Get(t.Context(), creds.PkScript)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, creds, got)
}

// Compile-time check that PreimageRepository satisfies the port.
var _ ports.PreimageRepository = (*PreimageRepository)(nil)
