package run

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fileion-installer/internal/manifest"
)

func fast(t *testing.T) {
	old := BackoffFor
	BackoffFor = func(int) time.Duration { return time.Millisecond }
	t.Cleanup(func() { BackoffFor = old })
}

// A vendor that cuts the connection after half the file on the first try and honours Range on the next one.
func TestABrokenDownloadResumesWithRangeAndTheHashIsOverTheWholeFile(t *testing.T) {
	fast(t)
	body := []byte(strings.Repeat("0123456789", 1000))
	var ranges []string
	var n int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rg := r.Header.Get("Range")
		ranges = append(ranges, rg)
		if rg == "" {
			w.Header().Set("Content-Length", "10000")
			w.Write(body[:5000])
			if atomic.AddInt32(&n, 1) == 1 {
				panic(http.ErrAbortHandler) // reset
			}
			return
		}
		w.Header().Set("Content-Range", "bytes 5000-9999/10000")
		w.Header().Set("Content-Length", "5000")
		w.WriteHeader(206)
		w.Write(body[5000:])
	}))
	defer srv.Close()
	r := newRunner(t, srv, notInstalled, &spy{})
	var msgs []string
	r.Progress = func(_ manifest.App, s Status, m, _ string) { msgs = append(msgs, string(s)+":"+m) }
	res := r.RunApp(context.Background(), app(srv, sum(body)))
	if res.Status != Done {
		t.Fatalf("%+v", res)
	}
	if len(ranges) != 2 || ranges[1] != "bytes=5000-" {
		t.Fatalf("ranges %v", ranges)
	}
	if !strings.Contains(strings.Join(msgs, "|"), "Download interrupted. Retrying…") {
		t.Fatal(msgs)
	}
}

func TestAVendorWithoutRangeStartsOverAndAWrongFileIsStillAMismatch(t *testing.T) {
	fast(t)
	body := []byte(strings.Repeat("ab", 3000))
	var hits int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "6000")
		w.Write(body[:3000])
		if atomic.AddInt32(&hits, 1) == 1 {
			panic(http.ErrAbortHandler)
		}
		w.Write(body[3000:])
	}))
	defer srv.Close()
	r := newRunner(t, srv, notInstalled, &spy{})
	if res := r.RunApp(context.Background(), app(srv, sum(body))); res.Status != Done { // answered 200 to the Range: restarted, not glued
		t.Fatalf("%+v", res)
	}
	if res := r.RunApp(context.Background(), app(srv, strings.Repeat("0", 64))); res.Status != Mismatch {
		t.Fatalf("%+v", res)
	}
}

func TestAfterThreeRetriesTheRowSaysCouldntDownloadWithNoRawErrorAndNoFileIsLeft(t *testing.T) {
	fast(t)
	var hits int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Length", "100")
		w.Write([]byte("x"))
		panic(http.ErrAbortHandler)
	}))
	defer srv.Close()
	r := newRunner(t, srv, notInstalled, &spy{})
	var logged string
	r.Log = func(f string, a ...any) { logged += "\n" + strings.TrimSpace(f) }
	res := r.RunApp(context.Background(), app(srv, sum([]byte("x"))))
	if res.Status != Failed || res.Message != "Couldn't download from the vendor" {
		t.Fatalf("%+v", res)
	}
	if hits != 1+Retries {
		t.Fatalf("tries %d", hits)
	}
	if strings.Contains(res.Message, "tcp") || strings.Contains(res.Message, "wsarecv") {
		t.Fatal("raw error shown")
	}
	if left, _ := os.ReadDir(r.TempDir); len(left) != 0 {
		t.Fatalf("left in temp: %v", left)
	}
	if !strings.Contains(logged, "download failed: %v") {
		t.Fatal("the full error must be logged")
	}
}

func TestAMissingFileIsNotRetried(t *testing.T) {
	fast(t)
	var hits int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&hits, 1); http.NotFound(w, r) }))
	defer srv.Close()
	r := newRunner(t, srv, notInstalled, &spy{})
	if res := r.RunApp(context.Background(), app(srv, sum([]byte("x")))); res.Status != Failed || hits != 1 {
		t.Fatalf("%+v hits %d", res, hits)
	}
}

