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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"fileion-installer/internal/manifest"
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
	Cancelled   Status = "cancelled"
)

// Result is the end state of one app.
type Result struct {
	Status  Status
	Message string
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
	// Progress is told about each step (may be nil).
	Progress func(app manifest.App, s Status, msg string)
}

func (r *Runner) say(a manifest.App, s Status, msg string) {
	if r.Progress != nil {
		r.Progress(a, s, msg)
	}
}

// RunApp installs (or updates) one Windows app from its manifest entry.
func (r *Runner) RunApp(ctx context.Context, a manifest.App) Result {
	res := r.runApp(ctx, a)
	r.say(a, res.Status, res.Message)
	return res
}

func (r *Runner) runApp(ctx context.Context, a manifest.App) Result {
	if ctx.Err() != nil {
		return Result{Cancelled, "Stopped before this one"}
	}
	if a.FlatpakID != "" {
		return r.runFlatpak(ctx, a)
	}
	if v, found := r.Installed(a); found && AtLeast(v, a.MinVersion) {
		return Result{UpToDate, "Already at " + v}
	}
	kind := strings.ToLower(a.InstallerType)
	if kind == "msix" || kind == "appx" || kind == "msstore" || kind == "zip" || kind == "portable" || strings.TrimSpace(a.SilentSwitch) == "" {
		return Result{Unsupported, "Needs its own installer: get it from its Fileion page"}
	}

	r.say(a, Downloading, "Downloading from the vendor")
	file, sum, err := r.download(ctx, a)
	if err != nil {
		if ctx.Err() != nil {
			return Result{Cancelled, "Stopped"}
		}
		return Result{Failed, "Download failed: " + err.Error()}
	}
	defer os.Remove(file)

	r.say(a, Verifying, "Checking the SHA-256")
	if sum != strings.ToLower(a.SHA256) {
		return Result{Mismatch, "Skipped: the file does not match the vendor's SHA-256, so it was not run"}
	}

	r.say(a, Installing, "Installing")
	name, args := command(a, file)
	if err := r.Exec(ctx, name, args); err != nil {
		if ctx.Err() != nil {
			return Result{Cancelled, "Stopped"}
		}
		return Result{Failed, "The installer reported: " + err.Error()}
	}
	return Result{Done, "Installed " + strings.TrimSpace(a.Version)}
}

func (r *Runner) runFlatpak(ctx context.Context, a manifest.App) Result {
	_, installed := r.Installed(a)
	args := []string{"install", "-y", "--noninteractive", "flathub", a.FlatpakID}
	msg := "Installed"
	if installed {
		args = []string{"update", "-y", "--noninteractive", a.FlatpakID}
		msg = "Checked and updated if needed"
	}
	r.say(a, Installing, "Installing with Flatpak")
	if err := r.Exec(ctx, "flatpak", args); err != nil {
		if ctx.Err() != nil {
			return Result{Cancelled, "Stopped"}
		}
		return Result{Failed, "Flatpak reported: " + err.Error()}
	}
	return Result{Done, msg}
}

// command builds the silent run: msi packages go through msiexec, other kinds run with their switch.
func command(a manifest.App, file string) (string, []string) {
	switches := strings.Fields(a.SilentSwitch)
	if kind := strings.ToLower(a.InstallerType); kind == "msi" || kind == "wix" {
		return "msiexec", append([]string{"/i", file}, switches...)
	}
	return file, switches
}

// download writes the vendor file to the temp folder and returns its path and SHA-256. Only https; nothing else is fetched.
func (r *Runner) download(ctx context.Context, a manifest.App) (string, string, error) {
	if !strings.HasPrefix(a.URL, "https://") {
		return "", "", fmt.Errorf("the address is not https")
	}
	ext := filepath.Ext(strings.SplitN(a.URL, "?", 2)[0])
	if len(ext) > 6 || ext == "" {
		ext = ".exe"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return "", "", err
	}
	res, err := r.Client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("the vendor answered HTTP %d", res.StatusCode)
	}
	f, err := os.CreateTemp(r.TempDir, "fileion-*"+ext)
	if err != nil {
		return "", "", err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), res.Body); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", "", err
	}
	return f.Name(), hex.EncodeToString(h.Sum(nil)), nil
}

// AtLeast says whether the installed version is the manifest's or newer. Versions compare as dotted numbers
// ("3.0.20" < "3.0.21", "10.0" > "9.9"); a missing minimum means any installed copy counts, as does an unreadable one.
func AtLeast(installed, min string) bool {
	if strings.TrimSpace(min) == "" {
		return true
	}
	a, b := numbers(installed), numbers(min)
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x > y
		}
	}
	return true
}

func numbers(v string) []int {
	var out []int
	for _, part := range strings.FieldsFunc(strings.TrimLeft(strings.TrimSpace(v), "vV"), func(r rune) bool { return r == '.' || r == '-' || r == '+' || r == '_' }) {
		n, err := strconv.Atoi(strings.TrimRight(part, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"))
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out
}

// DefaultClient has a connection timeout but none for the body: a large installer on a slow line is not an error.
func DefaultClient() *http.Client {
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 60 * time.Second}}
}
