package screen

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

var sizes = [][2]int{{800, 480}, {1280, 720}, {1920, 1080}}

func inside(outer, r Rect) bool {
	return r.X >= outer.X && r.Y >= outer.Y &&
		r.X+r.W <= outer.X+outer.W && r.Y+r.H <= outer.Y+outer.H
}

func overlap(a, b Rect) bool {
	return a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H
}

func centre(r Rect) (int, int) { return r.X + r.W/2, r.Y + r.H/2 }

func TestVisibleCentred(t *testing.T) {
	got := Visible(VideoSize{W: 1280, H: 720, MarginW: 0, MarginH: 40})
	want := Rect{0, 20, 1280, 680}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestMenuButtonsInsideVisible(t *testing.T) {
	for _, sz := range sizes {
		w, h := sz[0], sz[1]
		vis := Visible(VideoSize{W: w, H: h, MarginW: 40, MarginH: 30})
		s := Menu(w, h, vis, 20)
		aa, ok1 := s.Buttons["aa"]
		dex, ok2 := s.Buttons["dex"]
		if !ok1 || !ok2 {
			t.Fatalf("%dx%d: missing buttons %v", w, h, s.Buttons)
		}
		for name, b := range map[string]Rect{"aa": aa, "dex": dex} {
			if !inside(vis, b) {
				t.Errorf("%dx%d: %s %+v not inside %+v", w, h, name, b, vis)
			}
			if b.W*4 < vis.W {
				t.Errorf("%dx%d: %s too narrow: %+v", w, h, name, b)
			}
		}
		if overlap(aa, dex) {
			t.Errorf("%dx%d: buttons overlap", w, h)
		}
	}
}

func TestHit(t *testing.T) {
	vis := Visible(VideoSize{W: 1280, H: 720})
	s := Menu(1280, 720, vis, 20)
	x, y := centre(s.Buttons["aa"])
	if got := s.Hit(x, y); got != "aa" {
		t.Errorf("aa centre: got %q", got)
	}
	x, y = centre(s.Buttons["dex"])
	if got := s.Hit(x, y); got != "dex" {
		t.Errorf("dex centre: got %q", got)
	}
	if got := s.Hit(0, 0); got != "" {
		t.Errorf("(0,0): got %q", got)
	}
	if got := s.Hit(-5, 99999); got != "" {
		t.Errorf("(-5,99999): got %q", got)
	}
}

func TestScaleTouch(t *testing.T) {
	x, y := ScaleTouch(400, 240, 800, 480, 1280, 720)
	if x != 640 || y != 360 {
		t.Errorf("got (%d,%d)", x, y)
	}
	x, y = ScaleTouch(900, 500, 800, 480, 1280, 720)
	if x != 1279 || y != 719 {
		t.Errorf("clamp: got (%d,%d)", x, y)
	}
	x, y = ScaleTouch(-10, -10, 800, 480, 1280, 720)
	if x != 0 || y != 0 {
		t.Errorf("negative clamp: got (%d,%d)", x, y)
	}
}

func TestFontCoversASCII(t *testing.T) {
	for r := rune(33); r <= 126; r++ {
		g := glyphFor(r)
		set := false
		for _, row := range g {
			if row&0x1F != 0 {
				set = true
			}
		}
		if !set {
			t.Errorf("rune %q has no pixels", r)
		}
	}
	if glyphFor('I') == glyphFor('l') {
		t.Error("'I' and 'l' are identical")
	}
	if glyphFor(0x4E16) != glyphFor('?') {
		t.Error("unknown rune should draw as '?'")
	}
}

func TestMessageBackButton(t *testing.T) {
	for _, sz := range sizes {
		w, h := sz[0], sz[1]
		vis := Visible(VideoSize{W: w, H: h, MarginW: 40, MarginH: 30})
		lines := []string{"DeX is not in this version yet.", "It arrives with the next update."}
		s := Message(w, h, vis, lines, true)
		b, ok := s.Buttons["back"]
		if !ok || !inside(vis, b) {
			t.Errorf("%dx%d: back button %+v ok=%v", w, h, b, ok)
		}
		if got := Message(w, h, vis, lines, false); len(got.Buttons) != 0 {
			t.Errorf("%dx%d: expected no buttons, got %v", w, h, got.Buttons)
		}
	}
}

func TestCountdownChangesPixels(t *testing.T) {
	vis := Visible(VideoSize{W: 1280, H: 720})
	a := Menu(1280, 720, vis, 20)
	b := Menu(1280, 720, vis, 19)
	if bytes.Equal(a.Canvas.Pix, b.Canvas.Pix) {
		t.Error("countdown 20 and 19 produce identical pictures")
	}
}

func TestBackgroundAndSize(t *testing.T) {
	s := Menu(800, 480, Visible(VideoSize{W: 800, H: 480}), 5)
	if s.Canvas.W != 800 || s.Canvas.H != 480 || len(s.Canvas.Pix) != 800*480*3 {
		t.Fatalf("bad canvas %d %d %d", s.Canvas.W, s.Canvas.H, len(s.Canvas.Pix))
	}
	if p := s.Canvas.Pix[:3]; p[0] != 16 || p[1] != 18 || p[2] != 24 {
		t.Errorf("corner pixel %v is not background", p)
	}
}

// Exercise the drawing code end to end by encoding real PNGs.
func TestWritePNGs(t *testing.T) {
	dir := t.TempDir()
	lines := []string{"DeX is not in this version yet.", "It arrives with the next update."}
	for _, sz := range sizes {
		w, h := sz[0], sz[1]
		vis := Visible(VideoSize{W: w, H: h, MarginW: 20, MarginH: 20})
		for name, s := range map[string]Screen{
			"menu":    Menu(w, h, vis, 17),
			"message": Message(w, h, vis, lines, true),
		} {
			img := image.NewRGBA(image.Rect(0, 0, w, h))
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					i := (y*w + x) * 3
					img.Set(x, y, color.RGBA{s.Canvas.Pix[i], s.Canvas.Pix[i+1], s.Canvas.Pix[i+2], 255})
				}
			}
			var buf bytes.Buffer
			if err := png.Encode(&buf, img); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(dir, name+"-"+strconv.Itoa(w)+"x"+strconv.Itoa(h)+".png")
			if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}
