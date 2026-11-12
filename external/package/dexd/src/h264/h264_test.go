package h264

import (
	"bytes"
	"testing"
)

func bitsOf(w *bitWriter) string {
	var s []byte
	for i := 0; i < w.nbits; i++ {
		if w.buf[i/8]&(0x80>>(i%8)) != 0 {
			s = append(s, '1')
		} else {
			s = append(s, '0')
		}
	}
	return string(s)
}

func TestBitWriterExpGolomb(t *testing.T) {
	cases := []struct {
		write func(*bitWriter)
		want  string
	}{
		{func(w *bitWriter) { w.ue(0) }, "1"},
		{func(w *bitWriter) { w.ue(3) }, "00100"},
		{func(w *bitWriter) { w.ue(25) }, "000011010"},
		{func(w *bitWriter) { w.se(-1) }, "011"},
		{func(w *bitWriter) { w.se(1) }, "010"},
		{func(w *bitWriter) { w.se(0) }, "1"},
		{func(w *bitWriter) { w.u(3, 5) }, "101"},
	}
	for i, c := range cases {
		var w bitWriter
		c.write(&w)
		if got := bitsOf(&w); got != c.want {
			t.Errorf("case %d: got %s want %s", i, got, c.want)
		}
	}
}

func TestTrailingBits(t *testing.T) {
	var w bitWriter
	w.u(3, 0)
	w.trailing()
	if !bytes.Equal(w.buf, []byte{0x10}) {
		t.Fatalf("got % x", w.buf)
	}
}

func TestEmulationPrevention(t *testing.T) {
	cases := [][2][]byte{
		{{0, 0, 1}, {0, 0, 3, 1}},
		{{0, 0, 0}, {0, 0, 3, 0}},
		{{0, 0, 4}, {0, 0, 4}},
		{{0, 0, 0, 0, 0}, {0, 0, 3, 0, 0, 3, 0}},
		{{5, 0, 0, 2, 0}, {5, 0, 0, 3, 2, 0}},
	}
	for _, c := range cases {
		if got := escape(c[0]); !bytes.Equal(got, c[1]) {
			t.Errorf("% x: got % x want % x", c[0], got, c[1])
		}
	}
}

// bitReader reads back what the encoder wrote, for the tests only.
type bitReader struct {
	b   []byte
	pos int
}

func (r *bitReader) u(n int) int {
	v := 0
	for i := 0; i < n; i++ {
		v = v<<1 | int(r.b[r.pos/8]>>(7-r.pos%8)&1)
		r.pos++
	}
	return v
}

func (r *bitReader) ue() int {
	zeros := 0
	for r.u(1) == 0 {
		zeros++
	}
	return 1<<zeros - 1 + r.u(zeros)
}

// nals splits an Annex B stream into NAL units without start codes.
func nals(stream []byte) [][]byte {
	var out [][]byte
	parts := bytes.Split(stream, []byte{0, 0, 0, 1})
	for _, p := range parts {
		if len(p) > 0 {
			out = append(out, p)
		}
	}
	return out
}

type spsInfo struct{ profile, constraints, level, mbW, mbH, cropBottom, cropRight int }

func parseSPS(nal []byte) spsInfo {
	r := &bitReader{b: nal[1:]}
	var s spsInfo
	s.profile, s.constraints, s.level = r.u(8), r.u(8), r.u(8)
	r.ue()           // sps id
	r.ue()           // log2_max_frame_num_minus4
	if r.ue() != 2 { // pic_order_cnt_type
		panic("poc type")
	}
	r.ue() // max refs
	r.u(1) // gaps
	s.mbW = r.ue() + 1
	s.mbH = r.ue() + 1
	r.u(1) // frame_mbs_only
	r.u(1) // direct_8x8
	if r.u(1) == 1 {
		r.ue()
		s.cropRight = r.ue()
		r.ue()
		s.cropBottom = r.ue()
	}
	return s
}

func TestParamsFields(t *testing.T) {
	cases := []struct {
		w, h int
		want spsInfo
	}{
		{800, 480, spsInfo{66, 0xC0, 30, 50, 30, 0, 0}},
		{1280, 720, spsInfo{66, 0xC0, 31, 80, 45, 0, 0}},
		{1920, 1080, spsInfo{66, 0xC0, 40, 120, 68, 4, 0}},
	}
	for _, c := range cases {
		ps := nals(Params(c.w, c.h))
		if len(ps) != 2 || ps[0][0]&0x1f != 7 || ps[1][0]&0x1f != 8 {
			t.Fatalf("%dx%d: want SPS then PPS, got %d units", c.w, c.h, len(ps))
		}
		if got := parseSPS(ps[0]); got != c.want {
			t.Errorf("%dx%d: got %+v want %+v", c.w, c.h, got, c.want)
		}
	}
}

func solid(w, h int, c [3]byte) *Picture {
	p := &Picture{W: w, H: h, Y: make([]byte, w*h), Cb: make([]byte, w*h/4), Cr: make([]byte, w*h/4)}
	for i := range p.Y {
		p.Y[i] = c[0]
	}
	for i := range p.Cb {
		p.Cb[i], p.Cr[i] = c[1], c[2]
	}
	return p
}

