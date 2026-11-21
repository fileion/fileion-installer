// Package run installs the apps of a manifest one by one: download from the vendor URL only, check the vendor's SHA-256
// (a mismatch is never run), run the silent switch, and skip apps that are already at the manifest's version.
package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

	"fileion-installer/internal/manifest"
	"fileion-installer/internal/portable"
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
	Unpacking   Status = "unpacking"   // a zip being unpacked
	Saved       Status = "saved"       // a portable app or a zip is in its folder
	YourTurn    Status = "yourturn"    // an interactive app: its own setup window is open and waits for the person
	NotFinished Status = "notfinished" // the setup window closed and the app is not installed: Retry or Skip
	Skipped     Status = "skipped"     // the person chose Skip
	NotRun      Status = "notrun"      // the person stopped the run before this one
)

// Result is the end state of one app.
type Result struct {
	Status  Status
	Message string
	Link    string // the app's Fileion page, for the statuses that send the person there
}

// OK says whether the app counts as worked in the anonymous count (installed, updated or already current).
func (r Result) OK() bool { return r.Status == Done || r.Status == UpToDate || r.Status == Saved }

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
	// PollEvery is how often the registry is looked at while an installer runs (0 = 4 s); WaitHint is when the window says the installer
	// may be waiting for the person (0 = 60 s); Grace is how long an installer that is already detected gets to end by itself (0 = 30 s).
	PollEvery, WaitHint, Grace time.Duration
	// Log gets diagnostic lines for the log file (may be nil).
	Log func(format string, args ...any)
	// Progress is told about each step (may be nil).
	Progress func(app manifest.App, s Status, msg string, link string)
	// Portable keeps the list of portable and zip apps; AppsDir is the folder their own folders go in (Downloads\Fileion Apps).
	Portable *portable.Store
	AppsDir  string
	// ExecJob runs a program and waits until it and everything it started has ended (Windows: a job object). When leave is closed it stops
	// waiting and returns WITHOUT ending anything (an app the setup launched at its end keeps running). Nil = Exec.
	ExecJob func(ctx context.Context, name string, args []string, leave <-chan struct{}) error
	// Stopped says whether the person chose Stop (so a closed setup is "Stopped", not "Skipped by you"); may be nil.
	Stopped func() bool
	// StopCh is closed when the person chooses Stop: a download in progress ends at once and its row says "Not run". A setup already running is not touched. May be nil.
	StopCh func() <-chan struct{}
	// Front brings the window to the front and flashes its taskbar button, when it is the person's turn (may be nil).
	Front func()
	// Arm returns a channel that is closed when the person presses Skip (or Stop) for this app; nil = never.
	Arm func(app manifest.App) <-chan struct{}
	// Ask waits for "retry" or "skip" after an interactive setup closed without the app being installed; nil = skip.
	Ask func(ctx context.Context, app manifest.App) string
	// Placed tells where a portable app or a zip ended up (may be nil).
	Placed func(app manifest.App, folder, exe string)
	// Transfer is told how many bytes of the download have arrived (total is 0 when the vendor does not say), at most a few times a second (may be nil).
	Transfer func(app manifest.App, done, total int64)
}

func (r *Runner) say(a manifest.App, s Status, msg string, link string) {
	if r.Progress != nil {
		r.Progress(a, s, msg, link)
	}
}

// RunApp installs (or updates) one Windows app from its manifest entry. A panic anywhere in the work for one app is logged with its stack and
// becomes that app's failure: it never takes the whole run (and the window) down.
func (r *Runner) RunApp(ctx context.Context, a manifest.App) Result {
	return r.RunAppAhead(ctx, a, nil)
}

// RunAppAhead is RunApp with the app's download possibly already started by Ahead (take waits for it).
func (r *Runner) RunAppAhead(ctx context.Context, a manifest.App, take func() Fetched) (res Result) {
	defer func() {
		if p := recover(); p != nil {
			if r.Log != nil {
				r.Log("%s: PANIC %v\n%s", a.Name, p, debug.Stack())
			}
			res = Result{Status: Failed, Message: "The installer hit an internal error on this app (see the log)"}
			r.say(a, res.Status, res.Message, res.Link)
		}
	}()
	res = r.runAppWith(ctx, a, take)
	if res.Status == Unsupported || res.Status == Manual || res.Status == Failed || res.Status == Mismatch {
		res.Link = a.Page
	}
	r.say(a, res.Status, res.Message, res.Link)
	return res
}

