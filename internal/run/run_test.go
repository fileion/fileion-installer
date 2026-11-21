package run

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fileion-installer/internal/manifest"
	"fileion-installer/internal/match"
	"fileion-installer/internal/portable"
)

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

type spy struct {
	execs [][]string
	hits  int
	hang  bool
}

func newRunner(t *testing.T, srv *httptest.Server, installed func(manifest.App) (string, bool), s *spy) *Runner {
	t.Helper()
	return &Runner{
		Client:    srv.Client(),
		TempDir:   t.TempDir(),
		Installed: installed,
		Exec: func(ctx context.Context, name string, args []string) error {
			s.execs = append(s.execs, append([]string{name}, args...))
			if s.hang {
				<-ctx.Done() // an installer that shows its own window and waits for a click
				return ctx.Err()
			}
			return nil
		},
	}
}

func server(t *testing.T, body []byte, s *spy) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.hits++; w.Write(body) }))
	t.Cleanup(srv.Close)
	return srv
}

func app(srv *httptest.Server, hash string) manifest.App {
	return manifest.App{ID: 1, Name: "Test", Version: "2.0.0", MinVersion: "2.0.0", URL: srv.URL + "/test.exe", SHA256: hash, SilentSwitch: "/S", InstallerType: "nullsoft"}
}

func notInstalled(manifest.App) (string, bool) { return "", false }

func TestAHashMismatchIsSkippedNeverRunAndTheFileIsRemoved(t *testing.T) {
	s := &spy{}
	srv := server(t, []byte("not what the vendor published"), s)
	r := newRunner(t, srv, notInstalled, s)

	res := r.RunApp(context.Background(), app(srv, sum([]byte("the real installer"))))

	if res.Status != Mismatch || !res.Bad() {
		t.Fatalf("status = %s, want mismatch", res.Status)
	}
	if len(s.execs) != 0 {
		t.Fatalf("the file was run: %v", s.execs)
	}
	if left, _ := filepath.Glob(filepath.Join(r.TempDir, "*")); len(left) != 0 {
		t.Fatalf("the downloaded file was kept: %v", left)
	}
	if !strings.Contains(res.Message, "SHA-256") {
		t.Errorf("the message does not say why: %q", res.Message)
	}
}

func TestAnAppAlreadyAtTheVersionIsSkippedWithoutDownloading(t *testing.T) {
	s := &spy{}
	srv := server(t, []byte("x"), s)
	r := newRunner(t, srv, func(manifest.App) (string, bool) { return "2.1.0", true }, s)

	res := r.RunApp(context.Background(), app(srv, sum([]byte("x"))))

	if res.Status != UpToDate || !res.OK() {
		t.Fatalf("status = %s, want up-to-date", res.Status)
	}
	if s.hits != 0 || len(s.execs) != 0 {
		t.Fatalf("downloaded %d times, ran %v", s.hits, s.execs)
	}
}

func TestAnOlderInstalledVersionIsUpdated(t *testing.T) {
	body := []byte("new installer")
	s := &spy{}
	srv := server(t, body, s)
	r := newRunner(t, srv, func(manifest.App) (string, bool) { return "1.9.9", true }, s)

	res := r.RunApp(context.Background(), app(srv, sum(body)))

	if res.Status != Done || len(s.execs) != 1 {
		t.Fatalf("status = %s, execs = %v", res.Status, s.execs)
	}
	if got := s.execs[0][1:]; len(got) != 1 || got[0] != "/S" {
		t.Errorf("silent switch not passed: %v", s.execs[0])
	}
	if _, err := os.Stat(s.execs[0][0]); err == nil {
		t.Errorf("the downloaded installer was not removed afterwards")
	}
}

func TestMsiGoesThroughMsiexec(t *testing.T) {
	body := []byte("msi")
	s := &spy{}
	srv := server(t, body, s)
	r := newRunner(t, srv, notInstalled, s)
	a := app(srv, sum(body))
	a.InstallerType, a.SilentSwitch = "msi", "/qn /norestart"

	if res := r.RunApp(context.Background(), a); res.Status != Done {
		t.Fatalf("status = %s", res.Status)
	}
	if s.execs[0][0] != "msiexec" || s.execs[0][1] != "/i" || s.execs[0][3] != "/qn" {
		t.Errorf("command = %v", s.execs[0])
	}
}

