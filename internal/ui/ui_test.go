package ui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestTheWindowAnswersOnlyToItsOwnHostAndPath(t *testing.T) {
	s, url, err := New(t.TempDir()+"/log", true)
	if err != nil {
		t.Fatal(err)
	}
	s.SetApps([]string{"VLC"})
	s.Update(0, "done", "Installed 3.0.21", "")
	s.Finish(1, 0)

	res, err := http.Get(url + "state")
	if err != nil {
		t.Fatal(err)
	}
	var st State
	json.NewDecoder(res.Body).Decode(&st)
	res.Body.Close()
	if len(st.Rows) != 1 || st.Rows[0].Status != "done" || !st.Finished || st.OK != 1 {
		t.Fatalf("state = %+v", st)
	}

	req, _ := http.NewRequest("GET", url+"state", nil)
	req.Host = "evil.example"
	if res, _ := http.DefaultClient.Do(req); res.StatusCode != http.StatusNotFound {
		t.Errorf("a foreign Host got %d", res.StatusCode)
	}
	other := strings.Replace(url, "/t/", "/x/", 1)
	if res, _ := http.Get(other + "state"); res.StatusCode != http.StatusNotFound {
		t.Errorf("a wrong path got %d", res.StatusCode)
	}

	if res, _ := http.Get(url + "close"); res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET close got %d", res.StatusCode)
	}
	http.Post(url+"close", "text/plain", nil)
	select {
	case <-s.Closed:
	default:
		t.Error("Close did not close")
	}
}

func TestSanitizeKeepsOneShortLine(t *testing.T) {
	if got := Sanitize("a\nb\x00c"); got != "a b c" {
		t.Errorf("got %q", got)
	}
	if got := Sanitize(strings.Repeat("x", 500)); len(got) != 200 {
		t.Errorf("len %d", len(got))
	}
}

func TestTheCountIsSentUnlessDontSendIsTicked(t *testing.T) {
	s, url, err := New(t.TempDir()+"/log", false)
	if err != nil {
		t.Fatal(err)
	}
	if !s.SendReport() {
		t.Fatal("the default must be to send")
	}
	if res, _ := http.Get(url + "report?send=0"); res.StatusCode != http.StatusMethodNotAllowed || !s.SendReport() {
		t.Errorf("GET changed the choice (%d)", res.StatusCode)
	}
	http.Post(url+"report?send=0", "text/plain", nil)
	if s.SendReport() {
		t.Error("ticking Don't send must stop the count")
	}
	http.Post(url+"report?send=1", "text/plain", nil)
	if !s.SendReport() {
		t.Error("unticking must send again")
	}
}

func TestTheNoCodeWindowShowsTheMessageAndTheSetupLink(t *testing.T) {
	s, url, err := New(t.TempDir()+"/log", false)
	if err != nil {
		t.Fatal(err)
	}
	s.NoCode("This file has no app list. Get your installer from software.fileion.com/setup")
	res, _ := http.Get(url + "state")
	var st State
	json.NewDecoder(res.Body).Decode(&st)
	res.Body.Close()
	if !st.NoCode || !st.Finished || !strings.Contains(st.Note, "no app list") || len(st.Rows) != 0 {
		t.Fatalf("state = %+v", st)
	}
	page, _ := http.Get(url)
	body := new(strings.Builder)
	buf := make([]byte, 1<<16)
	n, _ := page.Body.Read(buf)
	body.Write(buf[:n])
	page.Body.Close()
	if !strings.Contains(body.String(), `href="https://software.fileion.com/setup"`) {
		t.Error("the page links to software.fileion.com/setup")
	}
}

func get(t *testing.T, url string) (int, string, string) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header.Get("Content-Type"), string(b)
}