func (r *Runner) logf(a manifest.App, format string, args ...any) {
	if r.Log != nil {
		r.Log("%s: "+format, append([]any{a.Name}, args...)...)
	}
}

// pre decides, without touching anything, whether the app ends before any download: stopped, a Flatpak, not understood, already current or not something
// this program can run. done=false means it needs the download and the install.
func (r *Runner) pre(ctx context.Context, a manifest.App) (res Result, done bool) {
	if ctx.Err() != nil {
		return Result{Status: Cancelled, Message: "Stopped before this one"}, true
	}
	if a.FlatpakID != "" {
		return Result{}, false
	}
	mode := a.EffectiveMode()
	if !a.KnownMode() {
		return Result{Status: Unsupported, Message: "Needs a newer Fileion Installer: get it from its Fileion page"}, true
	}
	kind := strings.ToLower(a.InstallerType)
	if mode == manifest.ModePortable || mode == manifest.ModeZip {
		if v, folder, found := r.portableAt(a); found && AtLeast(v, a.MinVersion) {
			return Result{Status: UpToDate, Message: "Already at " + v + " in " + folder}, true
		}
	} else if v, found := r.Installed(a); found && AtLeast(v, a.MinVersion) {
		return Result{Status: UpToDate, Message: "Already at " + v}, true
	}
	// A kind this program cannot run (a store package), and a "silent" entry with nothing to run it with (the way older servers marked the rest)
	if kind == "msix" || kind == "appx" || kind == "msstore" || (mode == manifest.ModeSilent && (kind == "zip" || kind == "portable" || strings.TrimSpace(a.SilentSwitch) == "")) {
		return Result{Status: Unsupported, Message: "Needs its own installer: get it from its Fileion page"}, true
	}
	return Result{}, false
}

// Fetched is the end of one app's download: the file (already checked: hashed over the whole file) or why there is none.
type Fetched struct {
	File, Sum string
	Got       Received
	Err       error
}

// fetchApp downloads one app (with retries) and tells the row. Stop ends it at once.
func (r *Runner) fetchApp(ctx context.Context, a manifest.App) Fetched {
	r.say(a, Downloading, "Downloading from the vendor", "")
	dlCtx, endDl := context.WithCancel(ctx) // Stop ends the download, never a setup that is already running
	defer endDl()
	if r.StopCh != nil {
		stop := r.StopCh()
		go func() {
			select {
			case <-stop:
				endDl()
			case <-dlCtx.Done():
			}
		}()
	}
	file, sum, got, err := r.download(dlCtx, a)
	return Fetched{file, sum, got, err}
}

// runAppWith runs one app; ahead (may be nil) is its download started earlier, waited for here, else it downloads now.
func (r *Runner) runAppWith(ctx context.Context, a manifest.App, ahead func() Fetched) Result {
	if res, done := r.pre(ctx, a); done {
		if ahead != nil { // its download was started and is no longer wanted
			if f := ahead(); f.File != "" {
				os.Remove(f.File)
			}
		}
		return res
	}
	if a.FlatpakID != "" {
		return r.runFlatpak(ctx, a)
	}
	mode := a.EffectiveMode()
	var f Fetched
	if ahead != nil {
		f = ahead()
	} else {
		f = r.fetchApp(ctx, a)
	}
	file, sum, got, err := f.File, f.Sum, f.Got, f.Err
	if err != nil {
		if ctx.Err() != nil {
			return Result{Status: Cancelled, Message: "Stopped"}
		}
		if r.Stopped != nil && r.Stopped() {
			return Result{Status: NotRun, Message: "Not run"}
		}
		r.logf(a, "download failed: %v", err)
		return Result{Status: Failed, Message: "Couldn't download from the vendor"}
	}
	defer os.Remove(file)

	r.say(a, Verifying, "Checking the SHA-256", "")
	if sum != strings.ToLower(a.SHA256) {
		if r.Log != nil {
			r.Log("%s: SHA-256 mismatch: received %d bytes, Content-Type %q, final address %s", a.Name, got.Size, got.ContentType, got.FinalURL)
		}
		return Result{Status: Mismatch, Message: "Skipped: the file does not match the vendor's SHA-256, so it was not run"}
	}

	if r.Stopped != nil && r.Stopped() { // chosen while the hash was being checked: nothing has started yet
		return Result{Status: NotRun, Message: "Not run"}
	}
	switch mode {
	case manifest.ModePortable:
		return r.placePortable(a, file)
	case manifest.ModeZip:
		return r.unpackZip(a, file)
	case manifest.ModeInteractive:
		name, args := command(a, file)
		return r.runInteractive(ctx, a, name, args)
	}
	r.say(a, Installing, "Installing", "")
	name, args := command(a, file)
	return r.install(ctx, a, name, args)
}