func TestPlainHttpAndUnsupportedKindsAreNeverRun(t *testing.T) {
	s := &spy{}
	srv := server(t, []byte("x"), s)
	r := newRunner(t, srv, notInstalled, s)

	a := app(srv, sum([]byte("x")))
	a.URL = strings.Replace(a.URL, "https://", "http://", 1)
	if res := r.RunApp(context.Background(), a); res.Status != Failed {
		t.Errorf("http: status = %s", res.Status)
	}
	b := app(srv, sum([]byte("x")))
	b.InstallerType = "msix"
	if res := r.RunApp(context.Background(), b); res.Status != Unsupported {
		t.Errorf("msix: status = %s", res.Status)
	}
	if len(s.execs) != 0 || s.hits != 0 {
		t.Errorf("ran %v, downloaded %d", s.execs, s.hits)
	}
}

func TestFlatpakInstallsThenUpdates(t *testing.T) {
	s := &spy{}
	srv := server(t, nil, s)
	a := manifest.App{ID: 2, Name: "Firefox", FlatpakID: "org.mozilla.firefox"}

	r := newRunner(t, srv, notInstalled, s)
	r.RunApp(context.Background(), a)
	r2 := newRunner(t, srv, func(manifest.App) (string, bool) { return "", true }, s)
	r2.RunApp(context.Background(), a)

	if s.execs[0][1] != "install" || s.execs[1][1] != "update" || s.execs[0][len(s.execs[0])-1] != "org.mozilla.firefox" {
		t.Errorf("commands = %v", s.execs)
	}
}

func TestAtLeast(t *testing.T) {
	cases := []struct {
		have, min string
		want      bool
	}{
		{"3.0.20", "3.0.20", true}, {"3.0.21", "3.0.20", true}, {"3.0.19", "3.0.20", false},
		{"10.0", "9.9", true}, {"1.2", "1.2.0", true}, {"1.2", "1.2.1", false}, {"v2.0", "2.0.0", true},
		{"5", "", true}, {"2.0.0-beta", "2.0.1", false},
	}
	for _, c := range cases {
		if got := AtLeast(c.have, c.min); got != c.want {
			t.Errorf("AtLeast(%q, %q) = %v, want %v", c.have, c.min, got, c.want)
		}
	}
}

func TestAnInstallerThatHangsIsStoppedAndMarkedManualWithItsFileionPage(t *testing.T) {
	body := []byte("setup with its own window")
	s := &spy{hang: true}
	srv := server(t, body, s)
	r := newRunner(t, srv, notInstalled, s)
	r.AppTimeout = 50 * time.Millisecond
	a := app(srv, sum(body))
	a.Page = "https://example.fileion.com/windows"

	start := time.Now()
	res := r.RunApp(context.Background(), a)

	if res.Status != Manual || res.OK() || res.Bad() {
		t.Fatalf("status = %s, want manual (neither worked nor failed)", res.Status)
	}
	if res.Link != a.Page || !strings.Contains(res.Message, "needs manual install") {
		t.Errorf("link %q, message %q", res.Link, res.Message)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("took %v: the limit did not stop it", time.Since(start))
	}
	if left, _ := filepath.Glob(filepath.Join(r.TempDir, "*")); len(left) != 0 {
		t.Errorf("the installer file was kept: %v", left)
	}
}

func TestTheTimeoutWaitsForTheKillToFinishBeforeItReturns(t *testing.T) {
	body := []byte("setup with its own window")
	s := &spy{hang: true}
	srv := server(t, body, s)
	r := newRunner(t, srv, notInstalled, s)
	r.AppTimeout = 50 * time.Millisecond
	var killed atomic.Bool
	r.Exec = func(ctx context.Context, name string, args []string) error {
		<-ctx.Done()
		time.Sleep(300 * time.Millisecond) // taskkill /T /F takes a moment
		killed.Store(true)
		return ctx.Err()
	}
	if res := r.RunApp(context.Background(), app(srv, sum(body))); res.Status != Manual {
		t.Fatalf("status = %s, want manual", res.Status)
	}
	if !killed.Load() {
		t.Fatal("the result came back before the installer's kill finished (the program could exit and leave it running)")
	}
}

func TestTheHashCheckRunsBeforeAnythingIsStartedEvenWithATimeoutSet(t *testing.T) {
	s := &spy{hang: true}
	srv := server(t, []byte("wrong"), s)
	r := newRunner(t, srv, notInstalled, s)
	r.AppTimeout = 50 * time.Millisecond

	if res := r.RunApp(context.Background(), app(srv, sum([]byte("right")))); res.Status != Mismatch || len(s.execs) != 0 {
		t.Fatalf("status %s, execs %v", res.Status, s.execs)
	}
}

