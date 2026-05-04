package bounty

import (
	"errors"
	"testing"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestPlugin returns a plugin with no live clients — only the in-memory
// buffer / flushTrigger machinery is exercised.
func newTestPlugin(batchSize int, batchTimeout time.Duration) *plugin {
	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)
	return &plugin{
		cfg:          Config{BatchSize: batchSize, BatchTimeout: batchTimeout, Log: log},
		log:          log,
		buffer:       make(map[wire.OutPoint]*MatchedBounty),
		flushTrigger: make(chan struct{}, 1),
	}
}

func mkMatch(idx byte, difficulty uint8, amount uint64) *MatchedBounty {
	var h chainhash.Hash
	h[0] = idx
	return &MatchedBounty{
		Difficulty: difficulty,
		Outpoint:   wire.OutPoint{Hash: h, Index: uint32(idx)},
		Amount:     amount,
	}
}

func TestEnqueue_Dedup(t *testing.T) {
	p := newTestPlugin(10, time.Second)
	m := mkMatch(0xAA, 4, 5_000)
	p.enqueue(t.Context(), m)
	p.enqueue(t.Context(), m)
	p.enqueue(t.Context(), m)
	assert.Len(t, p.buffer, 1, "duplicate outpoints must be deduplicated")
}

func TestEnqueue_BuffersAcrossDifficulties(t *testing.T) {
	p := newTestPlugin(10, time.Second)
	p.enqueue(t.Context(), mkMatch(0x01, 2, 5_000))
	p.enqueue(t.Context(), mkMatch(0x02, 4, 5_000))
	p.enqueue(t.Context(), mkMatch(0x03, 4, 5_000))
	// Single unified buffer — different difficulties co-exist and will be
	// claimed in one tx, mined for the max difficulty present.
	assert.Len(t, p.buffer, 3)
}

func TestEnqueue_SizeTrigger(t *testing.T) {
	p := newTestPlugin(2, time.Second)
	p.enqueue(t.Context(), mkMatch(0x01, 4, 5_000))
	select {
	case <-p.flushTrigger:
		t.Fatalf("flush should not fire below BatchSize")
	default:
	}
	p.enqueue(t.Context(), mkMatch(0x02, 4, 5_000))
	select {
	case <-p.flushTrigger:
	default:
		t.Fatalf("flush should fire when buffer reaches BatchSize")
	}
}

func TestMaxDifficulty(t *testing.T) {
	tests := []struct {
		name  string
		batch []*MatchedBounty
		want  uint8
	}{
		{"empty", nil, 0},
		{"single", []*MatchedBounty{mkMatch(1, 3, 1)}, 3},
		{"mixed", []*MatchedBounty{
			mkMatch(1, 2, 1),
			mkMatch(2, 5, 1),
			mkMatch(3, 1, 1),
			mkMatch(4, 4, 1),
		}, 5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, maxDifficulty(tc.batch))
		})
	}
}

func TestParseSpentOutpoints_SingleEntry(t *testing.T) {
	// Real-world fragment surfaced by arkd via gRPC InvalidArgument.
	err := errors.New(
		"introspector submit: rpc error: code = InvalidArgument desc = " +
			"VTXO_ALREADY_SPENT (6): " +
			"c7f4a4c0710972824d13b5571a6d7f18f18a17628da4485940d4c27d140acb01:0 already spent",
	)
	got := parseSpentOutpoints(err)
	require.Len(t, got, 1)

	hash, hErr := chainhash.NewHashFromStr(
		"c7f4a4c0710972824d13b5571a6d7f18f18a17628da4485940d4c27d140acb01",
	)
	require.NoError(t, hErr)
	want := wire.OutPoint{Hash: *hash, Index: 0}
	assert.True(t, got[want], "expected outpoint not extracted")
}

func TestParseSpentOutpoints_MultipleEntries(t *testing.T) {
	err := errors.New(
		"VTXO_ALREADY_SPENT (6): " +
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa:3 already spent; " +
			"VTXO_ALREADY_SPENT (6): " +
			"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb:7 already spent",
	)
	got := parseSpentOutpoints(err)
	assert.Len(t, got, 2)

	for _, h := range []string{
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	} {
		hash, _ := chainhash.NewHashFromStr(h)
		var idx uint32 = 3
		if h[0] == 'b' {
			idx = 7
		}
		assert.True(t, got[wire.OutPoint{Hash: *hash, Index: idx}], "missing %s:%d", h, idx)
	}
}

func TestParseSpentOutpoints_NotASpentError(t *testing.T) {
	tests := []error{
		nil,
		errors.New("rpc error: code = Unavailable desc = connection refused"),
		errors.New("introspector submit: timeout"),
		errors.New("AMOUNT_TOO_LOW (15): output #1 amount is below dust limit"),
	}
	for _, err := range tests {
		assert.Empty(t, parseSpentOutpoints(err), "expected no spent outpoints for %v", err)
	}
}

func TestParseSpentOutpoints_Recoverable(t *testing.T) {
	// arkd also rejects per-input via VTXO_RECOVERABLE — same retry path.
	err := errors.New(
		"introspector submit: rpc error: code = InvalidArgument desc = " +
			"VTXO_RECOVERABLE (8): " +
			"c76795d9db129ce69699953300bc79a94cc7c060d29d6ec46e4e8257597792d9:0 is recoverable",
	)
	got := parseSpentOutpoints(err)
	require.Len(t, got, 1)
	hash, hErr := chainhash.NewHashFromStr(
		"c76795d9db129ce69699953300bc79a94cc7c060d29d6ec46e4e8257597792d9",
	)
	require.NoError(t, hErr)
	assert.True(t, got[wire.OutPoint{Hash: *hash, Index: 0}])
}
