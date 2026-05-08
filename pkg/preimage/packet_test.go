package preimage_test

import (
	"testing"

	"github.com/arkade-os/arkd/pkg/ark-lib/extension"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arkade-os/bancod/pkg/preimage"
)

func TestClaimPacket_RoundTrip(t *testing.T) {
	in := preimage.ClaimPacket{
		Ciphertext: []byte{0xde, 0xad, 0xbe, 0xef},
		Taptree:    []string{"00aa", "11bb"},
	}
	raw, err := in.Serialize()
	require.NoError(t, err)
	out, err := preimage.DeserializeClaim(raw)
	require.NoError(t, err)
	assert.Equal(t, in.Ciphertext, out.Ciphertext)
	assert.Equal(t, in.Taptree, out.Taptree)
}

func TestClaimPacket_Type(t *testing.T) {
	p := preimage.ClaimPacket{}
	assert.Equal(t, uint8(0x04), p.Type())
}

func TestDeserializeClaim_Truncated(t *testing.T) {
	_, err := preimage.DeserializeClaim([]byte{0x01, 0x00})
	require.Error(t, err)
}

func TestDeserializeClaim_MissingFields(t *testing.T) {
	in := preimage.ClaimPacket{Ciphertext: []byte{0xaa}}
	raw, err := in.Serialize()
	if err == nil {
		_, err = preimage.DeserializeClaim(raw)
	}
	require.Error(t, err)
}

func TestFindClaim_Found(t *testing.T) {
	p := preimage.ClaimPacket{
		Ciphertext: []byte{0x01, 0x02},
		Taptree:    []string{"abcd"},
	}
	pkt, err := p.ToPacket()
	require.NoError(t, err)
	ext := extension.Extension{pkt}
	found, err := preimage.FindClaim(ext)
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, p.Ciphertext, found.Ciphertext)
}

func TestFindClaim_NotFound(t *testing.T) {
	other := extension.UnknownPacket{PacketType: 0xff, Data: []byte{0x00}}
	ext := extension.Extension{other}
	found, err := preimage.FindClaim(ext)
	require.NoError(t, err)
	require.Nil(t, found)
}