func TestSourceForgeDownloadLinksGoToTheDirectMirrorForm(t *testing.T) {
	for in, want := range map[string]string{
		"https://sourceforge.net/projects/wesnoth/files/wesnoth-1.18/wesnoth-1.18.4/wesnoth-1.18.4-win64.exe/download":                      "https://downloads.sourceforge.net/project/wesnoth/wesnoth-1.18/wesnoth-1.18.4/wesnoth-1.18.4-win64.exe",
		"https://sourceforge.net/projects/codeblocks/files/Binaries/25.03/Windows/32bit/codeblocks-25.03-32bit-setup-nonadmin.exe/download": "https://downloads.sourceforge.net/project/codeblocks/Binaries/25.03/Windows/32bit/codeblocks-25.03-32bit-setup-nonadmin.exe",
		"https://downloads.sourceforge.net/project/x/y.exe":                                                                                 "https://downloads.sourceforge.net/project/x/y.exe",
		"https://example.com/projects/p/files/a.exe/download":                                                                               "https://example.com/projects/p/files/a.exe/download",
		"https://sourceforge.net/projects/p/":                                                                                               "https://sourceforge.net/projects/p/",
	} {
		if got := DirectURL(in); got != want {
			t.Errorf("DirectURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAMismatchLogsWhatWasReceivedAndKeepsNoFile(t *testing.T) {
	s := &spy{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html>interstitial</html>"))
	}))
	t.Cleanup(srv.Close)
	r := newRunner(t, srv, notInstalled, s)
	var logged []string
	r.Log = func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }

	res := r.RunApp(context.Background(), app(srv, sum([]byte("the real installer"))))

	if res.Status != Mismatch || len(logged) != 1 {
		t.Fatalf("status %s, log %v", res.Status, logged)
	}
	for _, want := range []string{"25 bytes", "text/html", srv.URL + "/test.exe"} {
		if !strings.Contains(logged[0], want) {
			t.Errorf("log %q lacks %q", logged[0], want)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(r.TempDir, "*")); len(left) != 0 {
		t.Fatalf("a file was kept: %v", left)
	}
}

func TestARedirectToPlainHTTPIsRefused(t *testing.T) {
	s := &spy{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://mirror.example/x.exe", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	r := newRunner(t, srv, notInstalled, s)

	res := r.RunApp(context.Background(), app(srv, sum([]byte("x"))))

	if res.Status != Failed || len(s.execs) != 0 {
		t.Fatalf("status %s, execs %v", res.Status, s.execs)
	}
}

func TestADownloadSaysItIsTheInstallerNotABrowser(t *testing.T) {
	s := &spy{}
	var ua string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { ua = r.UserAgent(); w.Write([]byte("x")) }))
	t.Cleanup(srv.Close)
	r := newRunner(t, srv, notInstalled, s)

	r.RunApp(context.Background(), app(srv, sum([]byte("x"))))

	if ua != UserAgent {
		t.Fatalf("User-Agent = %q", ua)
	}
}

