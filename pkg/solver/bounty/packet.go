package bounty

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"

	"github.com/arkade-os/arkd/pkg/ark-lib/extension"
)

// TLV tags for Announcement.
const (
	tlvDifficulty       uint8 = 0x01
	tlvReceiverPkScript uint8 = 0x02
)

// TLV tags for MiningNoncePacket.
const (
	tlvNonce uint8 = 0x01
)

// NonceSize is the fixed nonce size used by the mining loop (uint64 BE counter).
const NonceSize = 8

// Announcement is Alice's announcement TLV: which receiver pkScript to pay
// and at what mining difficulty.
type Announcement struct {
	Difficulty       uint8
	ReceiverPkScript []byte // 34-byte P2TR
}

// Type implements extension.Packet.
func (p *Announcement) Type() uint8 { return AnnouncementPacketType }

// Serialize encodes the packet as a TLV stream. Tags are emitted in ascending
// order for round-trip determinism.
func (p *Announcement) Serialize() ([]byte, error) {
	if err := validateDifficulty(p.Difficulty); err != nil {
		return nil, err
	}
	if _, err := p2trWitnessProgram(p.ReceiverPkScript); err != nil {
		return nil, fmt.Errorf("invalid receiverPkScript: %w", err)
	}

	var buf bytes.Buffer
	encodeTLV(&buf, tlvDifficulty, []byte{p.Difficulty})
	encodeTLV(&buf, tlvReceiverPkScript, p.ReceiverPkScript)
	return buf.Bytes(), nil
}

// ToExtensionPacket wraps the packet for use inside an extension.Extension.
func (p *Announcement) ToExtensionPacket() (extension.Packet, error) {
	data, err := p.Serialize()
	if err != nil {
		return nil, err
	}
	return extension.UnknownPacket{PacketType: AnnouncementPacketType, Data: data}, nil
}

// DeserializeAnnouncement parses a TLV payload into an Announcement.
// Strict: unknown tags, wrong-length values, missing required fields → error.
func DeserializeAnnouncement(data []byte) (*Announcement, error) {
	pkt := &Announcement{}
	var hasDifficulty, hasReceiver bool

	err := walkTLV(data, func(tag uint8, value []byte) error {
		switch tag {
		case tlvDifficulty:
			if len(value) != 1 {
				return fmt.Errorf("invalid difficulty: expected 1 byte, got %d", len(value))
			}
			if err := validateDifficulty(value[0]); err != nil {
				return err
			}
			pkt.Difficulty = value[0]
			hasDifficulty = true
		case tlvReceiverPkScript:
			if len(value) != 34 {
				return fmt.Errorf("invalid receiverPkScript: expected 34 bytes, got %d", len(value))
			}
			if _, err := p2trWitnessProgram(value); err != nil {
				return fmt.Errorf("invalid receiverPkScript: %w", err)
			}
			pkt.ReceiverPkScript = slices.Clone(value)
			hasReceiver = true
		default:
			return fmt.Errorf("unknown TLV tag: 0x%02x", tag)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !hasDifficulty {
		return nil, errors.New("missing required field: difficulty")
	}
	if !hasReceiver {
		return nil, errors.New("missing required field: receiverPkScript")
	}
	return pkt, nil
}

// FindAnnouncement scans an Extension for a bounty announcement packet. Returns
// (nil, nil) when no announcement is present (not an error — the tx just isn't
// a bounty announcement).
func FindAnnouncement(ext extension.Extension) (*Announcement, error) {
	p := ext.GetPacketByType(AnnouncementPacketType)
	if p == nil {
		return nil, nil
	}
	unknown, ok := p.(extension.UnknownPacket)
	if !ok {
		return nil, fmt.Errorf("announcement packet has unexpected concrete type %T", p)
	}
	return DeserializeAnnouncement(unknown.Data)
}

// MiningNoncePacket carries the mining nonce in the bot's claim tx extension.
type MiningNoncePacket struct {
	Nonce []byte // exactly NonceSize bytes
}

// Type implements extension.Packet.
func (p *MiningNoncePacket) Type() uint8 { return MiningNoncePacketType }

// Serialize encodes the nonce packet as a TLV stream.
func (p *MiningNoncePacket) Serialize() ([]byte, error) {
	if len(p.Nonce) != NonceSize {
		return nil, fmt.Errorf("invalid nonce: expected %d bytes, got %d", NonceSize, len(p.Nonce))
	}
	var buf bytes.Buffer
	encodeTLV(&buf, tlvNonce, p.Nonce)
	return buf.Bytes(), nil
}

// ToExtensionPacket wraps the packet for use inside an extension.Extension.
func (p *MiningNoncePacket) ToExtensionPacket() (extension.Packet, error) {
	data, err := p.Serialize()
	if err != nil {
		return nil, err
	}
	return extension.UnknownPacket{PacketType: MiningNoncePacketType, Data: data}, nil
}

// DeserializeMiningNoncePacket parses a TLV payload into a MiningNoncePacket.
func DeserializeMiningNoncePacket(data []byte) (*MiningNoncePacket, error) {
	pkt := &MiningNoncePacket{}
	var hasNonce bool

	err := walkTLV(data, func(tag uint8, value []byte) error {
		switch tag {
		case tlvNonce:
			if len(value) != NonceSize {
				return fmt.Errorf("invalid nonce: expected %d bytes, got %d", NonceSize, len(value))
			}
			pkt.Nonce = slices.Clone(value)
			hasNonce = true
		default:
			return fmt.Errorf("unknown TLV tag: 0x%02x", tag)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !hasNonce {
		return nil, errors.New("missing required field: nonce")
	}
	return pkt, nil
}

// walkTLV iterates over a TLV stream calling handle for each (tag, value) entry.
// Returns the first decoder error or any error returned by handle.
func walkTLV(data []byte, handle func(tag uint8, value []byte) error) error {
	for offset := 0; offset < len(data); {
		if offset+3 > len(data) {
			return errors.New("truncated TLV: not enough bytes for type+length header")
		}
		tag := data[offset]
		length := int(binary.BigEndian.Uint16(data[offset+1 : offset+3]))
		offset += 3
		if offset+length > len(data) {
			return fmt.Errorf("truncated TLV: type 0x%02x expected %d bytes, got %d",
				tag, length, len(data)-offset)
		}
		if err := handle(tag, data[offset:offset+length]); err != nil {
			return err
		}
		offset += length
	}
	return nil
}

func encodeTLV(buf *bytes.Buffer, tlvType uint8, value []byte) {
	buf.WriteByte(tlvType)
	_ = binary.Write(buf, binary.BigEndian, uint16(len(value)))
	buf.Write(value)
}
