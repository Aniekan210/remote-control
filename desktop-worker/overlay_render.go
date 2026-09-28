package main

import (
	"image"
	"image/color"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomedium"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Drawing for the overlay (overlay.go / layered.go put it on screen). It is
// plain Go on image.RGBA (premultiplied alpha, like the layered windows
// want), with no Win32 in it, so it can be rendered and looked at on any
// machine — see TestOverlayPreview.
//
// Two pieces:
//   - an edge glow: a thin bright line at the very edge of the screen with
//     a short soft glow inward, coloured by a gradient that slowly flows
//     around the screen while a task runs (amber and breathing when paused
//     or waiting for you, green-teal when done);
//   - a status pill: a floating dark glass capsule at the top centre with
//     the status, step count, the current step, the live action and a
//     progress bar.

// ── Themes ─────────────────────────────────────────────────────────

// overlayTheme is how one task status looks.
type overlayTheme struct {
	label   string       // shown in the pill
	stops   []color.RGBA // gradient around the edge / across the progress bar
	flow    bool         // the gradient travels around the screen
	breathe float64      // glow pulse speed (0 = steady)
}

var (
	themeRunning = overlayTheme{
		label: "WORKING",
		stops: []color.RGBA{
			{56, 232, 255, 255},  // cyan
			{110, 123, 255, 255}, // indigo
			{195, 107, 255, 255}, // violet
			{255, 107, 200, 255}, // pink
		},
		flow: true,
	}
	themePaused = overlayTheme{
		label:   "PAUSED",
		stops:   []color.RGBA{{255, 190, 80, 255}, {255, 128, 72, 255}},
		breathe: 0.6,
	}
	themeWaiting = overlayTheme{
		label:   "NEEDS YOU",
		stops:   []color.RGBA{{255, 206, 84, 255}, {255, 120, 90, 255}},
		breathe: 1.4,
	}
	themeDone = overlayTheme{
		label: "DONE",
		stops: []color.RGBA{{52, 232, 158, 255}, {15, 184, 173, 255}},
	}
)

func themeFor(status string) overlayTheme {
	switch status {
	case "PAUSED":
		return themePaused
	case "NEEDS_INPUT":
		return themeWaiting
	case "COMPLETED":
		return themeDone
	default:
		return themeRunning
	}
}

// animated reports whether a status needs frames over time.
func (t overlayTheme) animated() bool { return t.flow || t.breathe > 0 }

// gradientAt samples the theme's gradient, looping, at u in [0,1).
func (t overlayTheme) gradientAt(u float64) color.RGBA {
	n := len(t.stops)
	if n == 1 {
		return t.stops[0]
	}
	u -= math.Floor(u)
	f := u * float64(n)
	i := int(f) % n
	j := (i + 1) % n
	return lerpColor(t.stops[i], t.stops[j], smooth(f-math.Floor(f)))
}

// intensity is the glow's brightness multiplier at time secs.
func (t overlayTheme) intensity(secs float64) float64 {
	if t.breathe == 0 {
		return 1
	}
	return 0.62 + 0.38*(0.5+0.5*math.Sin(secs*2*math.Pi*t.breathe/2))
}

func lerpColor(a, b color.RGBA, t float64) color.RGBA {
	l := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*t + 0.5) }
	return color.RGBA{l(a.R, b.R), l(a.G, b.G), l(a.B, b.B), 255}
}

func smooth(t float64) float64 { return t * t * (3 - 2*t) }

// ── Edge glow ──────────────────────────────────────────────────────

// edgeCornerRadius rounds the glow into the screen's corners so they pool
// softly instead of meeting in a hard square.
const edgeCornerRadius = 22.0

// edgeBand is how far (in px at 100% scaling) the glow reaches inward.
const edgeBand = 46.0

// edgeMask is the precomputed, time-independent part of the glow for one
// rectangle of the screen (one edge strip window): per pixel, its alpha
// (0–255 at full intensity) and where it sits along the screen's perimeter
// (0–1, for the gradient). Frames then only look colours up.
type edgeMask struct {
	rect  image.Rectangle // in screen coordinates
	alpha []uint8
	pos   []float32
}