// A name rule instead of a product code: old -> the run updates it, the next run skips it; Opera next to Opera GX stay apart.
func TestAnAppFoundByItsNameRuleIsUpdatedThenSkipped(t *testing.T) {
	s := &spy{}
	body := []byte("the new installer")
	srv := server(t, body, s)
	installed := []match.Entry{{Name: "Opera GX Stable 117.0.1", Publisher: "Opera Software", Version: "117.0.1"}, {Name: "Opera Stable 117.0.1", Publisher: "Opera Software", Version: "117.0.1"}}
	find := func(a manifest.App) (string, bool) {
		if a.Match == nil {
			return "", false
		}
		return match.Find(installed, *a.Match)
	}
	r := newRunner(t, srv, find, s)
	r.Exec = func(ctx context.Context, name string, args []string) error { // running the installer updates the entry
		s.execs = append(s.execs, append([]string{name}, args...))
		if len(installed) > 1 {
			installed[1].Version, installed[1].Name = "118.0.2", "Opera Stable 118.0.2"
		}
		return nil
	}
	opera := app(srv, sum(body))
	opera.Version, opera.MinVersion = "118.0.2", "118.0.2"
	opera.Match = &manifest.Match{Name: "Opera Stable ", Publisher: "Opera Software"}

	if res := r.RunApp(context.Background(), opera); res.Status != Done || len(s.execs) != 1 {
		t.Fatalf("old install: %s (%d runs)", res.Status, len(s.execs))
	}
	if res := r.RunApp(context.Background(), opera); res.Status != UpToDate || len(s.execs) != 1 || !strings.Contains(res.Message, "118.0.2") {
		t.Fatalf("second run: %s %q (%d runs)", res.Status, res.Message, len(s.execs))
	}
	// Opera GX, still at 117.0.1, is only ever compared with its own row
	gx := opera
	gx.Version, gx.MinVersion = "118.0.2", "118.0.2"
	gx.Match = &manifest.Match{Name: "Opera GX Stable ", Publisher: "Opera Software"}
	if res := r.RunApp(context.Background(), gx); res.Status != Done || len(s.execs) != 2 {
		t.Fatalf("Opera GX is older and must be updated: %s", res.Status)
	}
	// not installed at all: installed
	installed = nil
	if res := r.RunApp(context.Background(), opera); res.Status != Done || len(s.execs) != 3 {
		t.Fatalf("not installed: %s", res.Status)
	}
	// a version that cannot be read is updated, never skipped
	installed = []match.Entry{{Name: "Opera Stable ?", Publisher: "Opera Software", Version: "unknown"}}
	if res := r.RunApp(context.Background(), opera); res.Status != Done {
		t.Fatalf("unparsable version: %s", res.Status)
	}
}

func TestAPanicInOneAppIsLoggedAndTheNextAppStillRuns(t *testing.T) {
	s := &spy{}
	body := []byte("installer")
	srv := server(t, body, s)
	var logged string
	boom := func(a manifest.App) (string, bool) {
		if a.Name == "Boom" {
			panic("registry went wrong")
		}
		return "", false
	}
	r := newRunner(t, srv, boom, s)
	r.Log = func(f string, args ...any) { logged += fmt.Sprintf(f, args...) }
	var told []Status
	r.Progress = func(a manifest.App, st Status, msg, link string) { told = append(told, st) }

	bad := app(srv, sum(body))
	bad.Name = "Boom"
	res := r.RunApp(context.Background(), bad)
	if res.Status != Failed || !res.Bad() || !strings.Contains(res.Message, "internal error") {
		t.Fatalf("panic result: %+v", res)
	}
	if !strings.Contains(logged, "PANIC registry went wrong") || !strings.Contains(logged, "goroutine") {
		t.Errorf("the panic and its stack are in the log: %q", logged)
	}
	if next := r.RunApp(context.Background(), app(srv, sum(body))); next.Status != Done {
		t.Errorf("the next app still runs: %s", next.Status)
	}
	if len(told) == 0 || told[0] != Failed {
		t.Errorf("the window is told: %v", told)
	}
}

func TestADownloadReportsItsBytesAndTheTotal(t *testing.T) {
	s := &spy{}
	body := make([]byte, 300_000)
	srv := server(t, body, s)
	r := newRunner(t, srv, notInstalled, s)
	var lastDone, lastTotal int64
	calls := 0
	r.Transfer = func(a manifest.App, done, total int64) { calls++; lastDone, lastTotal = done, total }

	if res := r.RunApp(context.Background(), app(srv, sum(body))); res.Status != Done {
		t.Fatalf("status %s", res.Status)
	}
	if calls == 0 || lastDone != int64(len(body)) {
		t.Errorf("transfer: %d calls, last %d of %d", calls, lastDone, lastTotal)
	}
	if lastTotal < 0 {
		t.Errorf("an unknown total is 0, not %d", lastTotal)
	}
}

