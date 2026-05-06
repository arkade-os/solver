package preimage

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/wire"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubRepo is a minimal preimage.Repository for plugin tests.
type stubRepo struct {
	mu      sync.RWMutex
	entries map[string]ClaimCredentials
}

func newStubRepo() *stubRepo {
	return &stubRepo{entries: make(map[string]ClaimCredentials)}
}

func (s *stubRepo) Get(_ context.Context, pkScript []byte) (ClaimCredentials, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.entries[string(pkScript)]
	return c, ok, nil
}

func (s *stubRepo) put(pkScript []byte, creds ClaimCredentials) {
	s.mu.Lock()
	s.entries[string(pkScript)] = creds
	s.mu.Unlock()
}

func newTestPlugin(repo Repository) *plugin {
	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)
	return &plugin{
		cfg: Config{Repository: repo, Log: log},
		log: log,
	}
}

func mkTx(outputs ...*wire.TxOut) *psbt.Packet {
	tx := wire.NewMsgTx(2)
	for _, o := range outputs {
		tx.AddTxOut(o)
	}
	p, _ := psbt.NewFromUnsignedTx(tx)
	return p
}

func TestPluginDecode_Match(t *testing.T) {
	repo := newStubRepo()
	target := bytes.Repeat([]byte{0x42}, 34)
	creds := ClaimCredentials{Preimage: []byte("preimage"), PkScript: target}
	repo.put(target, creds)

	p := newTestPlugin(repo)

	tx := mkTx(
		&wire.TxOut{Value: 1234, PkScript: bytes.Repeat([]byte{0x99}, 34)},
		&wire.TxOut{Value: 5000, PkScript: target},
	)
	matched, err := p.decode(t.Context(), tx)
	require.NoError(t, err)
	require.NotNil(t, matched)
	assert.Equal(t, uint64(5000), matched.Amount)
	assert.Equal(t, uint32(1), matched.Outpoint.Index)
	assert.Equal(t, creds, matched.Credentials)
}

func TestPluginDecode_NoMatch(t *testing.T) {
	repo := newStubRepo()
	p := newTestPlugin(repo)

	tx := mkTx(&wire.TxOut{Value: 1234, PkScript: bytes.Repeat([]byte{0x99}, 34)})
	_, err := p.decode(t.Context(), tx)
	require.Error(t, err)
}

func TestPluginDecode_FirstMatchWins(t *testing.T) {
	repo := newStubRepo()
	a := bytes.Repeat([]byte{0xAA}, 34)
	b := bytes.Repeat([]byte{0xBB}, 34)
	repo.put(a, ClaimCredentials{PkScript: a, Preimage: []byte("a")})
	repo.put(b, ClaimCredentials{PkScript: b, Preimage: []byte("b")})

	p := newTestPlugin(repo)

	tx := mkTx(
		&wire.TxOut{Value: 1, PkScript: bytes.Repeat([]byte{0x99}, 34)},
		&wire.TxOut{Value: 2, PkScript: a},
		&wire.TxOut{Value: 3, PkScript: b},
	)
	matched, err := p.decode(t.Context(), tx)
	require.NoError(t, err)
	assert.Equal(t, uint32(1), matched.Outpoint.Index)
	assert.Equal(t, []byte("a"), matched.Credentials.Preimage)
}