// newEdgeMask computes the mask for rect on a sw x sh screen at scale.
func newEdgeMask(rect image.Rectangle, sw, sh int, scale float64) *edgeMask {
	m := &edgeMask{
		rect:  rect,
		alpha: make([]uint8, rect.Dx()*rect.Dy()),
		pos:   make([]float32, rect.Dx()*rect.Dy()),
	}
	w, h := float64(sw), float64(sh)
	perim := 2 * (w + h)
	r := edgeCornerRadius * scale
	line := 2.0 * scale
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			// Distance inward from the rounded screen outline.
			d := -sdRoundRect(px-w/2, py-h/2, w/2, h/2, r)
			if d < 0 {
				d = 0
			}
			a := 0.0
			a += 0.95 * clamp01(line+0.5-d)                  // crisp line at the edge
			a += 0.68 * math.Exp(-d/(8*scale))               // tight glow
			a += 0.26 * math.Exp(-d/(22*scale))              // wide, faint wash
			a *= clamp01((edgeBand*scale - d) / (8 * scale)) // fade out before the band ends
			i := (y-rect.Min.Y)*rect.Dx() + (x - rect.Min.X)
			m.alpha[i] = uint8(math.Min(a, 1)*255 + 0.5)
			m.pos[i] = float32(perimeterPos(px, py, w, h) / perim)
		}
	}
	return m
}

// edgeStrips splits the glow band into four windows — top and bottom
// full-width, left and right between them — so only the band is ever
// redrawn and pushed, never the whole screen.
func edgeStrips(sw, sh int, scale float64) []image.Rectangle {
	b := int(math.Ceil(edgeBand * scale))
	return []image.Rectangle{
		image.Rect(0, 0, sw, b),
		image.Rect(0, sh-b, sw, sh),
		image.Rect(0, b, b, sh-b),
		image.Rect(sw-b, b, sw, sh-b),
	}
}

// pillOrigin is where the pill window goes: centred, near the top.
func pillOrigin(sw int, scale float64) image.Point {
	pw, _ := pillSize(scale)
	return image.Pt((sw-pw)/2, int((pillTopGap-pillMargin)*scale))
}

// perimeterPos is how far along the screen's edge (clockwise from the
// top-left corner) the nearest edge point to (x, y) is.
func perimeterPos(x, y, w, h float64) float64 {
	dt, db, dl, dr := y, h-y, x, w-x
	switch math.Min(math.Min(dt, db), math.Min(dl, dr)) {
	case dt:
		return x
	case dr:
		return w + y
	case db:
		return w + h + (w - x)
	default:
		return 2*w + h + (h - y)
	}
}

// renderEdge draws one frame of a strip's glow into dst (sized like the
// mask's rect). phase moves the gradient around the screen (0–1).
func renderEdge(dst *image.RGBA, m *edgeMask, th overlayTheme, phase, intensity float64) {
	const lut = 512
	var colors [lut]color.RGBA
	for i := range colors {
		colors[i] = th.gradientAt(float64(i)/lut + phase)
	}
	for i, a := range m.alpha {
		o := i * 4
		if a == 0 {
			dst.Pix[o], dst.Pix[o+1], dst.Pix[o+2], dst.Pix[o+3] = 0, 0, 0, 0
			continue
		}
		c := colors[int(m.pos[i]*lut)%lut]
		al := uint32(float64(a)*intensity + 0.5)
		dst.Pix[o] = uint8(uint32(c.R) * al / 255)
		dst.Pix[o+1] = uint8(uint32(c.G) * al / 255)
		dst.Pix[o+2] = uint8(uint32(c.B) * al / 255)
		dst.Pix[o+3] = uint8(al)
	}
}

// ── Status pill ────────────────────────────────────────────────────

// pillFaces are the fonts the pill uses, at a given scale.
type pillFaces struct {
	label, step, detail font.Face
}

var (
	fontOnce              sync.Once
	fontRegular, fontBold *opentype.Font
)

