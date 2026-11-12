package screen

import "strconv"

// Screen is a drawn picture plus the touchable buttons on it.
type Screen struct {
	Canvas  *Canvas
	Buttons map[string]Rect
}

// VideoSize is a picture size and the total margins (both sides together)
// the car crops away.
type VideoSize struct{ W, H, MarginW, MarginH int }

var (
	white      = [3]byte{255, 255, 255}
	grey       = [3]byte{190, 195, 205}
	buttonFill = [3]byte{40, 90, 200}
)

// Visible is the picture size minus the margins, centred.
func Visible(c VideoSize) Rect {
	mw, mh := max(c.MarginW, 0), max(c.MarginH, 0)
	return Rect{X: mw / 2, Y: mh / 2, W: max(c.W-mw, 0), H: max(c.H-mh, 0)}
}

// ScaleTouch maps touchscreen coordinates to picture coordinates, clamped to
// the picture.
func ScaleTouch(x, y, touchW, touchH, w, h int) (int, int) {
	return scaleAxis(x, touchW, w), scaleAxis(y, touchH, h)
}

func scaleAxis(v, from, to int) int {
	if from <= 0 || to <= 0 {
		return 0
	}
	return min(max(v*to/from, 0), to-1)
}

// Hit returns the name of the button containing (x, y), or "".
func (s Screen) Hit(x, y int) string {
	for name, r := range s.Buttons {
		if r.Contains(x, y) {
			return name
		}
	}
	return ""
}

// lineScale picks the integer scale so a text line is about 1/14 of the
// visible height (minimum 2).
func lineScale(vis Rect) int {
	return max(2, (vis.H/14+3)/7) // round(H/14/7)
}

// fitScale shrinks scale until s fits in maxW (never below 1).
func fitScale(s string, scale, maxW int) int {
	for scale > 1 && TextWidth(s, scale) > maxW {
		scale--
	}
	return scale
}

// drawCentred draws s centred horizontally on cx with its vertical middle at cy.
func drawCentred(c *Canvas, cx, cy int, s string, scale int, col [3]byte) {
	c.DrawText(cx-TextWidth(s, scale)/2, cy-7*scale/2, s, scale, col)
}

func drawButton(c *Canvas, r Rect, label string, scale int) {
	c.FillRect(r, buttonFill)
	scale = fitScale(label, scale, r.W*9/10)
	drawCentred(c, r.X+r.W/2, r.Y+r.H/2, label, scale, white)
}

// Menu draws the mode menu with buttons "aa" and "dex".
func Menu(w, h int, visible Rect, secondsLeft int) Screen {
	c := NewCanvas(w, h)
	scale := lineScale(visible)
	bw, bh := visible.W*40/100, visible.H*45/100
	gap := visible.W * 6 / 100
	x0 := visible.X + (visible.W-2*bw-gap)/2
	y0 := visible.Y + (visible.H-bh)/2
	aa := Rect{x0, y0, bw, bh}
	dex := Rect{x0 + bw + gap, y0, bw, bh}

	cx := visible.X + visible.W/2
	title := "Choose a mode"
	drawCentred(c, cx, visible.Y+(y0-visible.Y)/2,
		title, fitScale(title, scale, visible.W*9/10), white)

	drawButton(c, aa, "Android Auto", scale)
	drawButton(c, dex, "DeX", scale)

	footer := "Android Auto starts in " + strconv.Itoa(secondsLeft) + " s"
	fy := y0 + bh + (visible.Y+visible.H-(y0+bh))/2
	drawCentred(c, cx, fy, footer, fitScale(footer, scale, visible.W*9/10), grey)

	return Screen{Canvas: c, Buttons: map[string]Rect{"aa": aa, "dex": dex}}
}

// Message draws centred text lines and, when back is true, a "Back" button
// named "back" near the bottom of the visible area.
func Message(w, h int, visible Rect, lines []string, back bool) Screen {
	c := NewCanvas(w, h)
	scale := lineScale(visible)
	cx := visible.X + visible.W/2
	lineH := 7*scale + 7*scale/2 // line plus spacing
	area := visible.H * 70 / 100 // text sits in the upper part
	top := visible.Y + (area-len(lines)*lineH)/2
	for i, l := range lines {
		drawCentred(c, cx, top+i*lineH+lineH/2, l, fitScale(l, scale, visible.W*9/10), white)
	}
	s := Screen{Canvas: c, Buttons: map[string]Rect{}}
	if back {
		bw, bh := visible.W*30/100, visible.H*15/100
		r := Rect{cx - bw/2, visible.Y + visible.H*78/100, bw, bh}
		drawButton(c, r, "Back", scale)
		s.Buttons["back"] = r
	}
	return s
}
