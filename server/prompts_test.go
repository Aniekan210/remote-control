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