// loadFonts uses Windows' own UI font (Segoe UI) when it's there, so the
// pill looks native; otherwise the Go fonts bundled with x/image.
// OVERLAY_FONT_REGULAR / OVERLAY_FONT_BOLD point at other .ttf files.
func loadFonts() {
	fontOnce.Do(func() {
		parse := func(paths []string, fallback []byte) *opentype.Font {
			for _, p := range paths {
				if p == "" {
					continue
				}
				if data, err := os.ReadFile(p); err == nil {
					if f, err := opentype.Parse(data); err == nil {
						return f
					}
				}
			}
			f, _ := opentype.Parse(fallback)
			return f
		}
		win := os.Getenv("WINDIR")
		if win == "" {
			win = `C:\Windows`
		}
		fontRegular = parse([]string{os.Getenv("OVERLAY_FONT_REGULAR"), win + `\Fonts\segoeui.ttf`}, goregular.TTF)
		fontBold = parse([]string{os.Getenv("OVERLAY_FONT_BOLD"), win + `\Fonts\seguisb.ttf`, win + `\Fonts\segoeuib.ttf`}, gomedium.TTF)
	})
}

func newPillFaces(scale float64) pillFaces {
	loadFonts()
	face := func(f *opentype.Font, px float64) font.Face {
		fc, err := opentype.NewFace(f, &opentype.FaceOptions{Size: px, DPI: 72, Hinting: font.HintingFull})
		if err != nil {
			return nil
		}
		return fc
	}
	return pillFaces{
		label:  face(fontBold, 11*scale),
		step:   face(fontBold, 16*scale),
		detail: face(fontRegular, 13*scale),
	}
}

// Pill geometry at 100% scaling.
const (
	pillWidth   = 460.0
	pillRadius  = 18.0
	pillPadX    = 18.0
	pillPadTop  = 14.0
	pillMargin  = 26.0 // room around the body for the shadow
	pillTopGap  = 14.0 // distance from the top of the screen
	pillFadedTo = 56   // opacity (of 255) while the cursor is underneath
)

// pillSize is the pill window's size (body plus shadow margin).
func pillSize(scale float64) (int, int) {
	return int(math.Ceil((pillWidth + 2*pillMargin) * scale)), int(math.Ceil((pillBodyHeight() + 2*pillMargin) * scale))
}

func pillBodyHeight() float64 {
	return pillPadTop + 14 + 8 + 20 + 4 + 17 + 12 + 3 + 14
}

// newPillBase draws the parts of the pill that never change — the shadow
// and the glass body — once, so each frame only draws the text, dot and
// bar on top of a copy.
func newPillBase(scale float64) *image.RGBA {
	pw, ph := pillSize(scale)
	dst := image.NewRGBA(image.Rect(0, 0, pw, ph))
	s := scale
	bw, bh := pillWidth*s, pillBodyHeight()*s
	ox, oy := pillMargin*s, pillMargin*s
	cx, cy := ox+bw/2, oy+bh/2
	r := pillRadius * s

	// Shadow, a little below the body.
	fillSDF(dst, dst.Bounds(), func(x, y float64) float64 {
		return sdRoundRect(x-cx, y-(cy+6*s), bw/2, bh/2, r)
	}, func(x, y, d float64) (color.RGBA, float64) {
		return color.RGBA{0, 0, 0, 255}, 0.45 * math.Exp(-math.Max(d, 0)/(9*s))
	})

	// Body: dark glass with a faint top-to-bottom sheen and a hairline edge.
	top, bottom := color.RGBA{28, 31, 41, 255}, color.RGBA{13, 15, 21, 255}
	fillSDF(dst, dst.Bounds(), func(x, y float64) float64 {
		return sdRoundRect(x-cx, y-cy, bw/2, bh/2, r)
	}, func(x, y, d float64) (color.RGBA, float64) {
		cov := clamp01(0.5 - d)
		if cov == 0 {
			return color.RGBA{}, 0
		}
		c := lerpColor(top, bottom, clamp01((y-oy)/bh))
		if d > -1.2*s { // hairline border
			return lerpColor(c, color.RGBA{255, 255, 255, 255}, 0.16), 0.94 * cov
		}
		return c, 0.94 * cov
	})
	return dst
}

// pillBodyRect is the body's rectangle within the pill window (where the
// cursor makes it fade).
func pillBodyRect(scale float64) image.Rectangle {
	m := int(pillMargin * scale)
	return image.Rect(m, m, m+int(pillWidth*scale), m+int(pillBodyHeight()*scale))
}

