package swap

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPair_Base(t *testing.T) {
	tests := []struct {
		name     string
		pair     string
		expected string
	}{
		{
			name:     "normal pair",
			pair:     "ABC/DEF",
			expected: "ABC",
		},
		{
			name:     "no slash",
			pair:     "ABCDEF",
			expected: "ABCDEF",
		},
		{
			name:     "empty string",
			pair:     "",
			expected: "",
		},
		{
			name:     "trailing slash",
			pair:     "ABC/",
			expected: "ABC",
		},
		{
			name:     "BTC/asset",
			pair:     "BTC/d4e5f6abc123",
			expected: "BTC",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := Pair{Pair: tc.pair}
			assert.Equal(t, tc.expected, p.Base())
		})
	}
}

func TestPair_Quote(t *testing.T) {
	tests := []struct {
		name     string
		pair     string
		expected string
	}{
		{
			name:     "normal pair",
			pair:     "ABC/DEF",
			expected: "DEF",
		},
		{
			name:     "no slash",
			pair:     "ABCDEF",
			expected: "",
		},
		{
			name:     "empty string",
			pair:     "",
			expected: "",
		},
		{
			name:     "trailing slash",
			pair:     "ABC/",
			expected: "",
		},
		{
			name:     "BTC/asset",
			pair:     "BTC/d4e5f6abc123",
			expected: "d4e5f6abc123",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := Pair{Pair: tc.pair}
			assert.Equal(t, tc.expected, p.Quote())
		})
	}
}
