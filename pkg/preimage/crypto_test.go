package preimage_test

import (
	"crypto/rand"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arkade-os/solver/pkg/preimage"
)

func TestECIES_RoundTrip(t *testing.T) {
	priv, err := btcec.NewPrivateKey()
	require.NoError(t, err)

	plaintext := []byte("preimage-and-arkade-script-payload")
	blob, err := preimage.Encrypt(priv.PubKey(), plaintext)
	require.NoError(t, err)
	require.Greater(t, len(blob), len(plaintext)+33+12+16-1)

	out, err := preimage.Decrypt(priv, blob)
	require.NoError(t, err)
	assert.Equal(t, plaintext, out)
}

func TestECIES_WrongKey(t *testing.T) {
	priv, _ := btcec.NewPrivateKey()
	wrong, _ := btcec.NewPrivateKey()
	blob, err := preimage.Encrypt(priv.PubKey(), []byte("hi"))
	require.NoError(t, err)
	_, err = preimage.Decrypt(wrong, blob)
	require.Error(t, err)
}

func TestECIES_Tampered(t *testing.T) {
	priv, _ := btcec.NewPrivateKey()
	blob, err := preimage.Encrypt(priv.PubKey(), []byte("hello world"))
	require.NoError(t, err)
	blob[50] ^= 0xff
	_, err = preimage.Decrypt(priv, blob)
	require.Error(t, err)
}

func TestECIES_TooShort(t *testing.T) {
	priv, _ := btcec.NewPrivateKey()
	_, err := preimage.Decrypt(priv, make([]byte, 10))
	require.Error(t, err)
}

func TestECIES_DifferentEphemeralEachCall(t *testing.T) {
	priv, _ := btcec.NewPrivateKey()
	pt := make([]byte, 64)
	_, _ = rand.Read(pt)
	a, err := preimage.Encrypt(priv.PubKey(), pt)
	require.NoError(t, err)
	b, err := preimage.Encrypt(priv.PubKey(), pt)
	require.NoError(t, err)
	assert.NotEqual(t, a, b, "two encryptions must differ (random ephemeral)")
}
