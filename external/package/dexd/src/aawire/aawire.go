// Package aawire reads, writes, splits and reassembles Android Auto frames.
//
// A frame is [channel u8][flags u8][length u16 BE], then [total u32 BE] only
// when it is the first of several fragments, then the payload.
package aawire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	FlagFirst     uint8 = 0x01
	FlagLast      uint8 = 0x02
	FlagControl   uint8 = 0x04
	FlagEncrypted uint8 = 0x08
)

// MaxPayload is the most plaintext carried by one frame.
const MaxPayload = 16384

// maxMessage bounds a reassembled message so a bad length cannot exhaust memory.
const maxMessage = 16 << 20

// Frame is one frame on the wire. Total is meaningful only on a frame that
// has FlagFirst without FlagLast.
type Frame struct {
	Channel uint8
	Flags   uint8
	Total   uint32
	Payload []byte
}

func (f Frame) firstOfMany() bool { return f.Flags&(FlagFirst|FlagLast) == FlagFirst }

// ReadFrame reads exactly one frame.
func ReadFrame(r io.Reader) (Frame, error) {
	var h [4]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return Frame{}, err
	}
	f := Frame{Channel: h[0], Flags: h[1]}
	n := int(binary.BigEndian.Uint16(h[2:]))
	if f.firstOfMany() {
		var t [4]byte
		if _, err := io.ReadFull(r, t[:]); err != nil {
			return Frame{}, unexpected(err)
		}
		f.Total = binary.BigEndian.Uint32(t[:])
	}
	f.Payload = make([]byte, n)
	if _, err := io.ReadFull(r, f.Payload); err != nil {
		return Frame{}, unexpected(err)
	}
	return f, nil
}

func unexpected(err error) error {
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}

// WriteFrame writes one frame with a single Write call.
func WriteFrame(w io.Writer, f Frame) error {
	if len(f.Payload) > 0xffff {
		return fmt.Errorf("aawire: payload of %d bytes does not fit one frame", len(f.Payload))
	}
	b := make([]byte, 0, 8+len(f.Payload))
	b = append(b, f.Channel, f.Flags)
	b = binary.BigEndian.AppendUint16(b, uint16(len(f.Payload)))
	if f.firstOfMany() {
		b = binary.BigEndian.AppendUint32(b, f.Total)
	}
	b = append(b, f.Payload...)
	_, err := w.Write(b)
	return err
}

// Split cuts a message into frames of at most MaxPayload bytes and sets the
// fragment flags on top of baseFlags.
func Split(channel, baseFlags uint8, msg []byte) []Frame {
	if len(msg) <= MaxPayload {
		return []Frame{{Channel: channel, Flags: baseFlags | FlagFirst | FlagLast, Payload: msg}}
	}
	var out []Frame
	for off := 0; off < len(msg); off += MaxPayload {
		end := off + MaxPayload
		f := Frame{Channel: channel, Flags: baseFlags}
		if off == 0 {
			f.Flags |= FlagFirst
			f.Total = uint32(len(msg))
		}
		if end >= len(msg) {
			end = len(msg)
			f.Flags |= FlagLast
		}
		f.Payload = msg[off:end]
		out = append(out, f)
	}
	return out
}

// Assembler joins fragments back into messages, one buffer per channel, so
// fragments of different channels may be interleaved.
type Assembler struct {
	parts map[uint8][]byte
}

// Add takes the next frame (already decrypted). done is true when a whole
// message is returned.
func (a *Assembler) Add(f Frame) (channel uint8, msg []byte, done bool, err error) {
	first, last := f.Flags&FlagFirst != 0, f.Flags&FlagLast != 0
	if first && last {
		delete(a.parts, f.Channel)
		return f.Channel, f.Payload, true, nil
	}
	if a.parts == nil {
		a.parts = map[uint8][]byte{}
	}
	if first {
		if f.Total > maxMessage {
			return f.Channel, nil, false, fmt.Errorf("aawire: channel %d announces a %d byte message", f.Channel, f.Total)
		}
		a.parts[f.Channel] = append(make([]byte, 0, f.Total), f.Payload...)
		return f.Channel, nil, false, nil
	}
	buf, ok := a.parts[f.Channel]
	if !ok {
		return f.Channel, nil, false, errors.New("aawire: fragment without a first fragment")
	}
	if len(buf)+len(f.Payload) > maxMessage {
		delete(a.parts, f.Channel)
		return f.Channel, nil, false, fmt.Errorf("aawire: channel %d message too large", f.Channel)
	}
	buf = append(buf, f.Payload...)
	if last {
		delete(a.parts, f.Channel)
		return f.Channel, buf, true, nil
	}
	a.parts[f.Channel] = buf
	return f.Channel, nil, false, nil
}
