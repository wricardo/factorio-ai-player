package main

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validScreenshotInput() ScreenshotInput {
	return ScreenshotInput{
		Player: "viewer", Surface: "nauvis", Position: &PositionInput{X: 12.5, Y: -4.5},
		Width: 320, Height: 180, Zoom: 1, ShowEntityInfo: true,
	}
}

func TestScreenshotValidationBounds(t *testing.T) {
	daytime := 0.5
	waterTick := 0
	base := validScreenshotInput()
	base.Daytime = &daytime
	base.WaterTick = &waterTick
	if err := validateScreenshotInput(base); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}

	tests := []struct {
		name string
		edit func(*ScreenshotInput)
		want string
	}{
		{"blank player", func(in *ScreenshotInput) { in.Player = " \t" }, "player"},
		{"blank surface", func(in *ScreenshotInput) { in.Surface = "\n" }, "surface"},
		{"missing position", func(in *ScreenshotInput) { in.Position = nil }, "position"},
		{"nonfinite x", func(in *ScreenshotInput) { in.Position.X = math.NaN() }, "finite"},
		{"zero width", func(in *ScreenshotInput) { in.Width = 0 }, "width"},
		{"oversize height", func(in *ScreenshotInput) { in.Height = 4097 }, "height"},
		{"anti alias width", func(in *ScreenshotInput) { in.AntiAlias = true; in.Width = 2049 }, "2048"},
		{"zero zoom", func(in *ScreenshotInput) { in.Zoom = 0 }, "zoom"},
		{"infinite zoom", func(in *ScreenshotInput) { in.Zoom = math.Inf(1) }, "zoom"},
		{"negative daytime", func(in *ScreenshotInput) { value := -0.01; in.Daytime = &value }, "daytime"},
		{"infinite daytime", func(in *ScreenshotInput) { value := math.Inf(1); in.Daytime = &value }, "daytime"},
		{"negative water tick", func(in *ScreenshotInput) { value := -1; in.WaterTick = &value }, "waterTick"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			position := *base.Position
			input.Position = &position
			test.edit(&input)
			if err := validateScreenshotInput(input); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q validation error, got %v", test.want, err)
			}
		})
	}
}

func TestScreenshotRequiresFreshVerifiedPNGBeforeSuccess(t *testing.T) {
	const relative = "factorio-ai-player/test-capture.png"
	root := t.TempDir()
	fake := newFakeRCON(t, `{"tick":321,"surface":"nauvis","player":"viewer"}`, "")
	fake.resolver.screenshotDir = root
	fake.resolver.screenshotTimeout = time.Second
	fake.resolver.screenshotPath = func() (string, error) { return relative, nil }
	input := validScreenshotInput()

	writeErr := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(time.Second)
		for len(fake.commands()) == 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		writeErr <- writeTestPNG(filepath.Join(root, filepath.FromSlash(relative)), input.Width, input.Height)
	}()

	result, err := screenshotMutation(t.Context(), fake.resolver, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-writeErr; err != nil {
		t.Fatal(err)
	}
	if result.Tick != 321 || result.Surface != "nauvis" || result.Player != "viewer" || result.Path != relative {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.Width != input.Width || result.Height != input.Height || result.Bytes <= 0 {
		t.Fatalf("invalid PNG metadata: %#v", result)
	}
}

func TestScreenshotMissingPNGReturnsErrorWithoutAnnouncement(t *testing.T) {
	fake := newFakeRCON(t, `{"tick":321,"surface":"nauvis","player":"viewer"}`)
	fake.resolver.screenshotDir = t.TempDir()
	fake.resolver.screenshotTimeout = 40 * time.Millisecond
	fake.resolver.screenshotPath = func() (string, error) { return "factorio-ai-player/missing.png", nil }

	if _, err := screenshotMutation(t.Context(), fake.resolver, validScreenshotInput()); err == nil || !strings.Contains(err.Error(), "PNG has not appeared") {
		t.Fatalf("expected missing PNG error, got %v", err)
	}
	if commands := fake.commands(); len(commands) != 1 {
		t.Fatalf("failed capture announced; commands=%v", commands)
	}
}

func TestScreenshotRejectsPreexistingPNG(t *testing.T) {
	const relative = "factorio-ai-player/existing.png"
	root := t.TempDir()
	filename := filepath.Join(root, filepath.FromSlash(relative))
	if err := writeTestPNG(filename, 320, 180); err != nil {
		t.Fatal(err)
	}
	r := &Resolver{
		addr: "127.0.0.1:1", screenshotDir: root,
		screenshotPath: func() (string, error) { return relative, nil },
	}
	if _, err := screenshotMutation(t.Context(), r, validScreenshotInput()); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected stale path rejection, got %v", err)
	}
}

func TestInspectScreenshotWaitsForCompletePNG(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "partial.png")
	if err := writeTestPNG(filename, 32, 18); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filename)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(filename, info.Size()-int64(len(pngEndMarker))); err != nil {
		t.Fatal(err)
	}
	if _, pending, err := inspectScreenshot(filename, 32, 18); err == nil || !pending || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("expected incomplete PNG to remain pending, got pending=%t err=%v", pending, err)
	}
}

func writeTestPNG(filename string, width, height int) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		return err
	}
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	image := image.NewNRGBA(image.Rect(0, 0, width, height))
	image.Set(0, 0, color.NRGBA{R: 255, A: 255})
	encodeErr := png.Encode(file, image)
	closeErr := file.Close()
	if encodeErr != nil {
		return encodeErr
	}
	return closeErr
}
