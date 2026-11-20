package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fileion-installer/internal/manifest"
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

func TestTheHashCheckRunsBeforeAnythingIsStartedEvenWithATimeoutSet(t *testing.T) {
	s := &spy{hang: true}
	srv := server(t, []byte("wrong"), s)
	r := newRunner(t, srv, notInstalled, s)
	r.AppTimeout = 50 * time.Millisecond

	if res := r.RunApp(context.Background(), app(srv, sum([]byte("right")))); res.Status != Mismatch || len(s.execs) != 0 {
		t.Fatalf("status %s, execs %v", res.Status, s.execs)
	}
}