// The next app downloads while the current one installs; installs never overlap; Close leaves nothing in the temp folder.
func TestTheNextDownloadsRunWhileTheCurrentInstallsAndInstallsStayOneAtATime(t *testing.T) {
	bodies := map[string][]byte{"/a.exe": []byte("aaa"), "/b.exe": []byte("bbb"), "/c.exe": []byte("ccc")}
	var mu sync.Mutex
	var fetched []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fetched = append(fetched, r.URL.Path)
		mu.Unlock()
		w.Write(bodies[r.URL.Path])
	}))
	defer srv.Close()
	var running, maxRunning int32
	var othersFetchedDuringFirstInstall int
	r := newRunner(t, srv, notInstalled, &spy{})
	first := true
	r.Exec = func(ctx context.Context, name string, args []string) error {
		c := atomic.AddInt32(&running, 1)
		if c > atomic.LoadInt32(&maxRunning) {
			atomic.StoreInt32(&maxRunning, c)
		}
		if first {
			first = false
			for i := 0; i < 200; i++ { // wait up to 2 s for the others to be fetched while this install "runs"
				mu.Lock()
				n := len(fetched)
				mu.Unlock()
				if n >= 3 {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			mu.Lock()
			othersFetchedDuringFirstInstall = len(fetched) - 1
			mu.Unlock()
		}
		atomic.AddInt32(&running, -1)
		return nil
	}
	var apps []manifest.App
	for i, p := range []string{"/a.exe", "/b.exe", "/c.exe"} {
		x := app(srv, sum(bodies[p]))
		x.ID, x.Name, x.URL = i+1, string(rune('A'+i)), srv.URL+p
		apps = append(apps, x)
	}
	ahead := r.NewAhead(context.Background(), apps)
	for i, a := range apps {
		ahead.Fill(i, func(int) bool { return true })
		if res := r.RunAppAhead(context.Background(), a, ahead.Take(i)); res.Status != Done {
			t.Fatalf("%d %+v", i, res)
		}
	}
	ahead.Close()
	if othersFetchedDuringFirstInstall < 2 {
		t.Fatalf("only %d other downloads ran during the first install", othersFetchedDuringFirstInstall)
	}
	if maxRunning != 1 {
		t.Fatalf("installs overlapped: %d", maxRunning)
	}
	if left, _ := os.ReadDir(r.TempDir); len(left) != 0 {
		t.Fatalf("left in temp: %v", left)
	}
}

func TestStopEndsTheDownloadsAheadAndCloseDeletesTheFilesNobodyTook(t *testing.T) {
	hold := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow.exe" {
			w.Header().Set("Content-Length", "10")
			w.Write([]byte("a"))
			w.(http.Flusher).Flush()
			select {
			case <-hold:
			case <-r.Context().Done():
			}
			return
		}
		w.Write([]byte("done"))
	}))
	defer srv.Close()
	defer close(hold)
	stop := make(chan struct{})
	var stopped atomic.Bool
	r := newRunner(t, srv, notInstalled, &spy{})
	r.StopCh = func() <-chan struct{} { return stop }
	r.Stopped = stopped.Load
	slow, quick := app(srv, sum([]byte("a"))), app(srv, sum([]byte("done")))
	slow.ID, slow.URL, quick.ID, quick.Name, quick.URL = 1, srv.URL+"/slow.exe", 2, "Q", srv.URL+"/q.exe"
	ahead := r.NewAhead(context.Background(), []manifest.App{slow, quick})
	ahead.Fill(0, func(int) bool { return true })
	time.Sleep(200 * time.Millisecond) // quick is downloaded and waiting, slow is mid-download
	stopped.Store(true)
	close(stop)
	ahead.Close() // returns only when the slow download has really ended
	if left, _ := os.ReadDir(r.TempDir); len(left) != 0 {
		t.Fatalf("left in temp: %v", left)
	}
}
