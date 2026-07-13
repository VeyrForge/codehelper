package web

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func solidPNG(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// halfRedPNG: left half color a, right half color b — a known % difference.
func halfPNG(t *testing.T, w, h int, left, right color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x < w/2 {
				img.Set(x, y, left)
			} else {
				img.Set(x, y, right)
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func TestDiffIdentical(t *testing.T) {
	a := solidPNG(t, 40, 40, color.RGBA{10, 20, 30, 255})
	d, err := DiffImages(a, a)
	if err != nil {
		t.Fatal(err)
	}
	if d.ChangedPct != 0 || d.ChangedPix != 0 {
		t.Fatalf("identical images should be 0%% changed, got %.2f%%", d.ChangedPct)
	}
	if len(d.DiffPNG) == 0 {
		t.Error("expected a diff image even for identical inputs")
	}
}

func TestDiffHalfChanged(t *testing.T) {
	white := color.RGBA{255, 255, 255, 255}
	black := color.RGBA{0, 0, 0, 255}
	base := solidPNG(t, 100, 10, white)
	cur := halfPNG(t, 100, 10, white, black) // right half flipped to black
	d, err := DiffImages(base, cur)
	if err != nil {
		t.Fatal(err)
	}
	if d.ChangedPct < 45 || d.ChangedPct > 55 {
		t.Fatalf("expected ~50%% changed, got %.2f%%", d.ChangedPct)
	}
}

func TestDiffSizeMismatch(t *testing.T) {
	a := solidPNG(t, 40, 40, color.RGBA{0, 0, 0, 255})
	b := solidPNG(t, 50, 40, color.RGBA{0, 0, 0, 255})
	d, err := DiffImages(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if !d.SizeMismatch || d.ChangedPct != 100 {
		t.Fatalf("size mismatch should be 100%% + flagged, got %+v", d)
	}
	if d.BaselineDim != "40x40" || d.CurrentDim != "50x40" {
		t.Errorf("dims wrong: %s vs %s", d.BaselineDim, d.CurrentDim)
	}
}

func TestBaselineStorageRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	data := solidPNG(t, 8, 8, color.RGBA{1, 2, 3, 255})
	if _, ok := LoadBaseline("home"); ok {
		t.Fatal("no baseline should exist yet")
	}
	if err := SaveBaseline("home", FormatPNG, data); err != nil {
		t.Fatal(err)
	}
	got, ok := LoadBaseline("home")
	if !ok || !bytes.Equal(got, data) {
		t.Fatal("baseline round trip failed")
	}
}
