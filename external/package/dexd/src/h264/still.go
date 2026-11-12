package h264

// Picture is a 4:2:0 picture. W and H are even.
type Picture struct {
	W, H      int
	Y, Cb, Cr []byte
}

// YCbCr converts one colour with BT.601 limited-range coefficients.
func YCbCr(r, g, b byte) [3]byte {
	R, G, B := int(r), int(g), int(b)
	return [3]byte{
		byte((66*R+129*G+25*B+128)>>8 + 16),
		byte((-38*R-74*G+112*B+128)>>8 + 128),
		byte((112*R-94*G-18*B+128)>>8 + 128),
	}
}

// FromRGB converts packed RGB to a Picture; each chroma sample is the
// average of its 2x2 block.
func FromRGB(w, h int, rgb []byte) *Picture {
	p := &Picture{W: w, H: h, Y: make([]byte, w*h), Cb: make([]byte, w*h/4), Cr: make([]byte, w*h/4)}
	for y := 0; y < h; y += 2 {
		for x := 0; x < w; x += 2 {
			cb, cr := 0, 0
			for dy := 0; dy < 2; dy++ {
				for dx := 0; dx < 2; dx++ {
					i := (y+dy)*w + x + dx
					c := YCbCr(rgb[3*i], rgb[3*i+1], rgb[3*i+2])
					p.Y[i] = c[0]
					cb += int(c[1])
					cr += int(c[2])
				}
			}
			j := (y/2)*(w/2) + x/2
			p.Cb[j], p.Cr[j] = byte((cb+2)/4), byte((cr+2)/4)
		}
	}
	return p
}

// padded is a Picture enlarged to whole macroblocks, filled with background.
type padded struct {
	mbW, mbH  int
	y, cb, cr []byte
}

func pad(p *Picture, bg [3]byte) padded {
	m := padded{mbW: mbs(p.W), mbH: mbs(p.H)}
	W, H := m.mbW*16, m.mbH*16
	m.y, m.cb, m.cr = make([]byte, W*H), make([]byte, W*H/4), make([]byte, W*H/4)
	for i := range m.y {
		m.y[i] = bg[0]
	}
	for i := range m.cb {
		m.cb[i], m.cr[i] = bg[1], bg[2]
	}
	for y := 0; y < p.H; y++ {
		copy(m.y[y*W:], p.Y[y*p.W:(y+1)*p.W])
	}
	for y := 0; y < p.H/2; y++ {
		copy(m.cb[y*W/2:], p.Cb[y*p.W/2:(y+1)*p.W/2])
		copy(m.cr[y*W/2:], p.Cr[y*p.W/2:(y+1)*p.W/2])
	}
	return m
}

// equal reports whether a rectangle of one plane is all v.
func equal(plane []byte, stride, x, y, w, h int, v byte) bool {
	for j := y; j < y+h; j++ {
		for i := x; i < x+w; i++ {
			if plane[j*stride+i] != v {
				return false
			}
		}
	}
	return true
}

// isBackground checks a region of macroblock (mx, my) in all three planes.
// The region is given in luma samples (dx, dy, w, h), all even.
func (m padded) isBackground(mx, my, dx, dy, w, h int, bg [3]byte) bool {
	W := m.mbW * 16
	x, y := mx*16+dx, my*16+dy
	return equal(m.y, W, x, y, w, h, bg[0]) &&
		equal(m.cb, W/2, x/2, y/2, w/2, h/2, bg[1]) &&
		equal(m.cr, W/2, x/2, y/2, w/2, h/2, bg[2])
}

// pcmMap decides, for every macroblock in raster order, whether it is coded
// raw (true) or flat (false).
func pcmMap(p *Picture, bg [3]byte) []bool { return decide(pad(p, bg), bg) }

