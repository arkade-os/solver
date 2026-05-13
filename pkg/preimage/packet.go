package preimage

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/arkade-os/arkd/pkg/ark-lib/extension"
)

// PacketType is the Ark extension TLV type for a preimage claim.
const PacketType uint8 = 0x04

const (
	tlvCiphertext   byte = 0x01
	tlvArkadeScript byte = 0x02
)

// ClaimPacket carries the per-tx data the solver needs to claim a
// preimage-gated VTXO:
//   - Ciphertext: ECIES(solverPub, 32-byte preimage)
//   - ArkadeScript: plaintext enforcePayTo(receiverPk) bytes; needed by
//     the introspector to evaluate the spend, and used by the plugin
//     to validate the tap tree binding.
//
// The tap tree no longer rides in the packet — it travels in the
// funding output's POutput.TaprootTapTree (BIP-371).
type ClaimPacket struct {
	Ciphertext   []byte
	ArkadeScript []byte
}

func (p *ClaimPacket) Type() uint8 { return PacketType }

func (p *ClaimPacket) Serialize() ([]byte, error) {
	if len(p.Ciphertext) == 0 {
		return nil, errors.New("ciphertext must not be empty")
	}
	if len(p.ArkadeScript) == 0 {
		return nil, errors.New("arkade_script must not be empty")
	}
	buf := &bytes.Buffer{}
	encodeTLV(buf, tlvCiphertext, p.Ciphertext)
	encodeTLV(buf, tlvArkadeScript, p.ArkadeScript)
	return buf.Bytes(), nil
}

func (p *ClaimPacket) ToPacket() (extension.Packet, error) {
	body, err := p.Serialize()
	if err != nil {
		return nil, err
	}
	return extension.UnknownPacket{PacketType: PacketType, Data: body}, nil
}

func DeserializeClaim(data []byte) (*ClaimPacket, error) {
	out := &ClaimPacket{}
	hasCiphertext := false
	hasArkadeScript := false

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
		case tlvArkadeScript:
			out.ArkadeScript = val
			hasArkadeScript = true
		}
	}

	if !hasCiphertext {
		return nil, errors.New("missing ciphertext TLV (0x01)")
	}
	if !hasArkadeScript {
		return nil, errors.New("missing arkade_script TLV (0x02)")
	}
	return out, nil
}

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
