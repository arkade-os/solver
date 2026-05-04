package bounty

import (
	"bytes"
	"testing"

	"github.com/arkade-os/arkd/pkg/ark-lib/extension"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/txscript"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func freshP2TR(t *testing.T) []byte {
	t.Helper()
	priv, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	pkScript, err := txscript.PayToTaprootScript(priv.PubKey())
	require.NoError(t, err)
	return pkScript
}

func TestAnnouncement_RoundTrip(t *testing.T) {
	receiver := freshP2TR(t)
	preimage := bytes.Repeat([]byte{0x42}, 32)
	_ = preimage // not used in this design but keeps the byte vector handy

	tests := []struct {
		name string
		pkt  *Announcement
	}{
		{"min difficulty", &Announcement{Difficulty: 1, ReceiverPkScript: receiver}},
		{"mid difficulty", &Announcement{Difficulty: 4, ReceiverPkScript: receiver}},
		{"max difficulty", &Announcement{Difficulty: 32, ReceiverPkScript: receiver}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := tc.pkt.Serialize()
			require.NoError(t, err)
			decoded, err := DeserializeAnnouncement(data)
			require.NoError(t, err)
			assert.Equal(t, tc.pkt.Difficulty, decoded.Difficulty)
			assert.Equal(t, tc.pkt.ReceiverPkScript, decoded.ReceiverPkScript)
		})
	}
}

func TestAnnouncement_RejectMalformed(t *testing.T) {
	receiver := freshP2TR(t)
	good := &Announcement{Difficulty: 4, ReceiverPkScript: receiver}
	goodBytes, err := good.Serialize()
	require.NoError(t, err)

	tests := []struct {
		name string
		data []byte
	}{
		{"truncated header", goodBytes[:2]},
		{"truncated value", goodBytes[:len(goodBytes)-1]},
		{
			"unknown tag",
			append([]byte{0x99, 0x00, 0x01, 0x00}, goodBytes...),
		},
		{
			"missing receiverPkScript",
			append([]byte{}, []byte{tlvDifficulty, 0x00, 0x01, 0x04}...),
		},
		{
			"missing difficulty",
			func() []byte {
				var b []byte
				b = append(b, tlvReceiverPkScript, 0x00, 0x22)
				b = append(b, receiver...)
				return b
			}(),
		},
		{
			"wrong length difficulty",
			[]byte{tlvDifficulty, 0x00, 0x02, 0x04, 0x00,
				tlvReceiverPkScript, 0x00, 0x22},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DeserializeAnnouncement(tc.data)
			assert.Error(t, err)
		})
	}

	t.Run("difficulty out of range", func(t *testing.T) {
		_, err := DeserializeAnnouncement([]byte{
			tlvDifficulty, 0x00, 0x01, 0x00,
			tlvReceiverPkScript, 0x00, 0x22,
		})
		assert.Error(t, err)
	})

	t.Run("non-P2TR receiverPkScript", func(t *testing.T) {
		bad := make([]byte, 34)
		bad[0] = txscript.OP_0 // not OP_1
		bad[1] = 0x20
		_, err := DeserializeAnnouncement(buildPkt(t, 4, bad))
		assert.Error(t, err)
	})

	t.Run("wrong receiverPkScript length", func(t *testing.T) {
		_, err := DeserializeAnnouncement([]byte{
			tlvDifficulty, 0x00, 0x01, 0x04,
			tlvReceiverPkScript, 0x00, 0x21, // declares 33 bytes
		})
		assert.Error(t, err)
	})
}

func buildPkt(t *testing.T, difficulty uint8, receiver []byte) []byte {
	t.Helper()
	out := []byte{tlvDifficulty, 0x00, 0x01, difficulty}
	out = append(out, tlvReceiverPkScript, 0x00, byte(len(receiver)))
	out = append(out, receiver...)
	return out
}

func TestAnnouncement_SerializeValidates(t *testing.T) {
	t.Run("difficulty zero", func(t *testing.T) {
		_, err := (&Announcement{Difficulty: 0, ReceiverPkScript: freshP2TR(t)}).Serialize()
		assert.Error(t, err)
	})
	t.Run("difficulty over 32", func(t *testing.T) {
		_, err := (&Announcement{Difficulty: 33, ReceiverPkScript: freshP2TR(t)}).Serialize()
		assert.Error(t, err)
	})
	t.Run("bad receiver", func(t *testing.T) {
		_, err := (&Announcement{Difficulty: 4, ReceiverPkScript: []byte{0x00}}).Serialize()
		assert.Error(t, err)
	})
}

func TestFindAnnouncement(t *testing.T) {
	receiver := freshP2TR(t)
	pkt, err := (&Announcement{Difficulty: 4, ReceiverPkScript: receiver}).ToExtensionPacket()
	require.NoError(t, err)

	t.Run("present", func(t *testing.T) {
		ext, err := extension.NewExtensionFromPackets(pkt)
		require.NoError(t, err)
		got, err := FindAnnouncement(ext)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, uint8(4), got.Difficulty)
		assert.Equal(t, receiver, got.ReceiverPkScript)
	})

	t.Run("absent", func(t *testing.T) {
		other, err := (&MiningNoncePacket{Nonce: make([]byte, NonceSize)}).ToExtensionPacket()
		require.NoError(t, err)
		ext, err := extension.NewExtensionFromPackets(other)
		require.NoError(t, err)
		got, err := FindAnnouncement(ext)
		require.NoError(t, err)
		assert.Nil(t, got)
	})
}

func TestMiningNoncePacket_RoundTrip(t *testing.T) {
	nonce := []byte{0xde, 0xad, 0xbe, 0xef, 0xca, 0xfe, 0xba, 0xbe}
	pkt := &MiningNoncePacket{Nonce: nonce}
	data, err := pkt.Serialize()
	require.NoError(t, err)
	decoded, err := DeserializeMiningNoncePacket(data)
	require.NoError(t, err)
	assert.Equal(t, nonce, decoded.Nonce)
}

func TestMiningNoncePacket_RejectMalformed(t *testing.T) {
	t.Run("wrong nonce length", func(t *testing.T) {
		_, err := DeserializeMiningNoncePacket([]byte{tlvNonce, 0x00, 0x04, 0x00, 0x00, 0x00, 0x00})
		assert.Error(t, err)
	})
	t.Run("missing nonce", func(t *testing.T) {
		_, err := DeserializeMiningNoncePacket([]byte{})
		assert.Error(t, err)
	})
	t.Run("unknown tag", func(t *testing.T) {
		_, err := DeserializeMiningNoncePacket([]byte{0x99, 0x00, 0x00})
		assert.Error(t, err)
	})
}