// renderPill draws the status pill for st at time secs onto a copy of base
// (from newPillBase at the same scale).
func renderPill(dst, base *image.RGBA, st OverlayState, faces pillFaces, scale, secs float64) {
	copy(dst.Pix, base.Pix)
	th := themeFor(st.Status)
	s := scale
	bw := pillWidth * s
	ox, oy := pillMargin*s, pillMargin*s

	x0 := ox + pillPadX*s
	x1 := ox + bw - pillPadX*s
	y := oy + pillPadTop*s
	accent := th.gradientAt(0.08 + secs*0.05)
	muted := color.RGBA{142, 150, 170, 255}

	// Row 1: status dot + label (+ task) … step counter.
	dotR := 4.2 * s
	dotX, dotY := x0+dotR, y+7*s
	if th.flow || th.breathe > 0 {
		// A soft ring pulsing out of the dot.
		p := math.Mod(secs*0.8, 1)
		ringR := dotR * (1 + 1.6*p)
		fillCircle(dst, dotX, dotY, ringR, accent, 0.35*(1-p))
	}
	fillCircle(dst, dotX, dotY, dotR, accent, 1)

	counter := ""
	if st.StepTotal > 0 && st.StepIndex > 0 {
		counter = strconv.Itoa(st.StepIndex) + " / " + strconv.Itoa(st.StepTotal)
	}
	counterW := 0.0
	if counter != "" && faces.label != nil {
		counterW = measure(faces.label, counter)
		drawText(dst, faces.label, x1-counterW, y+11*s, counter, muted)
	}
	lx := dotX + dotR + 9*s
	labelW := drawSpaced(dst, faces.label, lx, y+11*s, th.label, accent, 1.1*s)
	if st.TaskDescription != "" && faces.label != nil {
		tx := lx + labelW + 8*s
		room := x1 - counterW - 12*s - tx
		if room > 40*s {
			drawText(dst, faces.label, tx, y+11*s, ellipsize(faces.label, "·  "+st.TaskDescription, room), muted)
		}
	}
	y += 14*s + 8*s

	// Row 2: the step.
	step := st.StepText
	if step == "" {
		step = "Working…"
	}
	drawText(dst, faces.step, x0, y+15*s, ellipsize(faces.step, step, x1-x0), color.RGBA{244, 246, 251, 255})
	y += 20*s + 4*s

	// Row 3: what the machine is doing right now.
	switch {
	case st.ActionText != "":
		drawText(dst, faces.detail, x0, y+12*s, ellipsize(faces.detail, st.ActionText, x1-x0), color.RGBA{154, 163, 181, 255})
	case st.Status != "COMPLETED":
		// Nothing physical happening right now: a quiet reminder of the
		// kill switch (hotkey.go).
		drawText(dst, faces.detail, x0, y+12*s, "Ctrl+Alt+Shift+X to cancel", color.RGBA{96, 104, 122, 255})
	}
	y += 17*s + 12*s

	// Progress bar: the step fraction, or a sweeping shimmer while there's
	// no plan yet.
	barH := 3 * s
	barBounds := image.Rect(int(x0)-1, int(y)-1, int(x1)+2, int(y+barH)+2)
	track := func(x, yy float64) float64 { return sdRoundRect(x-(x0+x1)/2, yy-(y+barH/2), (x1-x0)/2, barH/2, barH/2) }
	fillSDF(dst, barBounds, track, func(x, yy, d float64) (color.RGBA, float64) {
		return color.RGBA{255, 255, 255, 255}, 0.09 * clamp01(0.5-d)
	})
	var from, to float64
	switch {
	case st.Status == "COMPLETED":
		from, to = 0, 1
	case st.StepTotal > 0:
		done := float64(max(st.StepIndex-1, 0)) / float64(st.StepTotal)
		from, to = 0, math.Max(done, 0.02)
	default:
		p := math.Mod(secs*0.6, 1.4) - 0.2
		from, to = math.Max(p, 0), math.Min(p+0.3, 1)
	}
	if to > from {
		fx0, fx1 := x0+(x1-x0)*from, x0+(x1-x0)*to
		fillSDF(dst, barBounds, func(x, yy float64) float64 {
			return sdRoundRect(x-(fx0+fx1)/2, yy-(y+barH/2), (fx1-fx0)/2, barH/2, barH/2)
		}, func(x, yy, d float64) (color.RGBA, float64) {
			return th.gradientAt((x-x0)/(x1-x0)*0.6 + secs*0.05), clamp01(0.5 - d)
		})
	}
}