func TestAStatusChangeStartsItsClockAndTheDownloadFiguresShowInTheState(t *testing.T) {
	s, url, _ := New(t.TempDir()+"/fileion-installer.log", false)
	s.SetApps([]string{"VLC"})
	s.Update(0, "downloading", "", "")
	s.Transfer(0, 12_400_000, 48_000_000)
	var st State
	_, _, body := get(t, url+"state")
	json.Unmarshal([]byte(body), &st)
	r := st.Rows[0]
	if r.Status != "downloading" || r.Done != 12_400_000 || r.Total != 48_000_000 || r.Since == 0 || st.LogPath == "" {
		t.Fatalf("row %+v, log path %q", r, st.LogPath)
	}
	since := r.Since
	s.Update(0, "downloading", "still", "") // same status: the clock and figures stay
	s.Update(0, "installing", "", "")       // new status: a new clock, no stale download figures
	_, _, body = get(t, url+"state")
	st = State{}
	json.Unmarshal([]byte(body), &st)
	if st.Rows[0].Done != 0 || st.Rows[0].Total != 0 || st.Rows[0].Since < since {
		t.Fatalf("after the change: %+v", st.Rows[0])
	}
}

func TestTheLogRouteServesWhileRunningAndOpenLogOpensTheFileItself(t *testing.T) {
	path := t.TempDir() + "/fileion-installer.log"
	os.WriteFile(path, []byte("start: version dev\n"), 0o600)
	s, url, _ := New(path, false)
	if code, _, body := get(t, url+"log"); code != 200 || !strings.Contains(body, "start: version dev") {
		t.Fatalf("log: %d %q", code, body)
	}
	if code, _, _ := get(t, url+"open-log"); code != http.StatusMethodNotAllowed {
		t.Errorf("open-log is POST only: %d", code)
	}
	if res, _ := http.Post(url+"open-log", "", nil); res.StatusCode != http.StatusNotImplemented {
		t.Errorf("no opener: %d", res.StatusCode)
	}
	opened := 0
	s.OpenLog = func() { opened++ }
	if res, _ := http.Post(url+"open-log", "", nil); res.StatusCode != http.StatusNoContent || opened != 1 {
		t.Errorf("open-log: %d, opened %d", res.StatusCode, opened)
	}
}

func TestTheWindowIconsAreTheExeIconAndTheManifestNamesThem(t *testing.T) {
	_, url, _ := New(t.TempDir()+"/log", false)
	ico, err := os.ReadFile("../../winres/icon.ico")
	if err != nil {
		t.Fatal(err)
	}
	// each embedded PNG that also exists in the .ico must be the very same bytes: one drawing for the exe and the window
	for _, size := range []int{32, 64} {
		code, ctype, body := get(t, url+fmt.Sprintf("icon-%d.png", size))
		if code != 200 || ctype != "image/png" || !strings.HasPrefix(body, "\x89PNG") {
			t.Fatalf("icon-%d.png: %d %s", size, code, ctype)
		}
		if !strings.Contains(string(ico), body) {
			t.Errorf("icon-%d.png is not the .exe's %d px image", size, size)
		}
	}
	for _, name := range []string{"icon-192.png", "icon-512.png"} {
		if code, _, _ := get(t, url+name); code != 200 {
			t.Errorf("%s: %d", name, code)
		}
	}
	_, ctype, body := get(t, url+"manifest.webmanifest")
	var m struct {
		Name  string
		Icons []struct{ Src, Sizes string }
	}
	if json.Unmarshal([]byte(body), &m) != nil || m.Name != "Fileion Installer" || len(m.Icons) != 2 || !strings.Contains(ctype, "manifest+json") {
		t.Errorf("manifest: %s %q", ctype, body)
	}
	code, _, pageHTML := get(t, url)
	for _, want := range []string{`href="icon-32.png"`, `href="icon-64.png"`, `rel="manifest"`} {
		if code != 200 || !strings.Contains(pageHTML, want) {
			t.Errorf("the page lacks %s", want)
		}
	}
}

// The page's behaviour is plain JS served as is; these are the strings and rules the owner asked for, so a rewrite cannot drop them silently.
func TestThePageHasTheProgressAndStoppedStatesAndNoSignPath(t *testing.T) {
	for _, want := range []string{
		"The installer stopped", "logPath", "Copy path", // the server-gone state shows the real log path
		"Installing…", "of '+n", "No data for 2 minutes", "Still running", // elapsed time, overall count, the slow notes
		"prefers-reduced-motion", "role','progressbar", "open-log",
	} {
		if !strings.Contains(string(page), want) {
			t.Errorf("page.html lacks %q", want)
		}
	}
	for _, bad := range []string{"SignPath", "signpath", "virus-free"} {
		if strings.Contains(string(page), bad) {
			t.Errorf("page.html contains %q", bad)
		}
	}
}
