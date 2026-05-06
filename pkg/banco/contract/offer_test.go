package contract

import (
	"bytes"
	"encoding/binary"
	"testing"

	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/arkade-os/arkd/pkg/ark-lib/asset"
	"github.com/arkade-os/arkd/pkg/ark-lib/extension"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// TLV building helpers
// ---------------------------------------------------------------------------

type tlvField struct {
	typ   byte
	value []byte
}

func buildTLV(fields ...tlvField) []byte {
	var buf []byte
	for _, f := range fields {
		buf = append(buf, f.typ)
		lenBuf := make([]byte, 2)
		binary.BigEndian.PutUint16(lenBuf, uint16(len(f.value)))
		buf = append(buf, lenBuf...)
		buf = append(buf, f.value...)
	}
	return buf
}

func wantAmountBytes(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

// ---------------------------------------------------------------------------
// Task 2: DeserializeOffer + Serialize roundtrip tests
// ---------------------------------------------------------------------------

func TestRoundtrip_MinimalOffer(t *testing.T) {
	orig := testMinimalOffer(t)

	data, err := orig.Serialize()
	require.NoError(t, err)
	require.NotEmpty(t, data)

	got, err := DeserializeOffer(data)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, orig.SwapPkScript, got.SwapPkScript)
	assert.Equal(t, orig.WantAmount, got.WantAmount)
	assert.Equal(t, orig.MakerPkScript, got.MakerPkScript)
	// x-only comparison: schnorr.ParsePubKey normalises to even parity
	assert.Equal(t, schnorr.SerializePubKey(orig.IntrospectorPubkey), schnorr.SerializePubKey(got.IntrospectorPubkey))
	assert.Nil(t, got.WantAsset)
	assert.Nil(t, got.OfferAsset)
	assert.Nil(t, got.ExitDelay)
	assert.Nil(t, got.MakerPublicKey)
	assert.Equal(t, uint64(0), got.CancelAt)
	assert.Equal(t, uint64(0), got.RatioNum)
	assert.Equal(t, uint64(0), got.RatioDen)
}

func TestRoundtrip_FullOffer(t *testing.T) {
	orig := testFullOffer(t)

	data, err := orig.Serialize()
	require.NoError(t, err)
	require.NotEmpty(t, data)

	got, err := DeserializeOffer(data)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, orig.SwapPkScript, got.SwapPkScript)
	assert.Equal(t, orig.WantAmount, got.WantAmount)
	assert.Equal(t, orig.MakerPkScript, got.MakerPkScript)
	// x-only comparison: schnorr.ParsePubKey normalises to even parity
	assert.Equal(t, schnorr.SerializePubKey(orig.IntrospectorPubkey), schnorr.SerializePubKey(got.IntrospectorPubkey))
	assert.Equal(t, orig.CancelAt, got.CancelAt)
	assert.Equal(t, orig.RatioNum, got.RatioNum)
	assert.Equal(t, orig.RatioDen, got.RatioDen)
	require.NotNil(t, got.MakerPublicKey)
	assert.Equal(t, schnorr.SerializePubKey(orig.MakerPublicKey), schnorr.SerializePubKey(got.MakerPublicKey))
	require.NotNil(t, got.ExitDelay)
	assert.Equal(t, orig.ExitDelay.Type, got.ExitDelay.Type)
	assert.Equal(t, orig.ExitDelay.Value, got.ExitDelay.Value)
}

// ---------------------------------------------------------------------------
// Missing required field tests
// ---------------------------------------------------------------------------

func TestDeserializeOffer_MissingRequiredField(t *testing.T) {
	minimal := testMinimalOffer(t)
	introBytes := testXOnlyBytes(minimal.IntrospectorPubkey)

	amountBytes := wantAmountBytes(minimal.WantAmount)

	swapField := tlvField{typ: tlvSwapPkScript, value: minimal.SwapPkScript}
	wantField := tlvField{typ: tlvWantAmount, value: amountBytes}
	makerField := tlvField{typ: tlvMakerPkScript, value: minimal.MakerPkScript}
	introField := tlvField{typ: tlvIntrospectorPubkey, value: introBytes}

	tests := []struct {
		name   string
		fields []tlvField
		errMsg string
	}{
		{
			name:   "missing swapPkScript",
			fields: []tlvField{wantField, makerField, introField},
			errMsg: "missing required field: swapPkScript",
		},
		{
			name:   "missing wantAmount",
			fields: []tlvField{swapField, makerField, introField},
			errMsg: "missing required field: wantAmount",
		},
		{
			name:   "missing makerPkScript",
			fields: []tlvField{swapField, wantField, introField},
			errMsg: "missing required field: makerPkScript",
		},
		{
			name:   "missing introspectorPubkey",
			fields: []tlvField{swapField, wantField, makerField},
			errMsg: "missing required field: introspectorPubkey",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := buildTLV(tc.fields...)
			_, err := DeserializeOffer(data)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.errMsg)
		})
	}
}

