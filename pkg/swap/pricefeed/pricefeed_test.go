package pricefeed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFetch(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		query     string
		pricePath string
		want      float64
		wantErr   bool
	}{
		{name: "pointer into nested object", body: `{"bitcoin":{"usd":50000}}`, pricePath: "/bitcoin/usd", want: 50000},
		{name: "pointer to numeric string", body: `{"price":"50000.25"}`, pricePath: "/price", want: 50000.25},
		{name: "pointer into array", body: `{"data":[{"p":1.5},{"p":2.5}]}`, pricePath: "/data/1/p", want: 2.5},
		{name: "pointer with escaped key", body: `{"a/b":7}`, pricePath: "/a~1b", want: 7},
		{name: "missing key", body: `{"bitcoin":{"usd":1}}`, pricePath: "/bitcoin/eur", wantErr: true},
		{name: "non-numeric value", body: `{"bitcoin":{"usd":"n/a"}}`, pricePath: "/bitcoin/usd", wantErr: true},
		{name: "descend into scalar", body: `{"bitcoin":1}`, pricePath: "/bitcoin/usd", wantErr: true},
		// empty path falls back to the provider's default pointer
		{name: "coingecko default", body: `{"bitcoin":{"usd":50000}}`, query: "?ids=bitcoin&vs_currencies=usd", want: 50000},
		{name: "unknown feed without path", body: `{"bitcoin":{"usd":50000}}`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				// nolint:errcheck
				w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			price, err := New().Fetch(context.Background(), srv.URL+tt.query, tt.pricePath)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, price)
		})
	}
}

func TestDefaultPricePath(t *testing.T) {
	path, err := DefaultPricePath("https://api.binance.com/api/v3/ticker/price?symbol=BTCUSDT")
	require.NoError(t, err)
	require.Equal(t, "/price", path)

	path, err = DefaultPricePath("https://api.coingecko.com/api/v3/simple/price?ids=bitcoin&vs_currencies=usd")
	require.NoError(t, err)
	require.Equal(t, "/bitcoin/usd", path)

	_, err = DefaultPricePath("https://feed.example.com/price")
	require.Error(t, err)
}

func TestValidatePricePath(t *testing.T) {
	require.NoError(t, ValidatePricePath(""))
	require.NoError(t, ValidatePricePath("/bitcoin/usd"))
	require.NoError(t, ValidatePricePath("/a~1b/c~0d"))
	require.Error(t, ValidatePricePath("bitcoin.usd"))
	require.Error(t, ValidatePricePath("/a~b"))
	require.Error(t, ValidatePricePath("/a~"))
}