// An installer that never returns although the app is installed (a hidden prompt, a child left alive): the app is detected in the registry
// while it runs, counted as installed, and the installer is left running, not killed.
func TestAnInstallerThatNeverReturnsIsDetectedWhileItRunsAndLeftAlone(t *testing.T) {
	s := &spy{}
	body := []byte("setup that never ends")
	srv := server(t, body, s)
	var polls int32
	installed := func(manifest.App) (string, bool) {
		if atomic.AddInt32(&polls, 1) < 3 {
			return "", false // not there yet
		}
		return "2.0.0", true // registered after a few polls
	}
	r := newRunner(t, srv, installed, s)
	r.PollEvery, r.Grace = 10*time.Millisecond, 50*time.Millisecond
	var killed int32
	r.Exec = func(ctx context.Context, name string, args []string) error {
		<-ctx.Done() // never returns on its own
		atomic.StoreInt32(&killed, 1)
		return ctx.Err()
	}
	var logged string
	var mu sync.Mutex
	r.Log = func(f string, a ...any) { mu.Lock(); logged += fmt.Sprintf(f, a...) + "\n"; mu.Unlock() }

	start := time.Now()
	res := r.RunApp(context.Background(), app(srv, sum(body)))
	if res.Status != Done || !res.OK() || !strings.Contains(res.Message, "installer is still open") {
		t.Fatalf("result %+v", res)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("took %v: it waited for the time limit", time.Since(start))
	}
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt32(&killed) != 0 {
		t.Error("the vendor installer was ended in the middle of its clean-up; it must be left running")
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(logged, "detected as installed at 2.0.0") || !strings.Contains(logged, "left running") {
		t.Errorf("the log says what happened: %q", logged)
	}
}

func TestAnInstallerThatEndsDuringTheGraceIsSimplyDoneWithItsExitCodeLogged(t *testing.T) {
	s := &spy{}
	body := []byte("setup")
	srv := server(t, body, s)
	r := newRunner(t, srv, func(manifest.App) (string, bool) { return "2.0.0", true }, s)
	r.PollEvery, r.Grace = 5*time.Millisecond, time.Second
	r.Exec = func(ctx context.Context, name string, args []string) error {
		time.Sleep(60 * time.Millisecond)
		return nil
	}
	var logged string
	r.Log = func(f string, a ...any) { logged += fmt.Sprintf(f, a...) + "\n" }
	// the "already installed" check at the start would skip it, so the first look says no and the polls say yes
	var first int32
	r.Installed = func(manifest.App) (string, bool) {
		if atomic.AddInt32(&first, 1) == 1 {
			return "", false
		}
		return "2.0.0", true
	}
	res := r.RunApp(context.Background(), app(srv, sum(body)))
	if res.Status != Done || strings.Contains(res.Message, "still open") {
		t.Fatalf("result %+v", res)
	}
	if !strings.Contains(logged, "exited after") || !strings.Contains(logged, "code 0") {
		t.Errorf("exit code and time are logged: %q", logged)
	}
}

func TestAnInstallerNotDetectedAfterAMinuteSaysItMayBeWaitingAndIsStoppedAtTheLimit(t *testing.T) {
	s := &spy{hang: true}
	body := []byte("setup with a hidden prompt")
	srv := server(t, body, s)
	r := newRunner(t, srv, notInstalled, s)
	r.PollEvery, r.WaitHint, r.AppTimeout = 5*time.Millisecond, 30*time.Millisecond, 150*time.Millisecond
	var said []string
	var mu sync.Mutex
	r.Progress = func(a manifest.App, st Status, msg, link string) { mu.Lock(); said = append(said, msg); mu.Unlock() }
	res := r.RunApp(context.Background(), app(srv, sum(body)))
	if res.Status != Manual {
		t.Fatalf("status %s", res.Status)
	}
	mu.Lock()
	defer mu.Unlock()
	waiting := false
	for _, m := range said {
		waiting = waiting || strings.Contains(m, "may be waiting for you")
	}
	if !waiting {
		t.Errorf("the window was never told the installer may be waiting: %v", said)
	}
}

func TestAnInstallerThatExitsNonZeroButLeftTheAppInstalledIsDone(t *testing.T) {
	s := &spy{}
	body := []byte("setup that wants a restart")
	srv := server(t, body, s)
	var n int32
	r := newRunner(t, srv, func(manifest.App) (string, bool) {
		if atomic.AddInt32(&n, 1) == 1 {
			return "", false
		}
		return "2.0.0", true
	}, s)
	r.Exec = func(ctx context.Context, name string, args []string) error { return fmt.Errorf("exit status 3010") }
	if res := r.RunApp(context.Background(), app(srv, sum(body))); res.Status != Done {
		t.Fatalf("a non-zero exit with the app installed: %+v", res)
	}
}

// --- v0.3.0: portable, zip and interactive apps ---

func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b strings.Builder
	w := zip.NewWriter(&b)
	for n, c := range files {
		e, _ := w.Create(n)
		e.Write([]byte(c))
	}
	w.Close()
	return []byte(b.String())
}

func portableRunner(t *testing.T, srv *httptest.Server, s *spy) *Runner {
	t.Helper()
	r := newRunner(t, srv, notInstalled, s)
	r.AppsDir = filepath.Join(t.TempDir(), "Downloads", "Fileion Apps")
	r.Portable = &portable.Store{Path: filepath.Join(t.TempDir(), "portable.json")}
	return r
}

