package preimage

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/arkade-os/arkd/pkg/ark-lib/extension"
)

// PacketType is the Ark extension TLV type for a preimage claim. Distinct from
// the banco offer's 0x03.
const PacketType uint8 = 0x04

const (
	tlvCiphertext byte = 0x01
	tlvTaptree    byte = 0x02
)

// ClaimPacket carries everything the bot needs to claim a preimage-gated VTXO.
//   - Ciphertext is the ECIES output of (preimage || varint(len) || arkadeScript).
//   - Taptree is the same []string of hex-encoded closure scripts that
//     TapscriptsVtxoScript.Encode() produces.
type ClaimPacket struct {
	Ciphertext []byte
	Taptree    []string
}

func (p *ClaimPacket) Type() uint8 { return PacketType }

// Serialize emits the TLV body. Both ciphertext and taptree are required.
func (p *ClaimPacket) Serialize() ([]byte, error) {
	if len(p.Ciphertext) == 0 {
		return nil, errors.New("ciphertext must not be empty")
	}
	if len(p.Taptree) == 0 {
		return nil, errors.New("taptree must not be empty")
	}
	buf := &bytes.Buffer{}
	encodeTLV(buf, tlvCiphertext, p.Ciphertext)
	taptreeBytes, err := json.Marshal(p.Taptree)
	if err != nil {
		return nil, fmt.Errorf("marshal taptree: %w", err)
	}
	encodeTLV(buf, tlvTaptree, taptreeBytes)
	return buf.Bytes(), nil
}

// ToPacket wraps the serialized body in an extension.UnknownPacket.
func (p *ClaimPacket) ToPacket() (extension.Packet, error) {
	body, err := p.Serialize()
	if err != nil {
		return nil, err
	}
	return extension.UnknownPacket{PacketType: PacketType, Data: body}, nil
}

// DeserializeClaim parses the TLV body produced by Serialize.
func DeserializeClaim(data []byte) (*ClaimPacket, error) {
	out := &ClaimPacket{}
	hasCiphertext := false
	hasTaptree := false

	offset := 0
	for offset < len(data) {
		if offset+3 > len(data) {
			return nil, errors.New("truncated TLV: not enough bytes for type+length header")
		}
		tlvType := data[offset]
		tlvLen := int(binary.BigEndian.Uint16(data[offset+1 : offset+3]))
		offset += 3
		if offset+tlvLen > len(data) {
			return nil, fmt.Errorf("truncated TLV: type 0x%02x wants %d bytes, %d left",
				tlvType, tlvLen, len(data)-offset)
		}
		val := make([]byte, tlvLen)
		copy(val, data[offset:offset+tlvLen])
		offset += tlvLen

		switch tlvType {
		case tlvCiphertext:
			out.Ciphertext = val
			hasCiphertext = true
		case tlvTaptree:
			var scripts []string
			if err := json.Unmarshal(val, &scripts); err != nil {
				return nil, fmt.Errorf("decode taptree: %w", err)
			}
			out.Taptree = scripts
			hasTaptree = true
		}
	}

	if !hasCiphertext {
		return nil, errors.New("missing ciphertext TLV (0x01)")
	}
	if !hasTaptree {
		return nil, errors.New("missing taptree TLV (0x02)")
	}
	return out, nil
}

// FindClaim searches the parsed Ark extension for a packet of type 0x04 and
// deserializes it. Returns (nil, nil) when no such packet is present.
func FindClaim(ext extension.Extension) (*ClaimPacket, error) {
	p := ext.GetPacketByType(PacketType)
	if p == nil {
		return nil, nil
	}
	unknown, ok := p.(extension.UnknownPacket)
	if !ok {
		return nil, fmt.Errorf(
			"preimage packet (type 0x%02x) has unexpected concrete type %T",
			PacketType, p,
		)
	}
	return DeserializeClaim(unknown.Data)
}

func encodeTLV(buf *bytes.Buffer, tlvType byte, value []byte) {
	buf.WriteByte(tlvType)
	hdr := make([]byte, 2)
	binary.BigEndian.PutUint16(hdr, uint16(len(value)))
	buf.Write(hdr)
	buf.Write(value)
}
