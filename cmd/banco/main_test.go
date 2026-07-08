package main

import "testing"

func TestComputeWantAmount(t *testing.T) {
	tests := []struct {
		name     string
		deposit  uint64
		baseDec  int
		quoteDec int
		price    float64
		want     uint64
		wantErr  bool
	}{
		// 500 zero-decimal asset units at price 1 asset/sat -> 500 sats... price is
		// base/quote in human units: 500 / 1 = 500 quote units, quoteDec 0.
		{"same decimals price 1", 500, 0, 0, 1, 500, false},
		// 1 BTC (1e8 sats, 8 dec) at 50000 USD/BTC -> 50000 USD with 2 decimals = 5_000_000 units.
		{"btc to usd", 100_000_000, 8, 2, 1.0 / 50_000, 5_000_000, false},
		{"rounding", 1, 0, 0, 3, 0, true}, // 1/3 rounds to 0 -> too small
		{"zero price", 500, 0, 0, 0, 0, true},
		{"negative price", 500, 0, 0, -1, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := computeWantAmount(tt.deposit, tt.baseDec, tt.quoteDec, tt.price)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestSplitPair(t *testing.T) {
	if _, _, ok := splitPair("BTC"); ok {
		t.Fatal("expected failure for pair without separator")
	}
	base, quote, ok := splitPair("abc123/BTC")
	if !ok || base != "abc123" || quote != "BTC" {
		t.Fatalf("got %q/%q ok=%v", base, quote, ok)
	}
}
