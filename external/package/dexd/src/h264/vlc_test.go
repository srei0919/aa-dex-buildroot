//go:build vlc

package h264

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// testRGB draws four coloured quadrants inside a background border. Edges
// deliberately fall inside macroblocks.
func testRGB(w, h int) []byte {
	cols := [4][3]byte{{220, 40, 40}, {40, 200, 60}, {40, 90, 200}, {240, 240, 240}}
	rgb := make([]byte, w*h*3)
	bx, by := w/7+3, h/7+5
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := [3]byte{16, 18, 24}
			if x >= bx && x < w-bx && y >= by && y < h-by {
				q := 0
				if x >= w/2+5 {
					q++
				}
				if y >= h/2+3 {
					q += 2
				}
				c = cols[q]
			}
			copy(rgb[(y*w+x)*3:], c[:])
		}
	}
	return rgb
}

func vlcPath() string {
	if p := os.Getenv("VLC"); p != "" {
		return p
	}
	return `C:\Program Files\VideoLAN\VLC\vlc.exe`
}

// decodeLast decodes a stream with VLC and returns a frame from late in it.
func decodeLast(t *testing.T, stream []byte, w, h int) (image.Image, string) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.h264")
	if err := os.WriteFile(in, stream, 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "png")
	os.Mkdir(out, 0o755)
	cmd := exec.Command(vlcPath(), "-I", "dummy", "-vv", "--no-audio", "--avcodec-hw=none", "--vout=dummy",
		"--demux", "h264", "--h264-fps", "30",
		"--video-filter=scene", "--scene-format=png", "--scene-ratio=1", "--scene-prefix=f",
		fmt.Sprintf("--scene-width=%d", w), fmt.Sprintf("--scene-height=%d", h),
		"--scene-path="+out, in, "vlc://quit")
	log, _ := cmd.CombinedOutput()
	files, _ := filepath.Glob(filepath.Join(out, "f*.png"))
	sort.Strings(files)
	if len(files) < 10 {
		t.Fatalf("VLC wrote %d frames; log tail:\n%s", len(files), tail(string(log), 30))
	}
	f, err := os.Open(files[len(files)-3])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img, string(log)
}

func tail(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// decoderErrors returns log lines in which the H.264 parser or decoder
// complains; decoders conceal damage silently, so this is checked too.
func decoderErrors(log string) []string {
	var bad []string
	for _, l := range strings.Split(log, "\n") {
		low := strings.ToLower(l)
		// The forced demuxer's format guess expects SPS first; an access
		// unit delimiter first is legal, so that one message is not a fault.
		if strings.Contains(low, "doesn't look like a h264 es stream") {
			continue
		}
		if !(strings.Contains(low, "avcodec") || strings.Contains(low, "h264") || strings.Contains(low, "packetizer")) {
			continue
		}
		for _, word := range []string{"error", "corrupt", "concealing", "invalid", "out of range", "decode_slice"} {
			if strings.Contains(low, word) {
				bad = append(bad, strings.TrimSpace(l))
				break
			}
		}
	}
	return bad
}

func TestDecodeWithVLC(t *testing.T) {
	for _, sz := range [][2]int{{800, 480}, {1280, 720}, {1920, 1080}} {
		w, h := sz[0], sz[1]
		t.Run(fmt.Sprintf("%dx%d", w, h), func(t *testing.T) {
			rgb := testRGB(w, h)
			p := FromRGB(w, h, rgb)
			bgc := YCbCr(16, 18, 24)
			var stream bytes.Buffer
			for i := 0; i < 30; i++ {
				stream.Write(EncodeStill(p, bgc, i%2))
			}
			img, log := decodeLast(t, stream.Bytes(), w, h)
			if errs := decoderErrors(log); len(errs) > 0 {
				t.Errorf("decoder complained:\n%s", strings.Join(errs, "\n"))
			}
			b := img.Bounds()
			if b.Dx() != w || b.Dy() != h {
				t.Logf("decoded frame is %dx%d; comparing by relative position", b.Dx(), b.Dy())
			}
			checked, bad := 0, 0
			for gy := 1; gy < 40; gy++ {
				for gx := 1; gx < 60; gx++ {
					x, y := gx*w/60, gy*h/40
					if !uniform(rgb, w, h, x, y, 6) {
						continue
					}
					checked++
					ix, iy := b.Min.X+x*b.Dx()/w, b.Min.Y+y*b.Dy()/h
					r, g, bl, _ := img.At(ix, iy).RGBA()
					want := rgb[(y*w+x)*3 : (y*w+x)*3+3]
					if far(r>>8, want[0]) || far(g>>8, want[1]) || far(bl>>8, want[2]) {
						if bad < 5 {
							t.Errorf("at (%d,%d) got %d,%d,%d want %v", x, y, r>>8, g>>8, bl>>8, want)
						}
						bad++
					}
				}
			}
			if checked < 1500 {
				t.Errorf("only %d points checked", checked)
			}
			if bad > 0 {
				t.Errorf("%d of %d points differ", bad, checked)
			}
		})
	}
}

func uniform(rgb []byte, w, h, x, y, r int) bool {
	c := rgb[(y*w+x)*3 : (y*w+x)*3+3]
	for j := y - r; j <= y+r; j++ {
		for i := x - r; i <= x+r; i++ {
			if i < 0 || j < 0 || i >= w || j >= h {
				return false
			}
			d := rgb[(j*w+i)*3 : (j*w+i)*3+3]
			if d[0] != c[0] || d[1] != c[1] || d[2] != c[2] {
				return false
			}
		}
	}
	return true
}

func far(got uint32, want byte) bool {
	d := int(got) - int(want)
	return d > 6 || d < -6
}
