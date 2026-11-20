package manifest

import (
	"strings"
	"testing"
)

func TestCodeFromFileName(t *testing.T) {
	cases := map[string]string{
		`C:\Users\me\Downloads\Fileion-VLC-7-Zip-and-4-more-3fa91c0b.exe`: "3fa91c0b",
		"Fileion-VLC-3FA91C0B (1).exe":                                    "3fa91c0b",
		"/home/me/Fileion-Firefox-ab12cd34ef.AppImage":                    "ab12cd34ef",
		"Fileion-Installer.exe":                                           "",
		"installer.exe":                                                   "",
		"Fileion-VLC-xyz12345.exe":                                        "",
	}
	for in, want := range cases {
		if got := CodeFromFileName(in); got != want {
			t.Errorf("CodeFromFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseKeepsOnlyInstallableWindowsApps(t *testing.T) {
	json := `{"code":"3fa91c0b","os":"windows","apps":[
		{"id":1,"name":"VLC","url":"https://vendor.example/vlc.exe","sha256":"` + strings.Repeat("AB", 32) + `","silent_switch":"/S","min_version":"3.0.20","product_code":"VLC"},
		{"id":2,"name":"Plain http","url":"http://vendor.example/a.exe","sha256":"` + strings.Repeat("ab", 32) + `"},
		{"id":3,"name":"No hash","url":"https://vendor.example/b.exe","sha256":""},
		{"id":4,"name":"Bad hash","url":"https://vendor.example/c.exe","sha256":"` + strings.Repeat("zz", 32) + `"}]}`
	m, err := Parse(strings.NewReader(json), "windows")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Apps) != 1 || m.Apps[0].Name != "VLC" {
		t.Fatalf("got %+v", m.Apps)
	}
	if m.Apps[0].SHA256 != strings.Repeat("ab", 32) || m.Apps[0].MinVersion != "3.0.20" || m.Apps[0].SilentSwitch != "/S" {
		t.Errorf("fields not read: %+v", m.Apps[0])
	}
}

func TestParseLinuxNeedsAFlatpakID(t *testing.T) {
	m, err := Parse(strings.NewReader(`{"apps":[{"id":1,"name":"Firefox","flatpak_id":"org.mozilla.firefox"},{"id":2,"name":"None"}]}`), "linux")
	if err != nil || len(m.Apps) != 1 || m.Apps[0].FlatpakID != "org.mozilla.firefox" {
		t.Fatalf("got %+v, %v", m, err)
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, err := Parse(strings.NewReader("<html>"), "windows"); err == nil {
		t.Fatal("expected an error")
	}
}