func TestAPortableAppIsSavedInItsFolderNotRunAndListedThenSkippedOnTheNextRun(t *testing.T) {
	s := &spy{}
	body := []byte("MZ portable program")
	srv := server(t, body, s)
	r := portableRunner(t, srv, s)
	a := app(srv, sum(body))
	a.Name, a.WingetID, a.Mode, a.InstallerType, a.SilentSwitch = "Rufus", "Pete.Rufus", "portable", "portable", ""
	var placed string
	r.Placed = func(_ manifest.App, folder, exe string) { placed = exe }

	res := r.RunApp(context.Background(), a)

	if res.Status != Saved || !res.OK() {
		t.Fatalf("status = %s (%s)", res.Status, res.Message)
	}
	if want := filepath.Join(r.AppsDir, "Rufus", "test.exe"); placed != want {
		t.Fatalf("placed %q, want %q", placed, want)
	}
	if b, _ := os.ReadFile(placed); string(b) != string(body) {
		t.Fatal("the file is not in its folder")
	}
	if len(s.execs) != 0 {
		t.Fatalf("a portable app was started: %v", s.execs)
	}
	if !strings.HasPrefix(res.Message, "Saved in Downloads"+string(filepath.Separator)+"Fileion Apps"+string(filepath.Separator)+"Rufus") {
		t.Errorf("message: %q", res.Message)
	}
	hits := s.hits
	if again := r.RunApp(context.Background(), a); again.Status != UpToDate || s.hits != hits {
		t.Fatalf("second run: %s, downloads %d -> %d", again.Status, hits, s.hits)
	}
}

func TestAPortableHashMismatchIsNeverSaved(t *testing.T) {
	s := &spy{}
	srv := server(t, []byte("tampered"), s)
	r := portableRunner(t, srv, s)
	a := app(srv, sum([]byte("the real one")))
	a.Mode, a.WingetID, a.SilentSwitch = "portable", "X.Y", ""

	if res := r.RunApp(context.Background(), a); res.Status != Mismatch {
		t.Fatalf("status = %s", res.Status)
	}
	if left, _ := filepath.Glob(filepath.Join(r.AppsDir, "*", "*")); len(left) != 0 {
		t.Fatalf("saved anyway: %v", left)
	}
}

func TestAZipIsVerifiedUnpackedAndDeleted(t *testing.T) {
	s := &spy{}
	z := zipBytes(t, map[string]string{"FSViewer/fsv.exe": "x", "FSViewer/readme.txt": "y"})
	srv := server(t, z, s)
	r := portableRunner(t, srv, s)
	a := app(srv, sum(z))
	a.Name, a.WingetID, a.Mode, a.InstallerType, a.SilentSwitch, a.ZipExe = "FastStone", "FS.Viewer", "zip", "zip", "", `FSViewer\fsv.exe`
	var exe string
	r.Placed = func(_ manifest.App, _, e string) { exe = e }
	var seen []Status
	r.Progress = func(_ manifest.App, st Status, _ string, _ string) { seen = append(seen, st) }

	res := r.RunApp(context.Background(), a)

	if res.Status != Saved {
		t.Fatalf("status = %s (%s)", res.Status, res.Message)
	}
	if want := filepath.Join(r.AppsDir, "FastStone", "FSViewer", "fsv.exe"); exe != want {
		t.Fatalf("exe %q, want %q", exe, want)
	}
	if left, _ := filepath.Glob(filepath.Join(r.TempDir, "*")); len(left) != 0 {
		t.Fatalf("the zip was kept: %v", left)
	}
	unpacked := false
	for _, st := range seen {
		unpacked = unpacked || st == Unpacking
	}
	if !unpacked {
		t.Errorf("never said Unpacking: %v", seen)
	}
}

func TestAZipWithAnEntryOutsideItsFolderFailsAndWritesNothingOutside(t *testing.T) {
	s := &spy{}
	z := zipBytes(t, map[string]string{"../../escape.txt": "x"})
	srv := server(t, z, s)
	r := portableRunner(t, srv, s)
	a := app(srv, sum(z))
	a.Mode, a.WingetID, a.SilentSwitch = "zip", "Z.Z", ""

	if res := r.RunApp(context.Background(), a); res.Status != Failed {
		t.Fatalf("status = %s", res.Status)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(r.AppsDir), "escape.txt")); err == nil {
		t.Fatal("wrote outside the folder")
	}
}