var bg = YCbCr(16, 18, 24)

func TestStillStructure(t *testing.T) {
	au := EncodeStill(solid(800, 480, bg), bg, 0)
	units := nals(au)
	var types []int
	for _, u := range units {
		types = append(types, int(u[0]&0x1f))
	}
	if len(types) != 4 || types[0] != 9 || types[1] != 7 || types[2] != 8 || types[3] != 5 {
		t.Fatalf("NAL types %v, want [9 7 8 5]", types)
	}
	if len(au) >= 2500 {
		t.Errorf("all-background 800x480 picture is %d bytes, want under 2500", len(au))
	}
}

func TestIdrIDChangesSlice(t *testing.T) {
	a := EncodeStill(solid(800, 480, bg), bg, 0)
	b := EncodeStill(solid(800, 480, bg), bg, 1)
	if bytes.Equal(a, b) {
		t.Fatal("idr_pic_id should change the slice header")
	}
}

func TestFlatRule(t *testing.T) {
	p := solid(64, 32, bg)
	p.Y[7*64+15] = 200
	pcm := pcmMap(p, bg)
	mbW := 4
	if !pcm[0] || !pcm[1] || pcm[2] {
		t.Fatalf("row 0: pcm %v %v %v, want true true false", pcm[0], pcm[1], pcm[2])
	}
	if !pcm[mbW*0+0] || pcm[mbW*1+1] {
		t.Fatalf("MB (1,1) should be flat")
	}
	if pcm[mbW*1+0] { // below MB(0,0): its upper neighbour's bottom row is background
		t.Fatalf("MB (0,1) should be flat")
	}
}

func TestFlatRuleBottomRow(t *testing.T) {
	p := solid(32, 32, bg)
	p.Cb[7*16+3] = 50 // chroma row 7 = bottom row of MB(0,0)
	pcm := pcmMap(p, bg)
	if !pcm[2] { // MB (0,1) has a non-background upper neighbour bottom row
		t.Fatal("MB (0,1) must be I_PCM")
	}
}

func TestStillSizeBound(t *testing.T) {
	p := solid(1280, 720, bg)
	c := YCbCr(40, 90, 200)
	for y := 100; y < 300; y++ {
		for x := 200; x < 600; x++ {
			p.Y[y*1280+x] = c[0]
			p.Cb[(y/2)*640+x/2], p.Cr[(y/2)*640+x/2] = c[1], c[2]
		}
	}
	if n := len(EncodeStill(p, bg, 0)); n >= 200*1024 {
		t.Errorf("%d bytes, want under 200 KB", n)
	}
}

func TestFromRGBMatchesYCbCr(t *testing.T) {
	rgb := make([]byte, 4*2*3)
	for i := 0; i < 8; i++ {
		rgb[i*3], rgb[i*3+1], rgb[i*3+2] = 40, 90, 200
	}
	p := FromRGB(4, 2, rgb)
	want := YCbCr(40, 90, 200)
	if p.Y[5] != want[0] || p.Cb[1] != want[1] || p.Cr[1] != want[2] {
		t.Fatalf("got %d %d %d want %v", p.Y[5], p.Cb[1], p.Cr[1], want)
	}
	if w := YCbCr(0, 0, 0); w[0] != 16 || w[1] != 128 || w[2] != 128 {
		t.Errorf("black = %v", w)
	}
	if w := YCbCr(255, 255, 255); w[0] != 235 {
		t.Errorf("white = %v", w)
	}
}

func TestOddSizePadsWithBackground(t *testing.T) {
	// 1080 rows are coded as 1088; the extra rows must not force I_PCM.
	au := EncodeStill(solid(1920, 1080, bg), bg, 0)
	if len(au) >= 15000 {
		t.Errorf("all-background 1920x1080 picture is %d bytes", len(au))
	}
}

func TestFlatInsideSolidArea(t *testing.T) {
	p := solid(64, 64, bg)
	c := YCbCr(40, 90, 200)
	for y := 16; y < 64; y++ {
		for x := 16; x < 64; x++ {
			p.Y[y*64+x] = c[0]
			p.Cb[(y/2)*32+x/2], p.Cr[(y/2)*32+x/2] = c[1], c[2]
		}
	}
	pcm := pcmMap(p, bg)
	if !pcm[1*4+1] {
		t.Error("MB (1,1) borders the background and must be I_PCM")
	}
	if pcm[2*4+2] || pcm[3*4+3] {
		t.Error("MBs inside the coloured area should be flat")
	}
}

func TestMenuSizedPictureIsSmall(t *testing.T) {
	// two large buttons, like the menu: interiors must not be raw
	p := solid(1280, 720, bg)
	c := YCbCr(40, 90, 200)
	for _, x0 := range []int{100, 700} {
		for y := 200; y < 520; y++ {
			for x := x0; x < x0+480; x++ {
				p.Y[y*1280+x] = c[0]
				p.Cb[(y/2)*640+x/2], p.Cr[(y/2)*640+x/2] = c[1], c[2]
			}
		}
	}
	if n := len(EncodeStill(p, bg, 0)); n >= 120*1024 {
		t.Errorf("%d bytes, want under 120 KB", n)
	}
}
