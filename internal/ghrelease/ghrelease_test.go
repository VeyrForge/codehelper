package ghrelease

import "testing"

func TestExpectedArchiveName(t *testing.T) {
	if got := expectedArchiveName("2.4.1", "windows", "amd64"); got != "codehelper_2.4.1_windows_amd64.zip" {
		t.Fatalf("windows: %q", got)
	}
	if got := expectedArchiveName("2.4.1", "linux", "amd64"); got != "codehelper_2.4.1_linux_amd64.tar.gz" {
		t.Fatalf("linux: %q", got)
	}
}

func TestStripV(t *testing.T) {
	if stripV("v1.2.3") != "1.2.3" {
		t.Fatal()
	}
	if stripV("1.2.3") != "1.2.3" {
		t.Fatal()
	}
}