func interactiveApp(srv *httptest.Server, hash string) manifest.App {
	a := app(srv, hash)
	a.Mode, a.SilentSwitch, a.InstallerType = "interactive", "", "exe"
	return a
}

func TestAnInteractiveAppIsStartedWithoutASwitchAndIsDoneWhenTheRegistryHasIt(t *testing.T) {
	s := &spy{}
	body := []byte("vendor setup")
	srv := server(t, body, s)
	installed := false
	r := newRunner(t, srv, func(manifest.App) (string, bool) { return "2.0.0", installed }, s)
	r.Exec = func(ctx context.Context, name string, args []string) error {
		s.execs = append(s.execs, append([]string{name}, args...))
		installed = true // the person finishes the setup
		return nil
	}
	fronted := 0
	r.Front = func() { fronted++ }
	var seen []Status
	r.Progress = func(_ manifest.App, st Status, _ string, _ string) { seen = append(seen, st) }

	res := r.RunApp(context.Background(), interactiveApp(srv, sum(body)))

	if res.Status != Done {
		t.Fatalf("status = %s (%s)", res.Status, res.Message)
	}
	if len(s.execs) != 1 || len(s.execs[0]) != 1 {
		t.Fatalf("started with a switch or not once: %v", s.execs)
	}
	if fronted != 1 {
		t.Errorf("the window was brought to the front %d times", fronted)
	}
	has := map[Status]bool{}
	for _, st := range seen {
		has[st] = true
	}
	if !has[YourTurn] {
		t.Errorf("never said it is the person's turn: %v", seen)
	}
}

func TestAnInteractiveSetupClosedWithoutTheAppIsNotFinishedThenRetryRunsItAgainAndSkipMovesOn(t *testing.T) {
	s := &spy{}
	body := []byte("vendor setup")
	srv := server(t, body, s)
	r := newRunner(t, srv, notInstalled, s)
	answers := []string{"retry", "skip"}
	asked := 0
	r.Ask = func(ctx context.Context, a manifest.App) string {
		asked++
		a2 := answers[0]
		answers = answers[1:]
		return a2
	}
	var seen []Status
	r.Progress = func(_ manifest.App, st Status, _ string, _ string) { seen = append(seen, st) }

	res := r.RunApp(context.Background(), interactiveApp(srv, sum(body)))

	if res.Status != Skipped || res.Bad() || res.OK() {
		t.Fatalf("status = %s", res.Status)
	}
	if len(s.execs) != 2 || asked != 2 {
		t.Fatalf("runs %d, asked %d, want 2 and 2", len(s.execs), asked)
	}
	notFinished := 0
	for _, st := range seen {
		if st == NotFinished {
			notFinished++
		}
	}
	if notFinished != 2 {
		t.Errorf("NotFinished said %d times: %v", notFinished, seen)
	}
}

func TestSkipEndsAnInteractiveSetupThatNeverReturnsAndTheHashIsStillCheckedFirst(t *testing.T) {
	s := &spy{hang: true}
	body := []byte("vendor setup")
	srv := server(t, body, s)
	r := newRunner(t, srv, notInstalled, s)
	skip := make(chan struct{})
	r.Arm = func(manifest.App) <-chan struct{} { return skip }
	r.Progress = func(_ manifest.App, st Status, _ string, _ string) {
		if st == YourTurn {
			go func() { time.Sleep(50 * time.Millisecond); close(skip) }()
		}
	}
	if res := r.RunApp(context.Background(), interactiveApp(srv, sum(body))); res.Status != Skipped {
		t.Fatalf("status = %s (%s)", res.Status, res.Message)
	}

	s2 := &spy{}
	srv2 := server(t, []byte("tampered"), s2)
	r2 := newRunner(t, srv2, notInstalled, s2)
	if res := r2.RunApp(context.Background(), interactiveApp(srv2, sum(body))); res.Status != Mismatch || len(s2.execs) != 0 {
		t.Fatalf("a wrong hash must stop before the setup opens: %s %v", res.Status, s2.execs)
	}
}

