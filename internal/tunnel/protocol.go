package tunnel

import (
	"encoding/binary"
	"fmt"
	"io"
	"sync/atomic"
)

// Frame header layout:
//
//	ConnID  (4 bytes) — unique connection identifier
//	SeqNum  (4 bytes) — sequence number for ordering
//	Flags   (1 byte)  — frame type flags
//	Length  (2 bytes) — payload length
//
// Total header: 11 bytes.
const HeaderSize = 11

// Maximum payload per frame (8 KB).
const MaxPayloadSize = 8192

// Flag constants.
const (
	FlagSYN     byte = 0x01 // new connection (payload = ATYP + ADDR + PORT)
	FlagFIN     byte = 0x02 // connection closed gracefully
	FlagRST     byte = 0x04 // connection reset / error
	FlagACK     byte = 0x08 // acknowledgement
	FlagReverse byte = 0x10 // data flowing from server to client
	FlagData    byte = 0x00 // regular data (no special flag needed)
)

// Frame represents a single framed packet in the tunnel protocol.
type Frame struct {
	ConnID  uint32
	SeqNum  uint32
	Flags   byte
	Payload []byte
}

// IsSYN returns true if this frame initiates a new connection.
func (f *Frame) IsSYN() bool { return f.Flags&FlagSYN != 0 }

// IsFIN returns true if this frame terminates a connection.
func (f *Frame) IsFIN() bool { return f.Flags&FlagFIN != 0 }

// IsRST returns true if this is a reset/error frame.
func (f *Frame) IsRST() bool { return f.Flags&FlagRST != 0 }

// IsACK returns true if this is an acknowledgement frame.
func (f *Frame) IsACK() bool { return f.Flags&FlagACK != 0 }

// IsReverse returns true if data flows from server to client.
func (f *Frame) IsReverse() bool { return f.Flags&FlagReverse != 0 }

// WriteFrame encodes the frame and writes it to w in a single write.
// Wire format: [ConnID:4][SeqNum:4][Flags:1][Length:2][Payload:Length]
func WriteFrame(w io.Writer, f *Frame) error {
	if len(f.Payload) > MaxPayloadSize {
		return fmt.Errorf("payload too large: %d > %d", len(f.Payload), MaxPayloadSize)
	}

	// Build entire frame in one buffer so it goes as a single TCP write
	buf := make([]byte, HeaderSize+len(f.Payload))
	binary.BigEndian.PutUint32(buf[0:4], f.ConnID)
	binary.BigEndian.PutUint32(buf[4:8], f.SeqNum)
	buf[8] = f.Flags
	binary.BigEndian.PutUint16(buf[9:11], uint16(len(f.Payload)))
	if len(f.Payload) > 0 {
		copy(buf[HeaderSize:], f.Payload)
	}

	_, err := w.Write(buf)
	return err
}

// ReadFrame reads one frame from r.
// Returns io.EOF if the reader is closed cleanly before the header.
func ReadFrame(r io.Reader) (*Frame, error) {
	var hdr [HeaderSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}

	length := binary.BigEndian.Uint16(hdr[9:11])
	if length > MaxPayloadSize {
		return nil, fmt.Errorf("frame payload too large: %d", length)
	}

	var payload []byte
	if length > 0 {
		payload = make([]byte, length)
		if _, err := io.ReadFull(r, payload); err != nil {
			return nil, fmt.Errorf("read payload: %w", err)
		}
	}

	return &Frame{
		ConnID:  binary.BigEndian.Uint32(hdr[0:4]),
		SeqNum:  binary.BigEndian.Uint32(hdr[4:8]),
		Flags:   hdr[8],
		Payload: payload,
	}, nil
}

// ConnIDGenerator produces unique connection IDs.
type ConnIDGenerator struct {
	counter atomic.Uint32
}

// Next returns the next unique connection ID.
func (g *ConnIDGenerator) Next() uint32 {
	return g.counter.Add(1)
}

// EncodeSYNPayload builds the SYN frame payload: ATYP(1) + ADDR(var) + PORT(2).
func EncodeSYNPayload(atyp byte, addr []byte, port []byte) []byte {
	payload := make([]byte, 0, 1+len(addr)+2)
	payload = append(payload, atyp)
	payload = append(payload, addr...)
	payload = append(payload, port...)
	return payload
}

// DecodeSYNPayload extracts ATYP, address bytes, and port from a SYN payload.
func DecodeSYNPayload(payload []byte) (atyp byte, addr []byte, port []byte, err error) {
	if len(payload) < 4 { // minimum: ATYP(1) + IPv4(4) would be 5, but at least need 4
		return 0, nil, nil, fmt.Errorf("SYN payload too short: %d", len(payload))
	}
	atyp = payload[0]
	var addrLen int
	switch atyp {
	case 0x01: // IPv4
		addrLen = 4
	case 0x03: // Domain
		if len(payload) < 2 {
			return 0, nil, nil, fmt.Errorf("domain SYN payload too short")
		}
		addrLen = 1 + int(payload[1]) // length byte + domain
	case 0x04: // IPv6
		addrLen = 16
	default:
		return 0, nil, nil, fmt.Errorf("unknown ATYP: 0x%02x", atyp)
	}

	if len(payload) < 1+addrLen+2 {
		return 0, nil, nil, fmt.Errorf("SYN payload truncated: need %d, have %d",
			1+addrLen+2, len(payload))
	}

	addr = payload[1 : 1+addrLen]
	port = payload[1+addrLen : 1+addrLen+2]
	return atyp, addr, port, nil
}
