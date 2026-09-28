package main

import "testing"

func TestDownscaleDims(t *testing.T) {
	cases := []struct{ w, h, wantW, wantH int }{
		{1920, 1080, 1260, 700},
		{2560, 1440, 1260, 700},
		{3840, 2160, 1260, 700},
		{1920, 1200, 1260, 784},
		{1366, 768, 1260, 700},
		{1024, 768, 1008, 756},
	}
	for _, c := range cases {
		w, h := downscaleDims(c.w, c.h)
		if w != c.wantW || h != c.wantH {
			t.Errorf("%dx%d -> %dx%d, want %dx%d", c.w, c.h, w, h, c.wantW, c.wantH)
		}
		if w%28 != 0 || h%28 != 0 || w > 1280 || h > 1280 || w*h > 1_000_000 {
			t.Errorf("%dx%d -> %dx%d breaks a limit", c.w, c.h, w, h)
		}
	}
}

func TestToScreen(t *testing.T) {
	f := shotFrame{imgW: 1260, imgH: 700, screenW: 1920, screenH: 1080}
	for _, c := range []struct{ x, y, wantX, wantY int }{
		{0, 0, 0, 0},
		{1259, 699, 1919, 1079},
		{630, 350, 960, 540},
		{1261, 700, 1919, 1079}, // small overshoot is clamped
	} {
		x, y, err := f.toScreen(c.x, c.y)
		if err != nil || x != c.wantX || y != c.wantY {
			t.Errorf("(%d,%d) -> (%d,%d) %v, want (%d,%d)", c.x, c.y, x, y, err, c.wantX, c.wantY)
		}
	}
	if _, _, err := f.toScreen(1300, 10); err == nil {
		t.Error("far outside the image must be an error")
	}
	if _, _, err := f.toScreen(-20, 10); err == nil {
		t.Error("negative coordinates must be an error")
	}
}