// portableAt looks the app up in the list of portable and zip apps kept on this PC.
func (r *Runner) portableAt(a manifest.App) (version, folder string, found bool) {
	if r.Portable == nil {
		return "", "", false
	}
	e, ok := r.Portable.Find(a.WingetID)
	return e.Version, r.shown(e.Folder), ok
}

// shown is a folder as the window says it: "Downloads\Fileion Apps\Rufus" for the usual place, the whole path elsewhere.
func (r *Runner) shown(dir string) string {
	apps := filepath.Clean(r.AppsDir)
	if r.AppsDir != "" && strings.EqualFold(filepath.Base(filepath.Dir(apps)), "Downloads") && strings.HasPrefix(strings.ToLower(dir), strings.ToLower(apps)) {
		return filepath.Join(filepath.Base(filepath.Dir(apps)), strings.TrimPrefix(dir, filepath.Dir(apps)+string(filepath.Separator)))
	}
	return dir
}

func (r *Runner) appFolder(a manifest.App) (string, error) {
	if r.AppsDir == "" {
		return "", errors.New("no folder for portable apps")
	}
	dir := filepath.Join(r.AppsDir, portable.SafeName(a.Name))
	if err := os.MkdirAll(r.AppsDir, 0o755); err != nil {
		return "", err
	}
	if portable.IsLink(r.AppsDir) || portable.IsLink(dir) { // elevated: never write through a symlink or junction
		return "", errors.New("it is a link, so nothing was written there")
	}
	return dir, os.MkdirAll(dir, 0o755)
}

func (r *Runner) remember(a manifest.App, dir, exe string) {
	if r.Portable != nil {
		if err := r.Portable.Put(portable.Entry{WingetID: a.WingetID, Name: a.Name, Version: strings.TrimSpace(a.Version), Folder: dir, Exe: exe}); err != nil && r.Log != nil {
			r.Log("%s: the portable list could not be saved: %v", a.Name, err)
		}
	}
	if r.Placed != nil {
		r.Placed(a, dir, exe)
	}
}

// placePortable puts the verified file in the app's own folder (Downloads\Fileion Apps\<App>) and notes it in the list.
func (r *Runner) placePortable(a manifest.App, file string) Result {
	dir, err := r.appFolder(a)
	if err != nil {
		return Result{Status: Failed, Message: "Could not make the app's folder (details are in the log)"}
	}
	name := portable.SafeName(filepath.Base(strings.SplitN(strings.TrimSuffix(strings.SplitN(a.URL, "?", 2)[0], "/download"), "#", 2)[0]))
	if filepath.Ext(name) == "" {
		name += ".exe"
	}
	target := filepath.Join(dir, name)
	if portable.IsLink(target) {
		return Result{Status: Failed, Message: "Could not save the file: its place is a link"}
	}
	if err := portable.MoveFile(file, target); err != nil {
		r.logf(a, "could not save the file: %v", err)
		return Result{Status: Failed, Message: "Could not save the file (details are in the log)"}
	}
	r.remember(a, dir, target)
	return Result{Status: Saved, Message: "Saved in " + r.shown(dir)}
}