func TestAnUnknownModeIsNotRunAndOrderForRunPutsInteractiveLast(t *testing.T) {
	s := &spy{}
	srv := server(t, []byte("x"), s)
	r := newRunner(t, srv, notInstalled, s)
	a := app(srv, sum([]byte("x")))
	a.Mode = "teleport"
	if res := r.RunApp(context.Background(), a); res.Status != Unsupported || s.hits != 0 {
		t.Fatalf("status = %s, downloads %d", res.Status, s.hits)
	}
	got := manifest.OrderForRun([]manifest.App{{Name: "a", Mode: "interactive"}, {Name: "b"}, {Name: "c", Mode: "portable"}, {Name: "d", Mode: "interactive"}, {Name: "e", Mode: "silent"}})
	names := ""
	for _, x := range got {
		names += x.Name
	}
	if names != "bcead" {
		t.Fatalf("order = %s, want bcead", names)
	}
}

func TestAnInteractiveSetupThatLeavesTheAppRunningIsDoneAfterAGraceAndNothingIsKilled(t *testing.T) {
	s := &spy{}
	body := []byte("vendor setup")
	srv := server(t, body, s)
	var installed atomic.Bool
	r := newRunner(t, srv, func(manifest.App) (string, bool) { return "2.0.0", installed.Load() }, s)
	r.PollEvery, r.Grace = 10*time.Millisecond, 60*time.Millisecond
	killed := false
	r.ExecJob = func(ctx context.Context, name string, args []string, leave <-chan struct{}) error {
		installed.Store(true) // the setup installs, then launches the app and its window stays in the job
		select {
		case <-leave:
			return nil // released, not ended
		case <-ctx.Done():
			killed = true
			return ctx.Err()
		}
	}
	start := time.Now()
	res := r.RunApp(context.Background(), interactiveApp(srv, sum(body)))
	if res.Status != Done || !strings.Contains(res.Message, "still open") {
		t.Fatalf("status = %s (%s)", res.Status, res.Message)
	}
	if killed || time.Since(start) > 5*time.Second {
		t.Fatalf("killed %v after %v", killed, time.Since(start))
	}
}

func TestSkipOrStopAfterTheAppIsInstalledIsDoneAndStopOnAnOpenSetupSaysStopped(t *testing.T) {
	s := &spy{hang: true}
	body := []byte("vendor setup")
	srv := server(t, body, s)
	var installed atomic.Bool
	r := newRunner(t, srv, func(manifest.App) (string, bool) { return "2.0.0", installed.Load() }, s)
	r.PollEvery, r.Grace = time.Hour, time.Hour // the registry is not polled: only the check after Skip sees the app
	skip := make(chan struct{})
	r.Arm = func(manifest.App) <-chan struct{} { return skip }
	r.Progress = func(_ manifest.App, st Status, _ string, _ string) {
		if st == YourTurn {
			go func() { time.Sleep(30 * time.Millisecond); installed.Store(true); close(skip) }()
		}
	}
	if res := r.RunApp(context.Background(), interactiveApp(srv, sum(body))); res.Status != Done {
		t.Fatalf("Skip after the app was installed gave %s", res.Status)
	}

	s2 := &spy{hang: true}
	srv2 := server(t, body, s2)
	r2 := newRunner(t, srv2, notInstalled, s2)
	skip2 := make(chan struct{})
	r2.Arm = func(manifest.App) <-chan struct{} { return skip2 }
	r2.Stopped = func() bool { return true }
	r2.Progress = func(_ manifest.App, st Status, _ string, _ string) {
		if st == YourTurn {
			go func() { time.Sleep(30 * time.Millisecond); close(skip2) }()
		}
	}
	if res := r2.RunApp(context.Background(), interactiveApp(srv2, sum(body))); res.Status != Cancelled || res.Message != "Stopped" {
		t.Fatalf("Stop gave %s %q, want Cancelled Stopped", res.Status, res.Message)
	}
}

func TestAPortableFolderThatIsALinkIsNotWrittenThrough(t *testing.T) {
	s := &spy{}
	body := []byte("MZ")
	srv := server(t, body, s)
	r := portableRunner(t, srv, s)
	outside := t.TempDir()
	os.MkdirAll(r.AppsDir, 0o755)
	if err := os.Symlink(outside, filepath.Join(r.AppsDir, "Test")); err != nil {
		t.Skip("no symlinks here")
	}
	a := app(srv, sum(body))
	a.Mode, a.WingetID, a.SilentSwitch = "portable", "L.L", ""
	if res := r.RunApp(context.Background(), a); res.Status != Failed {
		t.Fatalf("status = %s", res.Status)
	}
	if left, _ := filepath.Glob(filepath.Join(outside, "*")); len(left) != 0 {
		t.Fatalf("wrote through the link: %v", left)
	}
}
