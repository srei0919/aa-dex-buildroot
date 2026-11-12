// Package screen draws the dexd menu and message pictures into an RGB canvas.
package screen

// Rect is an axis-aligned rectangle in picture pixels.
type Rect struct{ X, Y, W, H int }

// Contains reports whether the point is inside the rectangle.
func (r Rect) Contains(x, y int) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

// Canvas is an RGB picture, 3 bytes per pixel, row-major.
type Canvas struct {
	W, H int
	Pix  []byte
}

// Background is the picture background colour (RGB).
var Background = [3]byte{16, 18, 24}

// NewCanvas returns a canvas filled with the background colour.
func NewCanvas(w, h int) *Canvas {
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	c := &Canvas{W: w, H: h, Pix: make([]byte, w*h*3)}
	c.FillRect(Rect{0, 0, w, h}, Background)
	return c
}

// FillRect fills r (clipped to the canvas) with col.
func (c *Canvas) FillRect(r Rect, col [3]byte) {
	x0, y0 := max(r.X, 0), max(r.Y, 0)
	x1, y1 := min(r.X+r.W, c.W), min(r.Y+r.H, c.H)
	for y := y0; y < y1; y++ {
		i := (y*c.W + x0) * 3
		for x := x0; x < x1; x++ {
			c.Pix[i], c.Pix[i+1], c.Pix[i+2] = col[0], col[1], col[2]
			i += 3
		}
	}
}

// TextWidth is the width in pixels of s drawn at the given scale.
func TextWidth(s string, scale int) int {
	n := len([]rune(s))
	if n == 0 {
		return 0
	}
	return (n*6 - 1) * scale
}

// DrawText draws s with its top-left corner at (x, y). Each font pixel is a
// scale x scale square; glyphs are 5 wide with a 1 pixel gap.
func (c *Canvas) DrawText(x, y int, s string, scale int, col [3]byte) {
	if scale < 1 {
		scale = 1
	}
	for _, r := range s {
		g := glyphFor(r)
		for row := 0; row < 7; row++ {
			for colIdx := 0; colIdx < 5; colIdx++ {
				if g[row]&(1<<(4-colIdx)) != 0 {
					c.FillRect(Rect{x + colIdx*scale, y + row*scale, scale, scale}, col)
				}
			}
		}
		x += 6 * scale
	}
}
