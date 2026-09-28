package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"testing"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// TestExecutorCalibration measures how accurately the executor model clicks
// (F1). It renders a synthetic 1920x1080 screen with five labelled buttons
// at known positions, asks the executor to click each one, and checks every
// click lands inside its button — once with the native-resolution PNG (how
// screenshots used to be sent) and once downscaled exactly like the worker
// does now (desktop-worker/scaling.go), with the answer scaled back to
// screen pixels. If the downscaled error is clearly worse, set
// EXECUTOR_FULL_RES=1 on the worker to keep native resolution for the
// executor (the planner stays downscaled).
//
// It makes 10 real executor calls, so it only runs when asked:
//
//	RUN_CALIBRATION=1 OPENROUTER_API_KEY=sk-or-... go test -run Calibration -v .
//
// EXECUTION_MODEL / EXECUTION_COORDS pick the model and coordinate space.
func TestExecutorCalibration(t *testing.T) {
	key := os.Getenv("OPENROUTER_API_KEY")
	if os.Getenv("RUN_CALIBRATION") != "1" || key == "" {
		t.Skip("set RUN_CALIBRATION=1 and OPENROUTER_API_KEY to run (costs money)")
	}

	type button struct {
		label string
		box   image.Rectangle
	}
	buttons := []button{
		{"Save", image.Rect(120, 90, 280, 138)},
		{"Cancel", image.Rect(1650, 110, 1830, 158)},
		{"Export", image.Rect(870, 515, 1050, 563)},
		{"Settings", image.Rect(200, 930, 400, 978)},
		{"Help", image.Rect(1760, 1000, 1880, 1048)},
	}
	screen := renderCalibrationScreen(1920, 1080, func(draw func(label string, box image.Rectangle)) {
		for _, b := range buttons {
			draw(b.label, b.box)
		}
	})

	for _, mode := range []string{"native", "downscaled"} {
		shot, frameW, frameH := calibrationShot(t, screen, mode == "downscaled")
		var totalErr float64
		misses := 0
		for _, b := range buttons {
			task := Task{
				DeviceID:        "calibration",
				Description:     "Click the " + b.label + " button.",
				InstructionList: []string{"Click the \"" + b.label + "\" button."},
				ConfirmedIndex:  -1,
			}
			out, cost, err := callExecutor("calibration-"+mode+"-"+b.label, task,
				Action{Type: "ADVANCE", ScreenshotPayload: shot}, apiKey{value: key})
			if err != nil {
				t.Fatalf("%s %s: %v", mode, b.label, err)
			}
			res := out.(ExecResult)
			x, y, ok := firstMove(res.Actions)
			if !ok {
				t.Errorf("%s %s: no MOUSE_MOVEMENT in %+v", mode, b.label, res)
				misses++
				continue
			}
			// Back to screen pixels, the way the worker does it.
			sx := int(math.Round((float64(x)+0.5)*1920/float64(frameW) - 0.5))
			sy := int(math.Round((float64(y)+0.5)*1080/float64(frameH) - 0.5))
			c := image.Pt((b.box.Min.X+b.box.Max.X)/2, (b.box.Min.Y+b.box.Max.Y)/2)
			dist := math.Hypot(float64(sx-c.X), float64(sy-c.Y))
			totalErr += dist
			hit := image.Pt(sx, sy).In(b.box)
			if !hit {
				misses++
			}
			t.Logf("%-10s %-8s image (%d,%d) -> screen (%d,%d), centre (%d,%d), off by %.1fpx, hit=%v, $%.5f",
				mode, b.label, x, y, sx, sy, c.X, c.Y, dist, hit, cost)
		}
		t.Logf("%s: mean error %.1fpx, %d/%d missed", mode, totalErr/float64(len(buttons)), misses, len(buttons))
		if misses > 0 {
			t.Errorf("%s: %d click(s) missed their button", mode, misses)
		}
	}
}

func firstMove(actions []Execution) (int, int, bool) {
	for _, a := range actions {
		if a.Type == "MOUSE_MOVEMENT" {
			return a.MousePosX, a.MousePosY, true
		}
	}
	return 0, 0, false
}

// calibrationShot encodes the screen as the worker would: a native PNG, or
// downscaled (longest side <= 1280, <= 1MP, multiples of 28) JPEG q80.
func calibrationShot(t *testing.T, img *image.RGBA, downscale bool) (Screenshot, int, int) {
	t.Helper()
	b := img.Bounds()
	var buf bytes.Buffer
	if !downscale {
		if err := png.Encode(&buf, img); err != nil {
			t.Fatal(err)
		}
		return Screenshot{Format: "png", Width: uint32(b.Dx()), Height: uint32(b.Dy()),
			ScreenWidth: uint32(b.Dx()), ScreenHeight: uint32(b.Dy()), Data: buf.Bytes()}, b.Dx(), b.Dy()
	}
	scale := math.Min(1, 1280/float64(max(b.Dx(), b.Dy())))
	scale = math.Min(scale, math.Sqrt(1_000_000/float64(b.Dx()*b.Dy())))
	w := max(int(float64(b.Dx())*scale)/28*28, 28)
	h := max(int(float64(b.Dy())*scale)/28*28, 28)
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	return Screenshot{Format: "jpeg", Width: uint32(w), Height: uint32(h),
		ScreenWidth: uint32(b.Dx()), ScreenHeight: uint32(b.Dy()), Data: buf.Bytes()}, w, h
}

// renderCalibrationScreen draws a plain app-like background with a few
// distractor lines and the buttons (label text drawn at 2x, ~26px tall).
func renderCalibrationScreen(w, h int, buttons func(func(string, image.Rectangle))) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{243, 244, 246, 255}}, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(0, 0, w, 48), &image.Uniform{color.RGBA{30, 41, 59, 255}}, image.Point{}, draw.Src)
	for y := 200; y < h-150; y += 90 {
		draw.Draw(img, image.Rect(480, y, 1440, y+2), &image.Uniform{color.RGBA{209, 213, 219, 255}}, image.Point{}, draw.Src)
	}
	buttons(func(label string, box image.Rectangle) {
		draw.Draw(img, box, &image.Uniform{color.RGBA{37, 99, 235, 255}}, image.Point{}, draw.Src)
		// Text at 1x on a scratch image, then scaled 2x into the button.
		face := basicfont.Face7x13
		tw := font.MeasureString(face, label).Ceil()
		small := image.NewRGBA(image.Rect(0, 0, tw, 13))
		d := &font.Drawer{Dst: small, Src: image.White, Face: face, Dot: fixed.P(0, 10)}
		d.DrawString(label)
		big := image.Rect(0, 0, tw*2, 26)
		at := image.Pt(box.Min.X+(box.Dx()-big.Dx())/2, box.Min.Y+(box.Dy()-big.Dy())/2)
		draw.NearestNeighbor.Scale(img, big.Add(at), small, small.Bounds(), draw.Over, nil)
	})
	return img
}

// Writes the calibration screen to a file, to eyeball it:
//
//	RUN_CALIBRATION_IMAGE=/tmp/cal.png go test -run CalibrationImage .
func TestCalibrationImage(t *testing.T) {
	path := os.Getenv("RUN_CALIBRATION_IMAGE")
	if path == "" {
		t.Skip("set RUN_CALIBRATION_IMAGE=<file.png> to write the calibration screen")
	}
	img := renderCalibrationScreen(1920, 1080, func(draw func(string, image.Rectangle)) {
		draw("Save", image.Rect(120, 90, 280, 138))
		draw("Export", image.Rect(870, 515, 1050, 563))
	})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	fmt.Println("wrote", path)
}