// unpackZip unpacks the verified zip into the app's folder; the zip itself is deleted by the caller.
func (r *Runner) unpackZip(a manifest.App, file string) Result {
	dir, err := r.appFolder(a)
	if err != nil {
		return Result{Status: Failed, Message: "Could not make the app's folder (details are in the log)"}
	}
	r.say(a, Unpacking, "Unpacking…", "")
	if err := portable.Unzip(file, dir); err != nil {
		r.logf(a, "could not unpack: %v", err)
		return Result{Status: Failed, Message: "Could not unpack the file (details are in the log)"}
	}
	r.remember(a, dir, portable.FindExe(dir, a.ZipExe))
	return Result{Status: Saved, Message: "Saved in " + r.shown(dir)}
}

// runInteractive is an app that has no silent switch ("a few clicks"): its own setup window opens and the person finishes it. The whole process
// tree is waited for (a setup that starts another program and exits is still waited for), then the registry decides: installed is Done, else
// "Not finished" and Retry or Skip. Skip (or Stop) ends the setup and moves on, so nothing can hang.
func (r *Runner) runInteractive(ctx context.Context, a manifest.App, name string, args []string) Result {
	limit, poll, grace := r.AppTimeout, r.PollEvery, r.Grace
	if limit < 30*time.Minute { // a person takes their time: at least half an hour
		limit = 30 * time.Minute
	}
	if poll <= 0 {
		poll = 4 * time.Second
	}
	if grace <= 0 {
		grace = 30 * time.Second
	}
	run := func(c context.Context, n string, ar []string, leave <-chan struct{}) error {
		if r.ExecJob != nil {
			return r.ExecJob(c, n, ar, leave)
		}
		done := make(chan error, 1)
		go func() { done <- r.Exec(c, n, ar) }()
		select {
		case err := <-done:
			return err
		case <-leave:
			return nil
		}
	}
	present := func() (string, bool) {
		v, found := r.Installed(a)
		return v, found && AtLeast(v, a.MinVersion)
	}
	done := Result{Status: Done, Message: "Installed " + strings.TrimSpace(a.Version)}
	for {
		r.say(a, YourTurn, "Your turn: finish "+a.Name+"’s setup window", "")
		if r.Front != nil {
			r.Front()
		}
		var skip <-chan struct{}
		if r.Arm != nil {
			skip = r.Arm(a)
		}
		runCtx, end := context.WithTimeout(ctx, limit)
		var skipped atomic.Bool
		stop := make(chan struct{})
		go func() {
			select {
			case <-skip:
				skipped.Store(true)
				end()
			case <-stop:
			}
		}()
		leave := make(chan struct{})
		ended := make(chan error, 1)
		go func() { ended <- run(runCtx, name, args, leave) }()
		// While the setup is open the registry is looked at, as for a silent installer: a setup that ends with "Launch the app" leaves the app (and
		// so the job) running although the app is installed. Once found, the setup gets a short time to end by itself, then it is left alone, not killed.
		tick := time.NewTicker(poll)
		var err error
		left := false
	wait:
		for {
			select {
			case err = <-ended:
				break wait
			case <-tick.C:
				if v, ok := present(); ok {
					if r.Log != nil {
						r.Log("%s: detected as installed at %s while its setup is still open; waiting up to %s for it to end", a.Name, v, grace)
					}
					select {
					case err = <-ended:
					case <-time.After(grace):
						close(leave)
						left = true
						err = <-ended
						if r.Log != nil {
							r.Log("%s: its setup is still open after %s and is left running (not killed): the app is installed", a.Name, grace)
						}
					case <-runCtx.Done():
						err = <-ended
					}
					break wait
				}
			}
		}
		tick.Stop()
		timedOut := errors.Is(runCtx.Err(), context.DeadlineExceeded)
		close(stop)
		end()
		if r.Log != nil {
			r.Log("%s: its setup window ended (%v)", a.Name, err)
		}
		_, installed := present()
		switch {
		case ctx.Err() != nil:
			return Result{Status: Cancelled, Message: "Stopped"}
		case installed && left:
			d := done
			d.Message += " (its setup is still open: you can close it)"
			return d
		case installed:
			return done // also when Skip or Stop came a moment after the app was installed
		case skipped.Load():
			if r.Stopped != nil && r.Stopped() {
				return Result{Status: Cancelled, Message: "Stopped"}
			}
			return Result{Status: Skipped, Message: "Skipped by you"}
		case timedOut:
			return Result{Status: Manual, Message: "Skipped: needs manual install"}
		}
		r.say(a, NotFinished, "Setup window closed before the app installed", "")
		answer := "skip"
		if r.Ask != nil {
			answer = r.Ask(ctx, a)
		}
		if ctx.Err() != nil {
			return Result{Status: Cancelled, Message: "Stopped"}
		}
		if answer != "retry" {
			if r.Stopped != nil && r.Stopped() {
				return Result{Status: Cancelled, Message: "Stopped"}
			}
			return Result{Status: Skipped, Message: "Skipped by you"}
		}
	}
}

