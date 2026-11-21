package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
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

// --- v0.3.0 ---

func post(t *testing.T, url string) int {
	t.Helper()
	res, err := http.Post(url, "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func TestTheWindowStartsOnPreparingAndMovesToTheListOnlyToALocalAddress(t *testing.T) {
	s, url, _ := New(t.TempDir()+"/log", true)
	st := func() State {
		res, err := http.Get(url + "state")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var v State
		json.NewDecoder(res.Body).Decode(&v)
		return v
	}
	if !st().Preparing {
		t.Fatal("the first screen must be Preparing")
	}
	s.Redirect("https://evil.example/")
	if st().Redirect != "" {
		t.Fatal("a non-local redirect was accepted")
	}
	s.Redirect("http://127.0.0.1:5555/t/abc/")
	if st().Redirect != "http://127.0.0.1:5555/t/abc/" {
		t.Fatal("the hand-off address was refused")
	}
	s.SetApps([]string{"A"})
	if st().Preparing {
		t.Fatal("still Preparing once the list is there")
	}
}

func TestSkipRetryAndStopSteerAnInteractiveRow(t *testing.T) {
	s, url, _ := New(t.TempDir()+"/log", true)
	s.SetApps([]string{"VLC", "OBS"})

	skip := s.Arm(0)
	if post(t, url+"row/skip?i=0") != http.StatusNoContent {
		t.Fatal("skip refused")
	}
	select {
	case <-skip:
	default:
		t.Fatal("Skip did not close the row's channel")
	}

	s.Update(1, "notfinished", "Setup window closed before the app installed", "")
	go func() { time.Sleep(30 * time.Millisecond); post(t, url+"row/retry?i=1") }()
	if got := s.Ask(context.Background(), 1); got != "retry" {
		t.Fatalf("Ask = %q, want retry", got)
	}

	later := s.Arm(1)
	post(t, url+"stop")
	select {
	case <-later:
	default:
		t.Fatal("Stop did not end a waiting row")
	}
	if !s.Stopping() {
		t.Fatal("not stopping")
	}
	if got := s.Ask(context.Background(), 1); got != "skip" {
		t.Fatalf("Ask after Stop = %q", got)
	}
	if post(t, url+"row/skip?i=9") != http.StatusBadRequest || post(t, url+"row/fly?i=0") != http.StatusBadRequest {
		t.Fatal("a bad row or action was accepted")
	}
	if res, _ := http.Get(url + "row/skip?i=0"); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("GET on a row action got %d", res.StatusCode)
	}
}

func TestRetryOnlyWorksOnAFinishedRunAndRowButtonsGoToMain(t *testing.T) {
	s, url, _ := New(t.TempDir()+"/log", true)
	s.SetApps([]string{"Rufus"})
	post(t, url+"retry")
	select {
	case <-s.Retried():
		t.Fatal("Retry counted while the run was still going")
	default:
	}
	s.Finish(0, 1)
	post(t, url+"retry")
	select {
	case <-s.Retried():
	case <-time.After(time.Second):
		t.Fatal("Retry did not arrive")
	}
	got := make(chan string, 1)
	s.RowAction = func(i int, a string) { got <- fmt.Sprint(i, a) }
	post(t, url+"row/move?i=0")
	select {
	case v := <-got:
		if v != "0move" {
			t.Fatalf("got %q", v)
		}
	case <-time.After(time.Second):
		t.Fatal("the row action never ran")
	}
	opened := false
	s.OpenLogFolder = func() { opened = true }
	post(t, url+"open-log-folder")
	if !opened {
		t.Fatal("open-log-folder did nothing")
	}
}

func TestThePageHasTheV030Controls(t *testing.T) {
	s, url, _ := New(t.TempDir()+"/log", false)
	_ = s
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	page := string(b)
	for _, want := range []string{"Preparing…", "Stop after the current app?", "Keep going", "Retry failed apps", "Share Fileion Installer", "Report a problem", "installer-help#report",
		"Open log folder", "Untick any extra offers", "A few clicks", "Add to Start menu", "Move…", "Open folder", "Not finished", "Skipped by you", "screen.availHeight", "Verifying SHA-256…", "Not run"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Contains(strings.ToLower(page), "signpath") {
		t.Error("SignPath text in the page")
	}
}

func TestRedirectParsesTheAddressAndReopenForgetsAStop(t *testing.T) {
	s, url, _ := New(t.TempDir()+"/log", true)
	for _, bad := range []string{"http://127.0.0.1:80@evil.com/t/x/", "http://127.0.0.1.evil.com:80/t/x/", "http://127.0.0.1/t/x/", "http://127.0.0.1:abc/t/x/", "http://127.0.0.1:5000/x/", "https://127.0.0.1:5000/t/x/", "http://user@127.0.0.1:5000/t/x/", "http://localhost:5000/t/x/"} {
		s.Redirect(bad)
		s.mu.Lock()
		got := s.state.Redirect
		s.mu.Unlock()
		if got != "" {
			t.Fatalf("%q was accepted", bad)
		}
	}
	s.Redirect("http://127.0.0.1:5000/t/abc/")

	s.SetApps([]string{"A"})
	post(t, url+"stop")
	if !s.Stopping() {
		t.Fatal("Stop did not stop")
	}
	s.Finish(0, 1)
	s.Reopen()
	if s.Stopping() {
		t.Fatal("a Stop before the retry still stops the retry")
	}
	s.Update(0, "notfinished", "", "")
	go func() { time.Sleep(30 * time.Millisecond); post(t, url+"row/retry?i=0") }()
	if got := s.Ask(context.Background(), 0); got != "retry" {
		t.Fatalf("after Reopen Ask = %q, want the person's retry", got)
	}
	if c := s.Arm(0); func() bool {
		select {
		case <-c:
			return true
		default:
			return false
		}
	}() {
		t.Fatal("a row armed after the retry is already stopped")
	}
	if s.Polled() {
		t.Fatal("polled before anyone asked")
	}
	http.Get(url + "state")
	if !s.Polled() {
		t.Fatal("not polled after a request for the state")
	}
}
