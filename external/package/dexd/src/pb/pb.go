// Package pb is a minimal protobuf reader and writer: enough for the handful
// of Android Auto messages dexd exchanges, with no generated code.
package pb

import (
	"encoding/binary"
	"errors"
)

// Wire types.
const (
	WireVarint  = 0
	WireFixed64 = 1
	WireBytes   = 2
	WireFixed32 = 5
)

// Writer appends fields to a message.
type Writer struct{ buf []byte }

func (w *Writer) key(field, wire int) {
	w.buf = binary.AppendUvarint(w.buf, uint64(field)<<3|uint64(wire))
}

// Varint writes an unsigned or two's-complement integer field.
func (w *Writer) Varint(field int, v uint64) {
	w.key(field, WireVarint)
	w.buf = binary.AppendUvarint(w.buf, v)
}

// Sint writes a zigzag-encoded signed field (sint32/sint64).
func (w *Writer) Sint(field int, v int64) { w.Varint(field, uint64(v<<1)^uint64(v>>63)) }

// Bool writes a boolean field.
func (w *Writer) Bool(field int, v bool) {
	if v {
		w.Varint(field, 1)
	} else {
		w.Varint(field, 0)
	}
}

// Bytes writes a length-delimited field (bytes, string or nested message).
func (w *Writer) Bytes(field int, b []byte) {
	w.key(field, WireBytes)
	w.buf = binary.AppendUvarint(w.buf, uint64(len(b)))
	w.buf = append(w.buf, b...)
}

// String writes a string field.
func (w *Writer) String(field int, s string) { w.Bytes(field, []byte(s)) }

// Out returns the encoded message.
func (w *Writer) Out() []byte { return w.buf }

// Field is one decoded field. Val holds varint and fixed-width values; Data
// holds length-delimited content.
type Field struct {
	Num  int
	Wire int
	Val  uint64
	Data []byte
}

var errTruncated = errors.New("pb: truncated message")

// Parse decodes every field of a message, keeping unknown ones.
func Parse(b []byte) ([]Field, error) {
	var out []Field
	for len(b) > 0 {
		k, n := binary.Uvarint(b)
		if n <= 0 {
			return nil, errTruncated
		}
		b = b[n:]
		f := Field{Num: int(k >> 3), Wire: int(k & 7)}
		switch f.Wire {
		case WireVarint:
			v, n := binary.Uvarint(b)
			if n <= 0 {
				return nil, errTruncated
			}
			f.Val, b = v, b[n:]
		case WireFixed64:
			if len(b) < 8 {
				return nil, errTruncated
			}
			f.Val, b = binary.LittleEndian.Uint64(b), b[8:]
		case WireFixed32:
			if len(b) < 4 {
				return nil, errTruncated
			}
			f.Val, b = uint64(binary.LittleEndian.Uint32(b)), b[4:]
		case WireBytes:
			l, n := binary.Uvarint(b)
			if n <= 0 || l > uint64(len(b)-n) {
				return nil, errTruncated
			}
			f.Data, b = b[n:n+int(l)], b[n+int(l):]
		default:
			return nil, errors.New("pb: unsupported wire type")
		}
		out = append(out, f)
	}
	return out, nil
}

// PackedVarints decodes a packed repeated varint field; a malformed tail is dropped.
func PackedVarints(b []byte) []uint64 {
	var out []uint64
	for len(b) > 0 {
		v, n := binary.Uvarint(b)
		if n <= 0 {
			break
		}
		out, b = append(out, v), b[n:]
	}
	return out
}
