package manifest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCodeFromFileName(t *testing.T) {
	cases := map[string]string{
		`C:\Users\me\Downloads\Fileion-VLC-7-Zip-and-4-more-3fa91c0b.exe`: "3fa91c0b",
		"Fileion-VLC-3FA91C0B (1).exe":                                    "3fa91c0b",
		"/home/me/Fileion-Firefox-ab12cd34ef.AppImage":                    "ab12cd34ef",
		"Fileion-Installer.exe":                                           "",
		"installer.exe":                                                   "",
		"Fileion-VLC-xyz12345.exe":                                        "",
		// what a rename or a second download does to the name
		"Fileion-Installer-36ee3881.exe.exe":     "36ee3881",
		"Fileion-Installer-36ee3881 (1).exe":     "36ee3881",
		"Fileion-Installer-36ee3881.exe (2).exe": "36ee3881",
		"Fileion-Installer-36ee3881.exe.exe (1)": "36ee3881",
		"  Fileion-Installer-36ee3881 .EXE  ":    "36ee3881",
		"Fileion-Installer-36ee3881.EXE.EXE":     "36ee3881",
		"Fileion-Installer-36ee3881(3).exe":      "36ee3881",
		"Fileion-App-1.2-3fa91c0b.exe":           "3fa91c0b",
		"My special bundle.exe.exe":              "",
		"Fileion-Installer.exe.exe":              "",
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

// The shape the live endpoint sends (SetupCatalog::manifest on software.fileion.com): Windows entries carry page, winget_id and min_version.
func TestParseTheLiveWindowsAndLinuxShapes(t *testing.T) {
	win := `{"code":"3fa91c0b","os":"windows","apps":[{"id":7,"name":"7-Zip","page":"https://7-zip.fileion.com/windows","winget_id":"7zip.7zip","version":"24.08","min_version":"24.08","url":"https://www.7-zip.org/a/7z2408-x64.exe","sha256":"` + strings.Repeat("a1", 32) + `","installer_type":"nullsoft","architecture":"x64","silent_switch":"/S","product_code":null}],"source":"Fileion: x"}`
	m, err := Parse(strings.NewReader(win), "windows")
	if err != nil || len(m.Apps) != 1 || m.Apps[0].Page != "https://7-zip.fileion.com/windows" || m.Apps[0].ProductCode != "" || m.Apps[0].WingetID != "7zip.7zip" {
		t.Fatalf("windows: %+v, %v", m, err)
	}
	lin := `{"code":"3fa91c0b","os":"linux","apps":[{"id":9,"name":"VLC","flatpak_id":"org.videolan.VLC"}],"source":"x"}`
	m, err = Parse(strings.NewReader(lin), "linux")
	if err != nil || len(m.Apps) != 1 || m.Apps[0].FlatpakID != "org.videolan.VLC" {
		t.Fatalf("linux: %+v, %v", m, err)
	}
}

func TestParseReadsTheMatchRule(t *testing.T) {
	j := `{"apps":[{"id":1,"name":"Opera","url":"https://vendor.example/o.exe","sha256":"` + strings.Repeat("ab", 32) + `","match":{"name":"Opera Stable ","publisher":"Opera Software"}},
		{"id":2,"name":"Plain","url":"https://vendor.example/p.exe","sha256":"` + strings.Repeat("ab", 32) + `"}]}`
	m, err := Parse(strings.NewReader(j), "windows")
	if err != nil || len(m.Apps) != 2 {
		t.Fatalf("%+v %v", m, err)
	}
	if r := m.Apps[0].Match; r == nil || r.Name != "Opera Stable " || r.Publisher != "Opera Software" || m.Apps[1].Match != nil {
		t.Errorf("match rules: %+v / %+v", m.Apps[0].Match, m.Apps[1].Match)
	}
}

func TestFetchRetriesOnceForANetworkErrorA5xxAnd404ButNotForOtherAnswers(t *testing.T) {
	retryDelay = time.Millisecond
	t.Cleanup(func() { retryDelay = 3 * time.Second })
	ok := `{"code":"3fa91c0b","os":"windows","apps":[]}`
	for _, c := range []struct {
		name   string
		first  int
		wantOK bool
		calls  int32
	}{{"404 then the list", 404, true, 2}, {"503 then the list", 503, true, 2}, {"500 then the list", 500, true, 2}, {"403 is final", 403, false, 1}, {"400 is final", 400, false, 1}} {
		var n int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if atomic.AddInt32(&n, 1) == 1 {
				w.WriteHeader(c.first)
				return
			}
			w.Write([]byte(ok))
		}))
		_, err := Fetch(context.Background(), srv.Client(), srv.URL, "3fa91c0b", "windows")
		srv.Close()
		if (err == nil) != c.wantOK || n != c.calls {
			t.Errorf("%s: err %v after %d calls, want ok=%v in %d", c.name, err, n, c.wantOK, c.calls)
		}
	}
	// still down after the retry: the error is the second one, after exactly two tries
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&n, 1); w.WriteHeader(404) }))
	defer srv.Close()
	if _, err := Fetch(context.Background(), srv.Client(), srv.URL, "3fa91c0b", "windows"); err == nil || n != 2 {
		t.Errorf("404 twice: err %v after %d calls", err, n)
	}
	// no network at all: one retry, then the error
	n = 0
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := dead.URL
	dead.Close()
	if _, err := Fetch(context.Background(), http.DefaultClient, url, "3fa91c0b", "windows"); err == nil || !strings.Contains(err.Error(), "could not reach Fileion") {
		t.Errorf("no network: %v", err)
	}
	// a cancelled run does not wait for the retry
	retryDelay = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if _, err := Fetch(ctx, srv.Client(), srv.URL, "3fa91c0b", "windows"); err == nil || time.Since(start) > 5*time.Second {
		t.Errorf("cancel: %v after %v", err, time.Since(start))
	}
}
