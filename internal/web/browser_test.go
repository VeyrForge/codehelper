package web

import "testing"

func TestDevicePresetsHaveSaneDimensions(t *testing.T) {
	want := map[string]struct{ w, h int }{
		"desktop": {1280, 800},
		"tablet":  {768, 1024},
		"mobile":  {390, 844},
	}
	for name, dim := range want {
		d, ok := ResolveDevice(name)
		if !ok {
			t.Fatalf("device %q missing", name)
		}
		if d.Width != dim.w || d.Height != dim.h {
			t.Errorf("device %q = %dx%d, want %dx%d", name, d.Width, d.Height, dim.w, dim.h)
		}
		if d.Scale < 1 || d.Scale > 2 {
			t.Errorf("device %q scale %g outside [1,2]", name, d.Scale)
		}
	}
}

func TestResolveDeviceDefaultAndUnknown(t *testing.T) {
	d, ok := ResolveDevice("")
	if !ok || d.Name != "desktop" {
		t.Fatalf(`ResolveDevice("") = %q,%v; want desktop,true`, d.Name, ok)
	}
	if _, ok := ResolveDevice("watch"); ok {
		t.Fatal("unknown device should return ok=false")
	}
}

func TestNormalizeFormatAndMIME(t *testing.T) {
	cases := map[string]struct{ norm, mime string }{
		"":      {FormatWebP, "image/webp"},
		"bogus": {FormatWebP, "image/webp"},
		"png":   {FormatPNG, "image/png"},
		"jpeg":  {FormatJPEG, "image/jpeg"},
		"webp":  {FormatWebP, "image/webp"},
	}
	for in, want := range cases {
		if got := NormalizeFormat(in); got != want.norm {
			t.Errorf("NormalizeFormat(%q)=%q want %q", in, got, want.norm)
		}
		if got := MIMEForFormat(NormalizeFormat(in)); got != want.mime {
			t.Errorf("MIMEForFormat(%q)=%q want %q", in, got, want.mime)
		}
	}
}

// ImageMagicMatches is the guard against the Playwright media-type bug — it must
// accept real magic bytes and reject a format/bytes mismatch.
func TestImageMagicMatches(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0}
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 0, 0}
	webp := append([]byte("RIFF"), append([]byte{0, 0, 0, 0}, []byte("WEBPxx")...)...)

	if !ImageMagicMatches(png, FormatPNG) {
		t.Error("png magic not accepted")
	}
	if !ImageMagicMatches(jpeg, FormatJPEG) {
		t.Error("jpeg magic not accepted")
	}
	if !ImageMagicMatches(webp, FormatWebP) {
		t.Error("webp magic not accepted")
	}
	// Mismatches must be rejected (this is what prevents the 400).
	if ImageMagicMatches(png, FormatWebP) {
		t.Error("png bytes wrongly accepted as webp")
	}
	if ImageMagicMatches(jpeg, FormatPNG) {
		t.Error("jpeg bytes wrongly accepted as png")
	}
	if ImageMagicMatches(nil, FormatWebP) {
		t.Error("nil wrongly accepted")
	}
}

func TestGuardURL(t *testing.T) {
	tests := []struct {
		url          string
		allowPrivate bool
		wantErr      bool
	}{
		{"http://127.0.0.1:3000/", false, false},       // loopback always allowed
		{"https://127.0.0.1/health", false, false},     // loopback https
		{"http://192.168.1.10/", false, true},          // private blocked by default
		{"http://192.168.1.10/", true, false},          // private allowed when opted in
		{"http://169.254.169.254/latest/", true, true}, // cloud metadata always blocked
		{"file:///etc/passwd", false, true},            // non-http scheme
		{"ftp://127.0.0.1/", false, true},              // non-http scheme
		{"http://", false, true},                       // no host
	}
	for _, tt := range tests {
		err := GuardURL(tt.url, tt.allowPrivate)
		if (err != nil) != tt.wantErr {
			t.Errorf("GuardURL(%q, allowPrivate=%v) err=%v, wantErr=%v", tt.url, tt.allowPrivate, err, tt.wantErr)
		}
	}
}