// ── Drawing helpers ────────────────────────────────────────────────

// sdRoundRect is the signed distance from (x, y) — relative to the centre
// — to a rounded rectangle with half-size hw x hh and corner radius r
// (negative inside).
func sdRoundRect(x, y, hw, hh, r float64) float64 {
	qx := math.Abs(x) - hw + r
	qy := math.Abs(y) - hh + r
	return math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - r
}

// fillSDF composites (source-over) a shape given by a distance function,
// with colour and coverage chosen per pixel, over the bounds part of dst.
func fillSDF(dst *image.RGBA, bounds image.Rectangle, sdf func(x, y float64) float64, shade func(x, y, d float64) (color.RGBA, float64)) {
	b := bounds.Intersect(dst.Bounds())
	for py := b.Min.Y; py < b.Max.Y; py++ {
		for px := b.Min.X; px < b.Max.X; px++ {
			x, y := float64(px)+0.5, float64(py)+0.5
			c, a := shade(x, y, sdf(x, y))
			if a <= 0.002 {
				continue
			}
			blendOver(dst, px, py, c, a)
		}
	}
}

func fillCircle(dst *image.RGBA, cx, cy, r float64, c color.RGBA, alpha float64) {
	b := dst.Bounds()
	for py := max(int(cy-r-2), b.Min.Y); py < min(int(cy+r+2), b.Max.Y); py++ {
		for px := max(int(cx-r-2), b.Min.X); px < min(int(cx+r+2), b.Max.X); px++ {
			d := math.Hypot(float64(px)+0.5-cx, float64(py)+0.5-cy) - r
			if a := alpha * clamp01(0.5-d); a > 0 {
				blendOver(dst, px, py, c, a)
			}
		}
	}
}

// blendOver composites colour c at opacity a over one premultiplied pixel.
func blendOver(dst *image.RGBA, x, y int, c color.RGBA, a float64) {
	i := dst.PixOffset(x, y)
	p := dst.Pix[i : i+4 : i+4]
	k := 1 - a
	p[0] = uint8(float64(c.R)*a + float64(p[0])*k + 0.5)
	p[1] = uint8(float64(c.G)*a + float64(p[1])*k + 0.5)
	p[2] = uint8(float64(c.B)*a + float64(p[2])*k + 0.5)
	p[3] = uint8(255*a + float64(p[3])*k + 0.5)
}

func drawText(dst *image.RGBA, face font.Face, x, baseline float64, text string, c color.RGBA) {
	if face == nil || text == "" {
		return
	}
	d := &font.Drawer{Dst: dst, Src: image.NewUniform(c), Face: face,
		Dot: fixed.Point26_6{X: fixed.Int26_6(x * 64), Y: fixed.Int26_6(baseline * 64)}}
	d.DrawString(text)
}

// drawSpaced draws text with extra letter spacing (for the small caps
// status label) and returns its width.
func drawSpaced(dst *image.RGBA, face font.Face, x, baseline float64, text string, c color.RGBA, spacing float64) float64 {
	if face == nil {
		return 0
	}
	start := x
	for _, r := range text {
		ch := string(r)
		drawText(dst, face, x, baseline, ch, c)
		x += measure(face, ch) + spacing
	}
	return x - start - spacing
}

func measure(face font.Face, s string) float64 {
	return float64(font.MeasureString(face, s)) / 64
}

// ellipsize shortens s with "…" until it fits in width.
func ellipsize(face font.Face, s string, width float64) string {
	if face == nil || measure(face, s) <= width {
		return s
	}
	for s != "" {
		_, size := utf8.DecodeLastRuneInString(s)
		s = strings.TrimRight(s[:len(s)-size], " ")
		if measure(face, s+"…") <= width {
			return s + "…"
		}
	}
	return "…"
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }
