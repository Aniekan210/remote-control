package main

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"math"
	"os"

	"golang.org/x/image/draw"
)

// Screenshots are downscaled before they're sent (F1): image tokens are a
// large part of every AI call's cost, and a 4K PNG is several MB. The AI
// then answers in the coordinates of the image it saw, so every click is
// scaled back to real screen pixels here before the mouse moves.
//
// Target: longest side at most 1280, total at most ~1,000,000 pixels, both
// sides multiples of 28 (the patch size vision models like Qwen2.5-VL tile
// images into, so nothing gets resampled again on their side), JPEG q80.
const (
	shotMaxSide   = 1280
	shotMaxPixels = 1_000_000
	shotMultiple  = 28
	shotJPEGQ     = 80
)

// downscaleDims returns the size a w x h screen is sent at.
func downscaleDims(w, h int) (int, int) {
	if w <= 0 || h <= 0 {
		return w, h
	}
	scale := math.Min(1, float64(shotMaxSide)/float64(max(w, h)))
	scale = math.Min(scale, math.Sqrt(float64(shotMaxPixels)/float64(w*h)))
	fit := func(v int) int {
		n := int(float64(v)*scale) / shotMultiple * shotMultiple
		return max(n, shotMultiple)
	}
	return fit(w), fit(h)
}

// encodeScreenshot turns a captured frame into the payload sent to the
// server. With fullRes it keeps the old native-resolution PNG (see
// EXECUTOR_FULL_RES); otherwise it's downscaled with CatmullRom and JPEG
// encoded. Width/Height are the image's size, ScreenWidth/ScreenHeight the
// real screen's.
func encodeScreenshot(img image.Image, fullRes bool) (Screenshot, error) {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if fullRes {
		shot, err := encodePNG(img)
		shot.ScreenWidth, shot.ScreenHeight = uint32(sw), uint32(sh)
		return shot, err
	}

	iw, ih := downscaleDims(sw, sh)
	dst := image.NewRGBA(image.Rect(0, 0, iw, ih))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: shotJPEGQ}); err != nil {
		return Screenshot{}, err
	}
	return Screenshot{
		Format:       "jpeg",
		Width:        uint32(iw),
		Height:       uint32(ih),
		ScreenWidth:  uint32(sw),
		ScreenHeight: uint32(sh),
		Data:         buf.Bytes(),
	}, nil
}

// encodePNG wraps an image as a native-resolution PNG Screenshot payload
// (the format before downscaling existed; see encodeScreenshot).
func encodePNG(img image.Image) (Screenshot, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return Screenshot{}, err
	}
	b := img.Bounds()
	return Screenshot{
		Format: "png",
		Width:  uint32(b.Dx()),
		Height: uint32(b.Dy()),
		Data:   buf.Bytes(),
	}, nil
}

// executorFullRes: EXECUTOR_FULL_RES=1 sends native-resolution PNGs for
// executor calls (planner calls are still downscaled). The escape hatch if
// the calibration test shows clicks got less accurate after downscaling.
var executorFullRes = os.Getenv("EXECUTOR_FULL_RES") == "1"

// coordTolerance is how far outside the image (in image pixels) a
// coordinate may land and still be clamped onto its edge — a model aiming
// at a control on the very edge can overshoot by a pixel or two. Anything
// further out is an error (the server replans).
const coordTolerance = 4

// shotFrame is the image a batch of coordinates refers to: the last
// screenshot sent, and the real screen it was taken of.
type shotFrame struct {
	imgW, imgH       int
	screenW, screenH int
}

// toScreen maps a point in the screenshot the AI saw onto real screen
// pixels, clamping small overshoots onto the edge. Pixel centres map to
// pixel centres, so a click on image pixel x lands in the middle of the
// screen pixels it was sampled from.
func (f shotFrame) toScreen(x, y int) (int, int, error) {
	if f.imgW <= 0 || f.imgH <= 0 || f.screenW <= 0 || f.screenH <= 0 {
		// No frame known (no screenshot sent yet): trust the coordinates.
		return x, y, nil
	}
	if x < -coordTolerance || y < -coordTolerance || x >= f.imgW+coordTolerance || y >= f.imgH+coordTolerance {
		return 0, 0, fmt.Errorf("coordinates (%d, %d) are outside the %dx%d screenshot", x, y, f.imgW, f.imgH)
	}
	x = min(max(x, 0), f.imgW-1)
	y = min(max(y, 0), f.imgH-1)
	sx := int(math.Round((float64(x)+0.5)*float64(f.screenW)/float64(f.imgW) - 0.5))
	sy := int(math.Round((float64(y)+0.5)*float64(f.screenH)/float64(f.imgH) - 0.5))
	return min(max(sx, 0), f.screenW-1), min(max(sy, 0), f.screenH-1), nil
}
