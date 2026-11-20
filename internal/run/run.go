// Package run installs the apps of a manifest one by one: download from the vendor URL only, check the vendor's SHA-256
// (a mismatch is never run), run the silent switch, and skip apps that are already at the manifest's version.
package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fileion-installer/internal/manifest"
	"fileion-installer/internal/version"
)

// Status is what the window shows for one app.
type Status string

const (
	Queued      Status = "queued"
	Downloading Status = "downloading"
	Verifying   Status = "verifying"
	Installing  Status = "installing"
	Done        Status = "done"        // installed or updated
	UpToDate    Status = "up-to-date"  // already at the manifest's version: nothing was downloaded
	Mismatch    Status = "mismatch"    // the file's SHA-256 is not the vendor's: not run
	Unsupported Status = "unsupported" // an installer kind this program cannot run silently
	Failed      Status = "failed"
	Manual      Status = "manual" // stopped at the time limit: it shows its own setup window or hung
	Cancelled   Status = "cancelled"
)

// Result is the end state of one app.
type Result struct {
	Status  Status
	Message string
	Link    string // the app's Fileion page, for the statuses that send the person there
}

// OK says whether the app counts as worked in the anonymous count (installed, updated or already current).
func (r Result) OK() bool { return r.Status == Done || r.Status == UpToDate }

// Bad says whether it counts as failed (an unsupported kind is neither).
func (r Result) Bad() bool { return r.Status == Failed || r.Status == Mismatch }

// Runner holds everything that touches the computer, so tests can replace it.
type Runner struct {
	Client  *http.Client
	TempDir string
	// Installed returns the installed version of an app (found=false when it is not installed or cannot be told).
	Installed func(manifest.App) (version string, found bool)
	// Exec runs a program and waits for it.
	Exec func(ctx context.Context, name string, args []string) error
	// AppTimeout stops an installer that runs longer than this (0 = DefaultTimeout).
	AppTimeout time.Duration
	// Log gets diagnostic lines for the log file (may be nil).
	Log func(format string, args ...any)
	// Progress is told about each step (may be nil).
	Progress func(app manifest.App, s Status, msg string, link string)
}

func (r *Runner) say(a manifest.App, s Status, msg string, link string) {
	if r.Progress != nil {
		r.Progress(a, s, msg, link)
	}
}

// RunApp installs (or updates) one Windows app from its manifest entry.
func (r *Runner) RunApp(ctx context.Context, a manifest.App) Result {
	res := r.runApp(ctx, a)
	if res.Status == Unsupported || res.Status == Manual {
		res.Link = a.Page
	}
	r.say(a, res.Status, res.Message, res.Link)
	return res
}

func (r *Runner) runApp(ctx context.Context, a manifest.App) Result {
	if ctx.Err() != nil {
		return Result{Status: Cancelled, Message: "Stopped before this one"}
	}
	if a.FlatpakID != "" {
		return r.runFlatpak(ctx, a)
	}
	if v, found := r.Installed(a); found && AtLeast(v, a.MinVersion) {
		return Result{Status: UpToDate, Message: "Already at " + v}
	}
	kind := strings.ToLower(a.InstallerType)
	if kind == "msix" || kind == "appx" || kind == "msstore" || kind == "zip" || kind == "portable" || strings.TrimSpace(a.SilentSwitch) == "" {
		return Result{Status: Unsupported, Message: "Needs its own installer: get it from its Fileion page"}
	}

	r.say(a, Downloading, "Downloading from the vendor", "")
	file, sum, got, err := r.download(ctx, a)
	if err != nil {
		if ctx.Err() != nil {
			return Result{Status: Cancelled, Message: "Stopped"}
		}
		return Result{Status: Failed, Message: "Download failed: " + err.Error()}
	}
	defer os.Remove(file)

	r.say(a, Verifying, "Checking the SHA-256", "")
	if sum != strings.ToLower(a.SHA256) {
		if r.Log != nil {
			r.Log("%s: SHA-256 mismatch: received %d bytes, Content-Type %q, final address %s", a.Name, got.Size, got.ContentType, got.FinalURL)
		}
		return Result{Status: Mismatch, Message: "Skipped: the file does not match the vendor's SHA-256, so it was not run"}
	}

	r.say(a, Installing, "Installing", "")
	name, args := command(a, file)
	limit := r.AppTimeout
	if limit <= 0 {
		limit = DefaultTimeout
	}
	runCtx, stop := context.WithTimeout(ctx, limit)
	defer stop()
	if err := r.Exec(runCtx, name, args); err != nil {
		if ctx.Err() != nil {
			return Result{Status: Cancelled, Message: "Stopped"}
		}
		if runCtx.Err() != nil {
			// Its own setup window, or a hang: stopped, never left running
			return Result{Status: Manual, Message: "Skipped: needs manual install"}
		}
		return Result{Status: Failed, Message: "The installer reported: " + err.Error()}
	}
	return Result{Status: Done, Message: "Installed " + strings.TrimSpace(a.Version)}
}