// ---------------------------------------------------------------------------
// Invalid field length tests
// ---------------------------------------------------------------------------

func TestDeserializeOffer_InvalidFieldLength(t *testing.T) {
	minimal := testMinimalOffer(t)
	introBytes := testXOnlyBytes(minimal.IntrospectorPubkey)
	amountBytes := wantAmountBytes(minimal.WantAmount)

	swapField := tlvField{typ: tlvSwapPkScript, value: minimal.SwapPkScript}
	makerField := tlvField{typ: tlvMakerPkScript, value: minimal.MakerPkScript}
	introField := tlvField{typ: tlvIntrospectorPubkey, value: introBytes}
	wantField := tlvField{typ: tlvWantAmount, value: amountBytes}

	tests := []struct {
		name   string
		fields []tlvField
		errMsg string
	}{
		{
			name: "wantAmount not 8 bytes",
			fields: []tlvField{
				swapField,
				{typ: tlvWantAmount, value: []byte{0x01, 0x02, 0x03}},
				makerField,
				introField,
			},
			errMsg: "invalid wantAmount",
		},
		{
			name: "cancelDelay not 8 bytes",
			fields: []tlvField{
				swapField,
				wantField,
				makerField,
				introField,
				{typ: tlvCancelDelay, value: []byte{0x01}},
			},
			errMsg: "invalid cancelDelay",
		},
		{
			name: "ratioNum not 8 bytes",
			fields: []tlvField{
				swapField,
				wantField,
				makerField,
				introField,
				{typ: tlvRatioNum, value: []byte{0x01, 0x02}},
			},
			errMsg: "invalid ratioNum",
		},
		{
			name: "ratioDen not 8 bytes",
			fields: []tlvField{
				swapField,
				wantField,
				makerField,
				introField,
				{typ: tlvRatioDen, value: []byte{0x01, 0x02}},
			},
			errMsg: "invalid ratioDen",
		},
		{
			name: "introspectorPubkey not 32 bytes",
			fields: []tlvField{
				swapField,
				wantField,
				makerField,
				{typ: tlvIntrospectorPubkey, value: []byte{0x01, 0x02}},
			},
			errMsg: "invalid introspectorPubkey",
		},
		{
			name: "exitTimelock not 9 bytes",
			fields: []tlvField{
				swapField,
				wantField,
				makerField,
				introField,
				{typ: tlvExitTimelock, value: []byte{0x00, 0x01}},
			},
			errMsg: "invalid exitTimelock",
		},
		{
			name: "makerPkScript not 34 bytes",
			fields: []tlvField{
				swapField,
				wantField,
				{typ: tlvMakerPkScript, value: []byte{0x01, 0x02}},
				introField,
			},
			errMsg: "invalid makerPkScript",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := buildTLV(tc.fields...)
			_, err := DeserializeOffer(data)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.errMsg)
		})
	}
}

// ---------------------------------------------------------------------------
// Truncated input tests
// ---------------------------------------------------------------------------

func TestDeserializeOffer_TruncatedInput(t *testing.T) {
	t.Run("header truncated (only 2 bytes)", func(t *testing.T) {
		data := []byte{tlvSwapPkScript, 0x00}
		_, err := DeserializeOffer(data)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "truncated TLV")
	})

	t.Run("value truncated", func(t *testing.T) {
		// Claim value length is 10 but only provide 2 bytes of value
		data := []byte{tlvSwapPkScript, 0x00, 0x0A, 0xAB, 0xCD}
		_, err := DeserializeOffer(data)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "truncated TLV")
	})
}

// ---------------------------------------------------------------------------
// Unknown TLV type test
// ---------------------------------------------------------------------------

func TestDeserializeOffer_UnknownType(t *testing.T) {
	minimal := testMinimalOffer(t)
	introBytes := testXOnlyBytes(minimal.IntrospectorPubkey)
	amountBytes := wantAmountBytes(minimal.WantAmount)

	data := buildTLV(
		tlvField{typ: tlvSwapPkScript, value: minimal.SwapPkScript},
		tlvField{typ: tlvWantAmount, value: amountBytes},
		tlvField{typ: tlvMakerPkScript, value: minimal.MakerPkScript},
		tlvField{typ: tlvIntrospectorPubkey, value: introBytes},
		tlvField{typ: 0xFF, value: []byte{0x01}},
	)
	_, err := DeserializeOffer(data)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown TLV type")
}

