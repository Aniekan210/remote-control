package main

import (
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// TestOverlayPreview renders the overlay over a fake desktop, one PNG per
// status, to look at the design without a Windows machine:
//
//	OVERLAY_PREVIEW_DIR=/tmp/overlay GOOS=... go test -run OverlayPreview .
func TestOverlayPreview(t *testing.T) {
	dir := os.Getenv("OVERLAY_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set OVERLAY_PREVIEW_DIR to write preview PNGs")
	}
	const sw, sh, scale = 1920, 1080, 1.0
	states := map[string]OverlayState{
		"running": {Visible: true, Status: "RUNNING", TaskDescription: "Reply 'thanks!' to the newest email in Gmail",
			StepText: "Open the reply editor for that email.", ActionText: "Click (1412, 388)", StepIndex: 4, StepTotal: 7},
		"planning":    {Visible: true, Status: "RUNNING", TaskDescription: "Play lo-fi music on YouTube", StepText: "Planning your task..."},
		"needs_input": {Visible: true, Status: "NEEDS_INPUT", TaskDescription: "Invite Sam on LinkedIn", StepText: "Waiting for you on your phone — Send the invite.", StepIndex: 6, StepTotal: 8},
		"paused":      {Visible: true, Status: "PAUSED", TaskDescription: "Search for cats on Google", StepText: "Focus the Google search box.", StepIndex: 3, StepTotal: 6},
		"done":        {Visible: true, Status: "COMPLETED", TaskDescription: "Open Notepad", StepText: "Task completed", StepIndex: 2, StepTotal: 2},
	}
	faces := newPillFaces(scale)
	for name, st := range states {
		img := fakeDesktop(sw, sh)
		th := themeFor(st.Status)
		for _, r := range edgeStrips(sw, sh, scale) {
			m := newEdgeMask(r, sw, sh, scale)
			strip := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
			renderEdge(strip, m, th, 0.1, th.intensity(0.9))
			draw.Draw(img, r, strip, image.Point{}, draw.Over)
		}
		pw, ph := pillSize(scale)
		pill := image.NewRGBA(image.Rect(0, 0, pw, ph))
		renderPill(pill, newPillBase(scale), st, faces, scale, 0.9)
		at := pillOrigin(sw, scale)
		draw.Draw(img, image.Rect(at.X, at.Y, at.X+pw, at.Y+ph), pill, image.Point{}, draw.Over)

		f, err := os.Create(filepath.Join(dir, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		png.Encode(f, img)
		f.Close()
	}
}

// fakeDesktop is a browser-ish window on a wallpaper, to judge contrast.
func fakeDesktop(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(30 + x*40/w), uint8(60 + y*50/h), 110, 255})
		}
	}
	draw.Draw(img, image.Rect(0, 0, w, h-48), &image.Uniform{color.RGBA{248, 249, 250, 255}}, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(0, 0, w, 86), &image.Uniform{color.RGBA{222, 225, 230, 255}}, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(10, 8, 330, 44), &image.Uniform{color.RGBA{248, 249, 250, 255}}, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(90, 50, w-200, 80), &image.Uniform{color.RGBA{255, 255, 255, 255}}, image.Point{}, draw.Src)
	for y := 150; y < h-150; y += 60 {
		draw.Draw(img, image.Rect(260, y, 1300, y+14), &image.Uniform{color.RGBA{215, 218, 224, 255}}, image.Point{}, draw.Src)
	}
	draw.Draw(img, image.Rect(0, h-48, w, h), &image.Uniform{color.RGBA{32, 32, 36, 255}}, image.Point{}, draw.Src)
	return img
}
