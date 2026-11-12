// Package h264 makes H.264 still pictures without a codec library: a picture
// is coded from just two macroblock kinds, raw (I_PCM) and flat (DC
// prediction, no residual). That is enough for menus and messages.
package h264

// bitWriter writes bits most significant first.
type bitWriter struct {
	buf   []byte
	nbits int
}

func (w *bitWriter) bit(b uint64) {
	if w.nbits%8 == 0 {
		w.buf = append(w.buf, 0)
	}
	if b&1 != 0 {
		w.buf[len(w.buf)-1] |= 0x80 >> (w.nbits % 8)
	}
	w.nbits++
}

// u writes v in n bits.
func (w *bitWriter) u(n int, v uint64) {
	for i := n - 1; i >= 0; i-- {
		w.bit(v >> i)
	}
}

// ue writes an unsigned Exp-Golomb code.
func (w *bitWriter) ue(v uint64) {
	v++
	n := 0
	for x := v; x > 1; x >>= 1 {
		n++
	}
	w.u(n, 0)
	w.u(n+1, v)
}

// se writes a signed Exp-Golomb code.
func (w *bitWriter) se(v int64) {
	if v > 0 {
		w.ue(uint64(2*v - 1))
	} else {
		w.ue(uint64(-2 * v))
	}
}

// align writes zero bits up to the next byte boundary.
func (w *bitWriter) align() {
	for w.nbits%8 != 0 {
		w.bit(0)
	}
}

// bytes appends whole bytes; the writer must be aligned.
func (w *bitWriter) bytes(b []byte) {
	w.buf = append(w.buf, b...)
	w.nbits += 8 * len(b)
}

// trailing writes rbsp_trailing_bits.
func (w *bitWriter) trailing() {
	w.bit(1)
	w.align()
}

// escape inserts emulation-prevention bytes into an RBSP.
func escape(rbsp []byte) []byte {
	out := make([]byte, 0, len(rbsp)+len(rbsp)/64+4)
	zeros := 0
	for _, b := range rbsp {
		if zeros >= 2 && b <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		out = append(out, b)
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

// nal returns one Annex B NAL unit: 4-byte start code, header, escaped payload.
func nal(header byte, rbsp []byte) []byte {
	out := append([]byte{0, 0, 0, 1, header}, escape(rbsp)...)
	return out
}