// install runs the vendor installer and watches it. Some installers never return on their own (a finish page, a "close the running app"
// prompt, a child process left alive) although the app is already installed, so while it runs the Uninstall registry is polled (by key or
// by the name rule): when the app appears at the expected version it counts as installed and the run moves on. The installer is then
// given a grace period to end by itself and, if it is still open, is LEFT RUNNING, not killed: ending a vendor installer in the middle of
// its clean-up (shortcuts, services, file associations) can leave the app half set up, and the app is already registered and works. Its
// window is not hidden (see sys.Exec), so a prompt is visible, and after a minute without detection the window says it may be waiting.
func (r *Runner) install(ctx context.Context, a manifest.App, name string, args []string) Result {
	limit, poll, hint, grace := r.AppTimeout, r.PollEvery, r.WaitHint, r.Grace
	if limit <= 0 {
		limit = DefaultTimeout
	}
	if poll <= 0 {
		poll = 4 * time.Second
	}
	if hint <= 0 {
		hint = 60 * time.Second
	}
	if grace <= 0 {
		grace = 30 * time.Second
	}
	// The installer's own lifetime is the run's, not this function's: it is only ended on the time limit or when the run is stopped
	execCtx, endIt := context.WithCancel(ctx)
	leave := false // true when the installer is deliberately left running
	defer func() {
		if !leave {
			endIt()
		}
	}()
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- r.Exec(execCtx, name, args) }()
	logf := func(format string, args ...any) {
		if r.Log != nil {
			r.Log("%s: "+format, append([]any{a.Name}, args...)...)
		}
	}
	elapsed := func() string { return time.Since(start).Round(time.Second).String() }
	finish := func(err error) Result { // the installer ended by itself
		code := exitCode(err)
		logf("installer exited after %s with code %d (%v)", elapsed(), code, err)
		if ctx.Err() != nil {
			return Result{Status: Cancelled, Message: "Stopped"}
		}
		if err != nil {
			return Result{Status: Failed, Message: fmt.Sprintf("The installer did not finish (exit code %d). Details are in the log.", code)}
		}
		return Result{Status: Done, Message: "Installed " + strings.TrimSpace(a.Version)}
	}
	present := func() (string, bool) {
		v, found := r.Installed(a)
		return v, found && AtLeast(v, a.MinVersion)
	}

	// Ending the installer is not done until its process tree is killed: the kill runs in the Exec goroutine, and the run may exit right after this returns
	endAndWait := func() {
		endIt()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	}
	tick, hintT, deadline := time.NewTicker(poll), time.NewTimer(hint), time.NewTimer(limit)
	defer tick.Stop()
	defer hintT.Stop()
	defer deadline.Stop()
	for {
		select {
		case err := <-done:
			if err != nil && ctx.Err() == nil {
				if v, ok := present(); ok { // some installers return a non-zero code (a restart is needed) and the app is there
					logf("installer exited after %s with code %d (%v), but the app is installed at %s", elapsed(), exitCode(err), err, v)
					return Result{Status: Done, Message: "Installed " + strings.TrimSpace(a.Version)}
				}
			}
			return finish(err)
		case <-tick.C:
			v, ok := present()
			if !ok {
				continue
			}
			logf("detected as installed at %s after %s while its installer is still running; waiting up to %s for it to end", v, elapsed(), grace)
			select {
			case err := <-done:
				return finish(err)
			case <-time.After(grace):
				leave = true
				logf("its installer is still open after %s and is left running (not killed): the app is installed", elapsed())
				return Result{Status: Done, Message: "Installed " + strings.TrimSpace(a.Version) + " (its installer is still open: you can close it)"}
			case <-ctx.Done():
				endAndWait()
				return Result{Status: Cancelled, Message: "Stopped"}
			}
		case <-hintT.C:
			logf("still running after %s and not detected yet: it may be waiting for the person", elapsed())
			r.say(a, Installing, a.Name+"'s installer may be waiting for you: look for its window or in the taskbar", "")
		case <-deadline.C:
			logf("time limit of %s reached; ending the installer", limit)
			endAndWait()
			return Result{Status: Manual, Message: "Skipped: needs manual install"}
		case <-ctx.Done():
			endAndWait()
			return Result{Status: Cancelled, Message: "Stopped"}
		}
	}
}

