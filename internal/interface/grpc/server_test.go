package grpcservice

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arkade-os/solver/internal/core/application"
	"github.com/arkade-os/solver/internal/core/ports"
	"github.com/arkade-os/solver/pkg/banco"
)

// stubService implements BancoService for gateway tests; only
// BuildDiscoveryCard is exercised.
type stubService struct {
	card []byte
	path string
	err  error
	// records the sign flag of the last call
	lastSign bool
}

func (s *stubService) AddPair(context.Context, banco.Pair) (banco.Pair, error) {
	return banco.Pair{}, nil
}
func (s *stubService) UpdatePair(context.Context, banco.Pair) (banco.Pair, error) {
	return banco.Pair{}, nil
}
func (s *stubService) RemovePair(context.Context, string) error        { return nil }
func (s *stubService) ListPairs(context.Context) ([]banco.Pair, error) { return nil, nil }
func (s *stubService) ListTrades(context.Context, int) ([]ports.Trade, error) {
	return nil, nil
}
func (s *stubService) GetBalance(context.Context) (*application.Balance, error) { return nil, nil }
func (s *stubService) GetAddress(context.Context) (*application.Address, error) { return nil, nil }
func (s *stubService) ListAssets(context.Context) ([]application.AssetInfo, error) {
	return nil, nil
}
func (s *stubService) SendOffchain(context.Context, string, string, string, uint64) (string, error) {
	return "", nil
}
func (s *stubService) CollaborativeExit(context.Context, string, string, uint64) (string, error) {
	return "", nil
}
func (s *stubService) Settle(context.Context, string) (string, error) { return "", nil }
func (s *stubService) BuildDiscoveryCard(_ context.Context, sign bool) ([]byte, string, error) {
	s.lastSign = sign
	return s.card, s.path, s.err
}

func TestDiscoveryCardRoute(t *testing.T) {
	card := []byte("{\n  \"version\": 0\n}\n")
	stub := &stubService{card: card, path: "solvers/signet/test-solver.json"}
	srv := httptest.NewServer(newHTTPGateway(newHandler(stub)))
	defer srv.Close()

	t.Run("returns raw card bytes and path hint", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/v1/discovery/card")
		require.NoError(t, err)
		defer resp.Body.Close() //nolint:errcheck
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, string(card), string(body), "body must be the exact card bytes")
		assert.Equal(t, "solvers/signet/test-solver.json", resp.Header.Get("X-Registry-Path"))
		assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
		assert.False(t, stub.lastSign)
	})

	t.Run("sign=true is forwarded", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/v1/discovery/card?sign=true")
		require.NoError(t, err)
		resp.Body.Close() //nolint:errcheck
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.True(t, stub.lastSign)
	})

	t.Run("invalid sign value rejected", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/v1/discovery/card?sign=yes")
		require.NoError(t, err)
		resp.Body.Close() //nolint:errcheck
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("service error surfaces as 400", func(t *testing.T) {
		stub.err = fmt.Errorf("SOLVER_NAME is not set")
		defer func() { stub.err = nil }()
		resp, err := http.Get(srv.URL + "/v1/discovery/card")
		require.NoError(t, err)
		defer resp.Body.Close()          //nolint:errcheck
		body, _ := io.ReadAll(resp.Body) //nolint:errcheck
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		assert.Contains(t, string(body), "SOLVER_NAME")
	})
}
