package main

import (
	"strings"
	"testing"
)

func TestExecutorPromptCoordinateModes(t *testing.T) {
	pixels := executorPrompt(coordsPixels)
	norm := executorPrompt(coordsNorm1000)
	if !strings.Contains(pixels, executorPixelCoords) {
		t.Fatal("pixel prompt lost its coordinate section")
	}
	if strings.Contains(norm, executorPixelCoords) || !strings.Contains(norm, executorNorm1000Coords) {
		t.Fatal("norm1000 prompt must replace the pixel coordinate section")
	}
}

func TestNorm1000ToPixels(t *testing.T) {
	got := norm1000ToPixels([]Execution{
		{Type: "MOUSE_MOVEMENT", MousePosX: 0, MousePosY: 1000},
		{Type: "MOUSE_MOVEMENT", MousePosX: 500, MousePosY: 500},
		{Type: "KEYBOARD_INPUT", KeyString: "x"},
	}, 1261, 701)
	if got[0].MousePosX != 0 || got[0].MousePosY != 700 || got[1].MousePosX != 630 || got[1].MousePosY != 350 {
		t.Fatalf("got %+v", got)
	}
}

func TestLimitToOneClick(t *testing.T) {
	move := Execution{Type: "MOUSE_MOVEMENT", MousePosX: 1, MousePosY: 1}
	click := Execution{Type: "LEFT_CLICK"}
	press := Execution{Type: "LEFT_CLICK", MouseHold: true}
	holdMove := Execution{Type: "MOUSE_MOVEMENT", MouseHold: true}
	typing := Execution{Type: "KEYBOARD_INPUT", KeyString: "hi{ENTER}"}

	cases := []struct {
		name     string
		in       []Execution
		wantKept int
	}{
		{"single click then typing is fine", []Execution{move, click, typing}, 3},
		{"second click is cut", []Execution{move, click, move, click}, 2},
		{"typing between clicks is kept", []Execution{move, click, typing, move, click}, 3},
		{"a drag is one click", []Execution{move, press, holdMove, click, typing}, 5},
		{"move after a drag is cut", []Execution{move, press, holdMove, click, move}, 4},
		{"keyboard only", []Execution{typing, typing}, 2},
	}
	for _, c := range cases {
		kept, cut := limitToOneClick(c.in)
		if len(kept) != c.wantKept || cut != len(c.in)-c.wantKept {
			t.Errorf("%s: kept %d cut %d, want kept %d", c.name, len(kept), cut, c.wantKept)
		}
	}
}

func TestMentionsFiles(t *testing.T) {
	yes := []string{"open the file", "Save it to my Downloads folder", "the report.pdf", `C:\Users\me`, "User answered: attach the screenshot"}
	no := []string{"The search results do not show Blossom's LinkedIn profile.", "a popup is in the way", "User answered: send it without a note"}
	for _, s := range yes {
		if !mentionsFiles(s) {
			t.Errorf("should mention files: %q", s)
		}
	}
	for _, s := range no {
		if mentionsFiles(s) {
			t.Errorf("should not mention files: %q", s)
		}
	}
}