// ---------------------------------------------------------------------------
// Exit timelock type roundtrip tests
// ---------------------------------------------------------------------------

func TestRoundtrip_ExitTimelockTypeBlock(t *testing.T) {
	orig := testMinimalOffer(t)
	orig.ExitDelay = &arklib.RelativeLocktime{
		Type:  arklib.LocktimeTypeBlock,
		Value: 144,
	}

	data, err := orig.Serialize()
	require.NoError(t, err)

	got, err := DeserializeOffer(data)
	require.NoError(t, err)
	require.NotNil(t, got.ExitDelay)
	assert.Equal(t, arklib.LocktimeTypeBlock, got.ExitDelay.Type)
	assert.Equal(t, uint32(144), got.ExitDelay.Value)
}

func TestRoundtrip_ExitTimelockTypeSecond(t *testing.T) {
	orig := testMinimalOffer(t)
	orig.ExitDelay = &arklib.RelativeLocktime{
		Type:  arklib.LocktimeTypeSecond,
		Value: 512,
	}

	data, err := orig.Serialize()
	require.NoError(t, err)

	got, err := DeserializeOffer(data)
	require.NoError(t, err)
	require.NotNil(t, got.ExitDelay)
	assert.Equal(t, arklib.LocktimeTypeSecond, got.ExitDelay.Type)
	assert.Equal(t, uint32(512), got.ExitDelay.Value)
}

// ---------------------------------------------------------------------------
// Roundtrip with WantAsset and OfferAsset
// ---------------------------------------------------------------------------

func newTestAssetId(t *testing.T) *asset.AssetId {
	t.Helper()
	assetBytes := make([]byte, 34)
	assetBytes[0] = 0xAB
	for i := 1; i < 32; i++ {
		assetBytes[i] = byte(i)
	}
	id, err := asset.NewAssetIdFromBytes(assetBytes)
	require.NoError(t, err)
	return id
}

func TestRoundtrip_WantAsset(t *testing.T) {
	orig := testMinimalOffer(t)
	orig.WantAsset = newTestAssetId(t)

	data, err := orig.Serialize()
	require.NoError(t, err)

	got, err := DeserializeOffer(data)
	require.NoError(t, err)
	require.NotNil(t, got.WantAsset)
	assert.Equal(t, orig.WantAsset.String(), got.WantAsset.String())
}

func TestRoundtrip_OfferAsset(t *testing.T) {
	orig := testMinimalOffer(t)
	orig.OfferAsset = newTestAssetId(t)

	data, err := orig.Serialize()
	require.NoError(t, err)

	got, err := DeserializeOffer(data)
	require.NoError(t, err)
	require.NotNil(t, got.OfferAsset)
	assert.Equal(t, orig.OfferAsset.String(), got.OfferAsset.String())
}

// ---------------------------------------------------------------------------
// Task 3: ToPacket, FulfillScript, VtxoScript, FindBancoOffer tests
// ---------------------------------------------------------------------------

func TestToPacket(t *testing.T) {
	orig := testMinimalOffer(t)

	pkt, err := orig.ToPacket()
	require.NoError(t, err)
	require.NotNil(t, pkt)

	assert.Equal(t, uint8(PacketType), pkt.Type())

	unknown, ok := pkt.(extension.UnknownPacket)
	require.True(t, ok, "expected UnknownPacket concrete type")

	// Data roundtrips back to the same offer
	got, err := DeserializeOffer(unknown.Data)
	require.NoError(t, err)
	assert.Equal(t, orig.WantAmount, got.WantAmount)
	assert.Equal(t, orig.SwapPkScript, got.SwapPkScript)
}

func TestFulfillScript_BTCPath(t *testing.T) {
	o := testMinimalOffer(t)
	o.WantAsset = nil // explicit BTC path

	script, err := o.FulfillScript()
	require.NoError(t, err)
	require.NotEmpty(t, script)
}

