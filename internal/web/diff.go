package web

// Visual regression diff: decode two screenshots, compare them pixel-by-pixel,
// and produce a highlighted diff image + a "% changed" number. Pure Go and
// untagged (no browser needed), so the diff logic is unit-testable on its own.
//
// Decoding covers the formats the browser tool can emit — PNG/JPEG via stdlib,
// WebP via golang.org/x/image/webp (decode-only, pure Go). The diff image is
// emitted as PNG so any vision model can view it.

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg" // register decoders for image.Decode
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	_ "golang.org/x/image/webp" // register WebP decoder
)

// DiffResult is the outcome of comparing a baseline screenshot to a new one.
type DiffResult struct {
	ChangedPct   float64 // fraction of pixels that differ, 0-100
	ChangedPix   int
	TotalPix     int
	SizeMismatch bool   // baseline and current have different dimensions
	BaselineDim  string // "WxH"
	CurrentDim   string // "WxH"
	DiffPNG      []byte // changed pixels highlighted red over a faded base (PNG)
}

// pixelThreshold is the per-pixel summed-channel difference above which a pixel
// counts as "changed" — small enough to catch real visual changes, large enough
// to ignore antialiasing/encoder jitter.
const pixelThreshold = 60

// DiffImages decodes baseline and current (any of png/jpeg/webp) and diffs them.
func DiffImages(baseline, current []byte) (*DiffResult, error) {
	base, _, err := image.Decode(bytes.NewReader(baseline))
	if err != nil {
		return nil, fmt.Errorf("decode baseline: %w", err)
	}
	cur, _, err := image.Decode(bytes.NewReader(current))
	if err != nil {
		return nil, fmt.Errorf("decode current: %w", err)
	}
	bb, cb := base.Bounds(), cur.Bounds()
	res := &DiffResult{
		BaselineDim: fmt.Sprintf("%dx%d", bb.Dx(), bb.Dy()),
		CurrentDim:  fmt.Sprintf("%dx%d", cb.Dx(), cb.Dy()),
	}
	if bb.Dx() != cb.Dx() || bb.Dy() != cb.Dy() {
		// Different dimensions can't be pixel-aligned — report it as a full change
		// and return the current frame as the "diff" so the agent still sees it.
		res.SizeMismatch = true
		res.ChangedPct = 100
		png, _ := encodePNG(cur)
		res.DiffPNG = png
		return res, nil
	}

	w, h := bb.Dx(), bb.Dy()
	res.TotalPix = w * h
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			br, bg, bb2, _ := base.At(bb.Min.X+x, bb.Min.Y+y).RGBA()
			cr, cg, cb2, _ := cur.At(cb.Min.X+x, cb.Min.Y+y).RGBA()
			// RGBA() returns 16-bit; shift to 8-bit for a stable threshold.
			d := abs8(br, cr) + abs8(bg, cg) + abs8(bb2, cb2)
			if d > pixelThreshold {
				res.ChangedPix++
				out.Set(x, y, color.RGBA{R: 255, G: 0, B: 0, A: 255})
			} else {
				// Fade unchanged pixels so the red changes stand out.
				out.Set(x, y, fade(cur.At(cb.Min.X+x, cb.Min.Y+y)))
			}
		}
	}
	if res.TotalPix > 0 {
		res.ChangedPct = float64(res.ChangedPix) / float64(res.TotalPix) * 100
	}
	png, err := encodePNG(out)
	if err != nil {
		return nil, err
	}
	res.DiffPNG = png
	return res, nil
}

func abs8(a, b uint32) int {
	x, y := int(a>>8), int(b>>8)
	if x > y {
		return x - y
	}
	return y - x
}

// fade lightens a pixel toward white so the red diff overlay reads clearly.
func fade(c color.Color) color.RGBA {
	r, g, b, _ := c.RGBA()
	mix := func(v uint32) uint8 { return uint8((int(v>>8) + 3*255) / 4) }
	return color.RGBA{R: mix(r), G: mix(g), B: mix(b), A: 255}
}

func encodePNG(img image.Image) ([]byte, error) {
	// Flatten to RGBA in case the source has an alpha/paletted model.
	rgba, ok := img.(*image.RGBA)
	if !ok {
		b := img.Bounds()
		rgba = image.NewRGBA(b)
		draw.Draw(rgba, b, img, b.Min, draw.Src)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, rgba); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ---- baseline storage (under ~/.codehelper/browser/baselines) ----

var baselineNameRe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// BaselineDir is where visual-regression baselines are stored.
func BaselineDir() (string, error) {
	base, err := BrowserDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "baselines"), nil
}

func baselinePath(name, format string) (string, error) {
	dir, err := BaselineDir()
	if err != nil {
		return "", err
	}
	safe := strings.Trim(baselineNameRe.ReplaceAllString(name, "_"), "_")
	if safe == "" {
		safe = "baseline"
	}
	return filepath.Join(dir, safe+"."+NormalizeFormat(format)), nil
}

// LoadBaseline returns the stored baseline bytes for name (any saved format).
func LoadBaseline(name string) ([]byte, bool) {
	for _, f := range []string{FormatWebP, FormatPNG, FormatJPEG} {
		if p, err := baselinePath(name, f); err == nil {
			if data, err := os.ReadFile(p); err == nil {
				return data, true
			}
		}
	}
	return nil, false
}

// SaveBaseline writes (or overwrites) the baseline for name in the given format.
func SaveBaseline(name, format string, data []byte) error {
	p, err := baselinePath(name, format)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}
