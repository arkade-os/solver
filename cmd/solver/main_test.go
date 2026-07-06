package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"
)

func runPairOverrides(t *testing.T, args ...string) map[string]any {
	t.Helper()
	var got map[string]any
	app := &cli.App{
		Commands: []*cli.Command{{
			Name:  "probe",
			Flags: pairFlags(false),
			Action: func(c *cli.Context) error {
				got = pairOverrides(c)
				return nil
			},
		}},
	}
	require.NoError(t, app.Run(append([]string{"solver", "probe"}, args...)))
	return got
}

func TestPairOverrides(t *testing.T) {
	got := runPairOverrides(t, "--pair", "BTC/aabbcc")
	assert.Equal(t, map[string]any{"pair": "BTC/aabbcc"}, got)

	got = runPairOverrides(t,
		"--pair", "BTC/aabbcc", "--min", "500", "--slippage-bps", "250",
	)
	assert.Equal(t, map[string]any{
		"pair":         "BTC/aabbcc",
		"min_amount":   uint64(500),
		"slippage_bps": uint(250),
	}, got)
}

func TestPairUpdatePartial(t *testing.T) {
	var putBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/pairs":
			// nolint:errcheck
			w.Write([]byte(`{"pairs":[{
				"pair":"BTC/aabbcc","min_amount":1000,"max_amount":100000,
				"price_feed":"https://example.com/price","slippage_bps":250,
				"base_decimals":8,"quote_decimals":6}]}`))
		case "PUT /v1/pair":
			body, _ := io.ReadAll(r.Body)
			require.NoError(t, json.Unmarshal(body, &putBody))
			// nolint:errcheck
			w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	app := &cli.App{
		Flags:    []cli.Flag{&cli.StringFlag{Name: "server"}},
		Commands: []*cli.Command{pairCommand},
	}
	err := app.Run([]string{
		"solver", "--server", srv.URL,
		"pair", "update", "--pair", "BTC/aabbcc", "--min", "2000",
	})
	require.NoError(t, err)

	pair, ok := putBody["pair"].(map[string]any)
	require.True(t, ok, "PUT body missing pair object: %v", putBody)
	assert.Equal(t, float64(2000), pair["min_amount"], "flag override applied")
	assert.Equal(t, float64(100000), pair["max_amount"], "unset field preserved")
	assert.Equal(t, float64(250), pair["slippage_bps"], "unset slippage preserved")
	assert.Equal(t, "https://example.com/price", pair["price_feed"])
}

func TestPairUpdateUnknownPair(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// nolint:errcheck
		w.Write([]byte(`{"pairs":[]}`))
	}))
	defer srv.Close()

	app := &cli.App{
		Flags:    []cli.Flag{&cli.StringFlag{Name: "server"}},
		Commands: []*cli.Command{pairCommand},
	}
	err := app.Run([]string{
		"solver", "--server", srv.URL,
		"pair", "update", "--pair", "NOPE/NOPE", "--min", "1",
	})
	assert.ErrorContains(t, err, `pair "NOPE/NOPE" not found`)
}