func TestFulfillScript_AssetPath(t *testing.T) {
	o := testMinimalOffer(t)

	// Build a valid 34-byte asset ID
	assetBytes := make([]byte, 34)
	assetBytes[0] = 0xAB
	for i := 1; i < 32; i++ {
		assetBytes[i] = byte(i)
	}
	// last 2 bytes are index (little-endian uint16), leave as 0

	assetId, err := asset.NewAssetIdFromBytes(assetBytes)
	require.NoError(t, err)
	o.WantAsset = assetId

	scriptAsset, err := o.FulfillScript()
	require.NoError(t, err)
	require.NotEmpty(t, scriptAsset)

	// BTC and asset scripts must differ
	o2 := testMinimalOffer(t)
	o2.WantAmount = o.WantAmount
	o2.MakerPkScript = o.MakerPkScript
	o2.WantAsset = nil
	scriptBTC, err := o2.FulfillScript()
	require.NoError(t, err)
	assert.False(t, bytes.Equal(scriptBTC, scriptAsset), "BTC and asset fulfill scripts should differ")
}

func TestVtxoScript_FulfillOnly(t *testing.T) {
	_, serverPub := testKeyPair(t)
	o := testMinimalOffer(t)
	// no CancelAt, no ExitDelay

	vtxo, err := o.VtxoScript(serverPub)
	require.NoError(t, err)
	require.NotNil(t, vtxo)
	require.Len(t, vtxo.Closures, 1, "fulfill-only: expect 1 closure")
}

func TestVtxoScript_WithCancel(t *testing.T) {
	_, serverPub := testKeyPair(t)
	_, makerPub := testKeyPair(t)
	o := testMinimalOffer(t)
	o.CancelAt = 1700000000
	o.MakerPublicKey = makerPub

	vtxo, err := o.VtxoScript(serverPub)
	require.NoError(t, err)
	require.NotNil(t, vtxo)
	require.Len(t, vtxo.Closures, 2, "with cancel: expect 2 closures")
}

func TestVtxoScript_WithExit(t *testing.T) {
	_, serverPub := testKeyPair(t)
	_, makerPub := testKeyPair(t)
	o := testMinimalOffer(t)
	o.ExitDelay = &arklib.RelativeLocktime{
		Type:  arklib.LocktimeTypeBlock,
		Value: 144,
	}
	o.MakerPublicKey = makerPub

	vtxo, err := o.VtxoScript(serverPub)
	require.NoError(t, err)
	require.NotNil(t, vtxo)
	require.Len(t, vtxo.Closures, 2, "with exit: expect 2 closures")
}

func TestVtxoScript_WithBoth(t *testing.T) {
	_, serverPub := testKeyPair(t)
	_, makerPub := testKeyPair(t)
	o := testMinimalOffer(t)
	o.CancelAt = 1700000000
	o.ExitDelay = &arklib.RelativeLocktime{
		Type:  arklib.LocktimeTypeBlock,
		Value: 144,
	}
	o.MakerPublicKey = makerPub

	vtxo, err := o.VtxoScript(serverPub)
	require.NoError(t, err)
	require.NotNil(t, vtxo)
	require.Len(t, vtxo.Closures, 3, "with both cancel and exit: expect 3 closures")
}

// ---------------------------------------------------------------------------
// FindBancoOffer tests
// ---------------------------------------------------------------------------

func TestFindBancoOffer_Found(t *testing.T) {
	orig := testMinimalOffer(t)

	pkt, err := orig.ToPacket()
	require.NoError(t, err)

	ext := extension.Extension{pkt}

	found, err := FindBancoOffer(ext)
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, orig.WantAmount, found.WantAmount)
	assert.Equal(t, orig.SwapPkScript, found.SwapPkScript)
}

func TestFindBancoOffer_NotFound_DifferentType(t *testing.T) {
	// Extension contains a packet with type 0xFF, not PacketType
	pkt := extension.UnknownPacket{PacketType: 0xFF, Data: []byte{0x01}}
	ext := extension.Extension{pkt}

	found, err := FindBancoOffer(ext)
	require.NoError(t, err)
	assert.Nil(t, found)
}

func TestFindBancoOffer_EmptyExtension(t *testing.T) {
	ext := extension.Extension{}

	found, err := FindBancoOffer(ext)
	require.NoError(t, err)
	assert.Nil(t, found)
}

// customPacket implements extension.Packet with the banco PacketType
// but is not an extension.UnknownPacket — exercises the !ok branch in FindBancoOffer.
type customPacket struct{}

func (customPacket) Type() uint8                { return PacketType }
func (customPacket) Serialize() ([]byte, error) { return []byte{}, nil }

func TestFindBancoOffer_UnexpectedConcreteType(t *testing.T) {
	ext := extension.Extension{customPacket{}}

	_, err := FindBancoOffer(ext)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected concrete type")
}
