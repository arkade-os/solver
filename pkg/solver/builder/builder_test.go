package builder

import (
	"context"
	"errors"
	"testing"

	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/wire"
	"github.com/stretchr/testify/require"

	"github.com/arkade-os/solver/pkg/solver/txmatch"
)

type intent struct{ value int }

func okPkt(t *testing.T) *psbt.Packet {
	t.Helper()
	tx := wire.NewMsgTx(2)
	tx.AddTxOut(&wire.TxOut{Value: 1, PkScript: []byte{0x00}})
	p, err := psbt.NewFromUnsignedTx(tx)
	require.NoError(t, err)
	return p
}

func TestBuilder_HappyPath(t *testing.T) {
	got := make(chan *intent, 1)
	plugin := For[*intent]().
		Filter(txmatch.Always(true)).
		Decode(func(context.Context, *psbt.Packet) (*intent, error) { return &intent{42}, nil }).
		Validate(func(context.Context, *intent) (bool, error) { return true, nil }).
		Solve(func(_ context.Context, i *intent) { got <- i }).
		Build()

	out, ok := plugin.Match(context.Background(), okPkt(t))
	require.True(t, ok)
	plugin.Solve(context.Background(), out)
	require.Equal(t, 42, (<-got).value)
}

func TestBuilder_FilterShortCircuits(t *testing.T) {
	decoded := false
	plugin := For[*intent]().
		Filter(txmatch.Always(false)).
		Decode(func(context.Context, *psbt.Packet) (*intent, error) {
			decoded = true
			return &intent{}, nil
		}).
		Solve(func(context.Context, *intent) {}).
		Build()

	_, ok := plugin.Match(context.Background(), okPkt(t))
	require.False(t, ok)
	require.False(t, decoded, "Decode must not run when a filter rejects")
}

func TestBuilder_DecodeErrSkipIsSilent(t *testing.T) {
	plugin := For[*intent]().
		Decode(func(context.Context, *psbt.Packet) (*intent, error) { return nil, ErrSkip }).
		Solve(func(context.Context, *intent) {}).
		Build()

	_, ok := plugin.Match(context.Background(), okPkt(t))
	require.False(t, ok)
}

func TestBuilder_DecodeOtherErrorIsSkip(t *testing.T) {
	plugin := For[*intent]().
		Decode(func(context.Context, *psbt.Packet) (*intent, error) { return nil, errors.New("boom") }).
		Solve(func(context.Context, *intent) {}).
		Build()

	_, ok := plugin.Match(context.Background(), okPkt(t))
	require.False(t, ok)
}

func TestBuilder_ValidatorOrderingAndShortCircuit(t *testing.T) {
	var calls []string
	plugin := For[*intent]().
		Decode(func(context.Context, *psbt.Packet) (*intent, error) { return &intent{1}, nil }).
		Validate(func(context.Context, *intent) (bool, error) {
			calls = append(calls, "v1")
			return true, nil
		}).
		Validate(func(context.Context, *intent) (bool, error) {
			calls = append(calls, "v2")
			return false, nil
		}).
		Validate(func(context.Context, *intent) (bool, error) {
			calls = append(calls, "v3")
			return true, nil
		}).
		Solve(func(context.Context, *intent) {}).
		Build()

	_, ok := plugin.Match(context.Background(), okPkt(t))
	require.False(t, ok)
	require.Equal(t, []string{"v1", "v2"}, calls, "v3 must not run after v2 returns false")
}

func TestBuilder_ValidatorErrorIsSkip(t *testing.T) {
	plugin := For[*intent]().
		Decode(func(context.Context, *psbt.Packet) (*intent, error) { return &intent{1}, nil }).
		Validate(func(context.Context, *intent) (bool, error) { return false, errors.New("db down") }).
		Solve(func(context.Context, *intent) {}).
		Build()

	_, ok := plugin.Match(context.Background(), okPkt(t))
	require.False(t, ok)
}

func TestBuilder_BuildPanicsOnMissingDecode(t *testing.T) {
	require.PanicsWithValue(t, "builder: Decode is required", func() {
		For[*intent]().Solve(func(context.Context, *intent) {}).Build()
	})
}

func TestBuilder_BuildPanicsOnMissingSolve(t *testing.T) {
	require.PanicsWithValue(t, "builder: Solve is required", func() {
		For[*intent]().
			Decode(func(context.Context, *psbt.Packet) (*intent, error) { return nil, nil }).
			Build()
	})
}

func TestBuilder_SolveDispatchesTypedIntent(t *testing.T) {
	got := make(chan int, 1)
	plugin := For[*intent]().
		Decode(func(context.Context, *psbt.Packet) (*intent, error) { return &intent{99}, nil }).
		Solve(func(_ context.Context, i *intent) { got <- i.value }).
		Build()

	out, ok := plugin.Match(context.Background(), okPkt(t))
	require.True(t, ok)
	plugin.Solve(context.Background(), out)
	require.Equal(t, 99, <-got)
}

func TestBuilder_SolveIgnoresWrongTypedIntent(t *testing.T) {
	called := false
	plugin := For[*intent]().
		Decode(func(context.Context, *psbt.Packet) (*intent, error) { return &intent{}, nil }).
		Solve(func(context.Context, *intent) { called = true }).
		Build()

	// A foreign value can't be unwrapped into *intent — Solve must no-op.
	plugin.Solve(context.Background(), "not an *intent")
	require.False(t, called)
}