// exitCode is the installer's exit code from the error Exec returned (0 for none, -1 when it did not run to an exit).
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
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
		r.logf(a, "flatpak: %v", err)
		return Result{Status: Failed, Message: "Flatpak could not finish (details are in the log)"}
	}
	return Result{Status: Done, Message: msg}
}

// command builds the silent run: msi packages go through msiexec, other kinds run with their switch.
func command(a manifest.App, file string) (string, []string) {
	switches := strings.Fields(a.SilentSwitch)
	if a.EffectiveMode() == manifest.ModeInteractive {
		switches = nil // the vendor's own window: no switch
	}
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

// Retries is how many times a download that breaks off is started again; BackoffFor is the wait before try n (1-based). Tests shorten it.
const Retries = 3

var BackoffFor = func(n int) time.Duration { return time.Duration(2*n*n) * time.Second } // 2 s, 8 s, 18 s

// permanent is an answer that trying again cannot change (a missing file, a refusal): not retried.
type permanent struct{ error }

// download writes the vendor file to the temp folder and returns its path and SHA-256. Only https, also across redirects. A download that breaks off
// (the connection is reset, a 5xx) is tried again up to Retries times with a growing wait, and continues from the bytes already there (HTTP Range) when
// the vendor allows it, else starts over. The SHA-256 is always taken over the WHOLE file at the end, never over the parts. The temp file is removed on every error.
func (r *Runner) download(ctx context.Context, a manifest.App) (string, string, Received, error) {
	var got Received
	if !strings.HasPrefix(a.URL, "https://") {
		return "", "", got, fmt.Errorf("the address is not https")
	}
	ext := filepath.Ext(strings.TrimSuffix(strings.SplitN(a.URL, "?", 2)[0], "/download"))
	if len(ext) > 6 || ext == "" {
		ext = ".exe"
	}
	f, err := os.CreateTemp(r.TempDir, "fileion-*"+ext)
	if err != nil {
		return "", "", got, err
	}
	fail := func(err error) (string, string, Received, error) {
		f.Close()
		os.Remove(f.Name())
		return "", "", got, err
	}
	var have int64
	var validator string // ETag or Last-Modified of the first answer: a resumed range is only taken if the file is still the same
	for attempt := 0; ; attempt++ {
		err = r.fetchPart(ctx, a, f, &have, &validator, &got)
		if err == nil {
			break
		}
		var perm permanent
		if ctx.Err() != nil || errors.As(err, &perm) || attempt >= Retries {
			return fail(err)
		}
		r.logf(a, "download interrupted after %d bytes (%v): try %d of %d, %s", have, err, attempt+1, Retries, BackoffFor(attempt+1))
		r.say(a, Downloading, "Download interrupted. Retrying…", "")
		select {
		case <-ctx.Done():
			return fail(ctx.Err())
		case <-time.After(BackoffFor(attempt + 1)):
		}
		r.say(a, Downloading, "Downloading from the vendor", "")
	}
	// the hash over the whole file, as it is on disk
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return fail(err)
	}
	got.Size = n
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", "", got, err
	}
	return f.Name(), hex.EncodeToString(h.Sum(nil)), got, nil
}