func (r *Runner) runFlatpak(ctx context.Context, a manifest.App) Result {
	_, installed := r.Installed(a)
	args := []string{"install", "-y", "--noninteractive", "flathub", a.FlatpakID}
	msg := "Installed"
	if installed {
		args = []string{"update", "-y", "--noninteractive", a.FlatpakID}
		msg = "Checked and updated if needed"
	}
	r.say(a, Installing, "Installing with Flatpak", "")
	if err := r.Exec(ctx, "flatpak", args); err != nil {
		if ctx.Err() != nil {
			return Result{Status: Cancelled, Message: "Stopped"}
		}
		return Result{Status: Failed, Message: "Flatpak reported: " + err.Error()}
	}
	return Result{Status: Done, Message: msg}
}

// command builds the silent run: msi packages go through msiexec, other kinds run with their switch.
func command(a manifest.App, file string) (string, []string) {
	switches := strings.Fields(a.SilentSwitch)
	if kind := strings.ToLower(a.InstallerType); kind == "msi" || kind == "wix" {
		return "msiexec", append([]string{"/i", file}, switches...)
	}
	return file, switches
}

// UserAgent is what downloads call themselves.
const UserAgent = "Fileion-Installer"

// Received describes what a download returned, for the log when the SHA-256 does not match.
type Received struct {
	Size        int64
	ContentType string
	FinalURL    string
}

// DirectURL rewrites a SourceForge "…/projects/{p}/files/{path}/download" address to the direct
// "https://downloads.sourceforge.net/project/{p}/{path}" form, which redirects to a mirror instead of showing a web page.
// Any other address is returned unchanged. The SHA-256 check still decides whether the file is run.
func DirectURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || (u.Host != "sourceforge.net" && u.Host != "www.sourceforge.net") {
		return raw
	}
	parts := strings.SplitN(strings.TrimPrefix(u.Path, "/"), "/", 4) // projects, {p}, files, {path}
	if len(parts) < 4 || parts[0] != "projects" || parts[2] != "files" {
		return raw
	}
	path := strings.TrimSuffix(strings.TrimSuffix(parts[3], "/download"), "/")
	if path == "" {
		return raw
	}
	return "https://downloads.sourceforge.net/project/" + parts[1] + "/" + path
}

// download writes the vendor file to the temp folder and returns its path and SHA-256. Only https, also across redirects.
func (r *Runner) download(ctx context.Context, a manifest.App) (string, string, Received, error) {
	var got Received
	if !strings.HasPrefix(a.URL, "https://") {
		return "", "", got, fmt.Errorf("the address is not https")
	}
	ext := filepath.Ext(strings.TrimSuffix(strings.SplitN(a.URL, "?", 2)[0], "/download"))
	if len(ext) > 6 || ext == "" {
		ext = ".exe"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, DirectURL(a.URL), nil)
	if err != nil {
		return "", "", got, err
	}
	// SourceForge (and similar hosts) show a web page with a countdown to anything that calls itself a browser; a program that says what it is gets the file
	req.Header.Set("User-Agent", UserAgent)
	// A redirect to anything but https is refused
	client := *r.Client
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return fmt.Errorf("redirected to a non-https address")
		}
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		return nil
	}
	res, err := client.Do(req)
	if err != nil {
		return "", "", got, err
	}
	defer res.Body.Close()
	got.FinalURL, got.ContentType = res.Request.URL.String(), res.Header.Get("Content-Type")
	if res.StatusCode != http.StatusOK {
		return "", "", got, fmt.Errorf("the vendor answered HTTP %d", res.StatusCode)
	}
	f, err := os.CreateTemp(r.TempDir, "fileion-*"+ext)
	if err != nil {
		return "", "", got, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), res.Body)
	got.Size = n
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", "", got, err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", "", got, err
	}
	return f.Name(), hex.EncodeToString(h.Sum(nil)), got, nil
}

// AtLeast says whether the installed version is the manifest's or newer (see version.AtLeast).
func AtLeast(installed, min string) bool { return version.AtLeast(installed, min) }

// DefaultTimeout is how long one installer may run before it is stopped.
const DefaultTimeout = 10 * time.Minute

// DefaultClient has a connection timeout but none for the body: a large installer on a slow line is not an error.
func DefaultClient() *http.Client {
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 60 * time.Second}}
}