// decide: a macroblock is flat only when it and the samples its DC
// prediction reads (the left neighbour's right column and the upper
// neighbour's bottom row) are all background, so the prediction rebuilds it
// exactly. Macroblock (0,0) has nothing to predict from and is always raw.
// Two columns/rows are checked so the matching chroma column/row is included.
func decide(m padded, bg [3]byte) []bool {
	pcm := make([]bool, m.mbW*m.mbH)
	for my := 0; my < m.mbH; my++ {
		for mx := 0; mx < m.mbW; mx++ {
			flat := (mx > 0 || my > 0) && m.isBackground(mx, my, 0, 0, 16, 16, bg)
			if flat && mx > 0 {
				flat = m.isBackground(mx-1, my, 14, 0, 2, 16, bg)
			}
			if flat && my > 0 {
				flat = m.isBackground(mx, my-1, 0, 14, 16, 2, bg)
			}
			pcm[my*m.mbW+mx] = !flat
		}
	}
	return pcm
}

// zeroToken writes coeff_token for "no coefficients" for the given nC.
func zeroToken(w *bitWriter, nC int) {
	switch {
	case nC < 2:
		w.u(1, 1)
	case nC < 4:
		w.u(2, 3)
	case nC < 8:
		w.u(4, 15)
	default:
		w.u(6, 3)
	}
}

func clampPCM(b byte) byte {
	if b == 0 {
		return 1 // early editions of the standard forbid a zero PCM sample
	}
	return b
}

// EncodeStill codes p as one IDR access unit (AUD, SPS, PPS, slice) in
// Annex B. bg is the background in Y, Cb, Cr; idrID should alternate
// between consecutive pictures.
func EncodeStill(p *Picture, bg [3]byte, idrID int) []byte {
	m := pad(p, bg)
	pcm := decide(m, bg)
	W := m.mbW * 16

	var s bitWriter
	s.ue(0)             // first_mb_in_slice
	s.ue(7)             // slice_type: I (all slices)
	s.ue(0)             // pic_parameter_set_id
	s.u(4, 0)           // frame_num
	s.ue(uint64(idrID)) // idr_pic_id
	s.u(1, 0)           // no_output_of_prior_pics_flag
	s.u(1, 0)           // long_term_reference_flag
	s.se(0)             // slice_qp_delta
	s.ue(1)             // disable_deblocking_filter_idc

	blk := make([]byte, 384)
	for my := 0; my < m.mbH; my++ {
		for mx := 0; mx < m.mbW; mx++ {
			i := my*m.mbW + mx
			if pcm[i] {
				s.ue(25) // I_PCM
				s.align()
				k := 0
				for y := 0; y < 16; y++ {
					for x := 0; x < 16; x++ {
						blk[k] = clampPCM(m.y[(my*16+y)*W+mx*16+x])
						k++
					}
				}
				for _, plane := range [][]byte{m.cb, m.cr} {
					for y := 0; y < 8; y++ {
						for x := 0; x < 8; x++ {
							blk[k] = clampPCM(plane[(my*8+y)*W/2+mx*8+x])
							k++
						}
					}
				}
				s.bytes(blk)
				continue
			}
			s.ue(3) // I_16x16, DC prediction, no coded luma or chroma
			s.ue(0) // intra_chroma_pred_mode: DC
			s.se(0) // mb_qp_delta
			haveA, haveB := mx > 0, my > 0
			nA, nB := 0, 0
			if haveA && pcm[i-1] {
				nA = 16
			}
			if haveB && pcm[i-m.mbW] {
				nB = 16
			}
			nC := 0
			switch {
			case haveA && haveB:
				nC = (nA + nB + 1) >> 1
			case haveA:
				nC = nA
			case haveB:
				nC = nB
			}
			zeroToken(&s, nC) // the luma DC block: no coefficients
		}
	}
	s.trailing()

	out := nal(nalAUD, []byte{0x10}) // primary_pic_type I, then trailing bits
	out = append(out, Params(p.W, p.H)...)
	return append(out, nal(nalIDR, s.buf)...)
}