// fetchPart asks for the file (from byte *have on, when something is already there) and appends what arrives to f, keeping *have exact.
func (r *Runner) fetchPart(ctx context.Context, a manifest.App, f *os.File, have *int64, validator *string, got *Received) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, DirectURL(a.URL), nil)
	if err != nil {
		return permanent{err}
	}
	// SourceForge (and similar hosts) show a web page with a countdown to anything that calls itself a browser; a program that says what it is gets the file
	req.Header.Set("User-Agent", UserAgent)
	if *have > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", *have))
		if *validator != "" {
			req.Header.Set("If-Range", *validator)
		}
	}
	// A redirect to anything but https is refused
	client := *r.Client
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return permanent{fmt.Errorf("redirected to a non-https address")}
		}
		if len(via) >= 10 {
			return permanent{fmt.Errorf("too many redirects")}
		}
		return nil
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	got.FinalURL, got.ContentType = res.Request.URL.String(), res.Header.Get("Content-Type")
	total := max(res.ContentLength, 0) // -1 = the vendor does not say
	switch {
	case res.StatusCode == http.StatusPartialContent && *have > 0 && strings.HasPrefix(res.Header.Get("Content-Range"), fmt.Sprintf("bytes %d-", *have)):
		total += *have // the range's length is the rest; the whole file is what the person is waiting for
	case res.StatusCode == http.StatusOK: // the whole file again: the vendor does not do ranges (or the file changed)
		*have = 0
		if err := f.Truncate(0); err != nil {
			return permanent{err}
		}
		if *validator = res.Header.Get("ETag"); *validator == "" {
			*validator = res.Header.Get("Last-Modified")
		}
	case res.StatusCode == http.StatusRequestedRangeNotSatisfiable || res.StatusCode == http.StatusPartialContent:
		*have = 0 // unusable: start over on the next try
		_ = f.Truncate(0)
		return fmt.Errorf("the vendor's answer to a range request was unusable (HTTP %d)", res.StatusCode)
	case res.StatusCode >= 500 || res.StatusCode == http.StatusTooManyRequests:
		return fmt.Errorf("the vendor answered HTTP %d", res.StatusCode)
	default:
		return permanent{fmt.Errorf("the vendor answered HTTP %d", res.StatusCode)}
	}
	if _, err := f.Seek(*have, io.SeekStart); err != nil {
		return permanent{err}
	}
	tell := func(done, total int64) {
		if r.Transfer != nil {
			r.Transfer(a, done, total)
		}
	}
	pw := &progress{done: *have, total: total, tell: tell}
	cw := &countWriter{f: f, n: have}
	_, err = io.Copy(io.MultiWriter(cw, pw), res.Body)
	tell(*have, total) // the last figure, whatever the pace of the updates
	if err == nil && res.ContentLength >= 0 && *have != total {
		err = io.ErrUnexpectedEOF
	}
	return err
}

// countWriter writes to the file and counts what really reached it.
type countWriter struct {
	f *os.File
	n *int64
}

func (c *countWriter) Write(b []byte) (int, error) {
	n, err := c.f.Write(b)
	*c.n += int64(n)
	return n, err
}

// progress counts the bytes of a download and reports them at most four times a second, and once more at the end.
type progress struct {
	done, total int64
	last        time.Time
	tell        func(done, total int64)
}

func (p *progress) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	if now := time.Now(); now.Sub(p.last) >= 250*time.Millisecond || (p.total > 0 && p.done >= p.total) {
		p.last = now
		p.tell(p.done, p.total)
	}
	return len(b), nil
}

// AtLeast says whether the installed version is the manifest's or newer (see version.AtLeast).
func AtLeast(installed, min string) bool { return version.AtLeast(installed, min) }

// DefaultTimeout is how long one installer may run before it is stopped.
const DefaultTimeout = 10 * time.Minute

// DefaultClient has a connection timeout but none for the body: a large installer on a slow line is not an error.
func DefaultClient() *http.Client {
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 60 * time.Second}}
}
