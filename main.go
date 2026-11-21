// Fileion Installer: reads the short code stamped in its own file (or, failing that, in its name), gets the app list from Fileion, and installs each app
// from its publisher's own download after checking the publisher's SHA-256. Run it again and it updates the same apps.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

	"fileion-installer/internal/manifest"
	"fileion-installer/internal/run"
	"fileion-installer/internal/sys"
	"fileion-installer/internal/ui"
)

// version is set by the build (-X main.version=…)
var version = "dev"

func main() {
	code := flag.String("code", "", "the set code (normally stamped inside this file, or in its name)")
	base := flag.String("base", "https://software.fileion.com", "where the list and the count live")
	noWindow := flag.Bool("no-window", false, "print progress instead of opening the window")
	appTimeout := flag.Duration("app-timeout", run.DefaultTimeout, "stop an installer that runs longer than this and mark it for manual install")
	noReport := flag.Bool("no-report", false, "do not send the anonymous count at the end")
	handoff := flag.String("handoff", "", "internal: the file the elevated copy writes its window address to (the first copy's window then moves to it)")
	userDirs := flag.String("user-dirs", "", "internal: the signed-in person's Downloads, Start menu Programs and LocalAppData folders, joined by |")
	flag.Parse()

	osName := runtime.GOOS
	sys.AttachParentConsole() // a Windows GUI program has no console: from a terminal, output goes to that terminal
	if osName != "windows" && osName != "linux" {
		fmt.Fprintln(os.Stderr, "Fileion Installer runs on Windows and Linux. On a Mac, use the command from software.fileion.com/setup.")
		os.Exit(2)
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		earlyFailure(nil, nil, osName, *noWindow, "This computer's processor ("+runtime.GOARCH+") is not supported by the Fileion Installer. Use the install command from software.fileion.com/setup instead.")
		os.Exit(2)
	}
	_, _, localAppData := userFolders(*userDirs)
	logPath := newLogPath(localAppData)
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	var logOut io.Writer = io.Discard // a GUI program's stderr may be no handle at all
	if err == nil {
		defer lf.Close()
		logOut = lf
	}
	logger := log.New(logOut, "", log.LstdFlags)
	sys.Log = logger.Printf

	// The list's code: the -code flag, else stamped inside this file by Fileion (so the file can be renamed and copied), else in its name
	var source string
	*code, source = resolveCode(*code, selfPaths())
	if *code == "" {
		logger.Printf("start: version %s, %s: no set code in this file", version, osName)
		earlyFailure(logger, &logPath, osName, *noWindow, "This file has no app list. Get your installer from software.fileion.com/setup")
		os.Exit(2)
	}
	logger.Printf("start: version %s, code %s (from the %s), %s", version, *code, source, osName)

	// The window comes up at once ("Preparing"), before the Windows permission prompt and while the list loads. When elevation is needed the first
	// copy keeps this window and starts an elevated copy that does the work; the window then moves to the elevated copy's own page.
	needAdmin := osName == "windows" && !sys.IsAdmin()
	elevated := *handoff != "" // this is the elevated copy
	win, url, err := ui.New(logPath, osName == "windows")
	if err != nil {
		logger.Printf("could not start the window: %v", err)
		earlyFailure(logger, nil, osName, *noWindow, "The installer could not start its window (details are in the log)")
		os.Exit(1)
	}
	logger.Printf("window: %s", url)
	win.OpenLog = func() {
		if err := sys.OpenFile(logPath); err != nil {
			logger.Printf("could not open the log: %v", err)
		}
	}
	win.OpenURL = func(u string) {
		if err := sys.OpenURL(u); err != nil {
			logger.Printf("could not open the link %s: %v", u, err)
		}
	}
	win.OpenLogFolder = func() { // the folder with the newest log selected
		if err := sys.OpenFolderSelect(logPath); err != nil {
			logger.Printf("could not open the log folder: %v", err)
		}
	}
	windowShown := *noWindow
	if elevated {
		windowShown = true // the first copy's window shows this one's page
		if err := os.WriteFile(*handoff, []byte(url), 0o600); err != nil {
			logger.Printf("could not write the hand-off: %v", err)
		}
	} else if !*noWindow {
		if err := sys.OpenWindow(url); err != nil {
			logger.Printf("could not open the window: %v", err)
		} else {
			windowShown = true
			go sys.CenterLoop(win.Closed)
		}
	}
	// a failure is told in the window; when no window could be opened it is a message box (Windows) so it is never silent
	tell := func(msg string) {
		win.Note(ui.Sanitize(msg))
		if !windowShown && osName == "windows" {
			showBox(msg)
		}
	}

	// Windows: one permission prompt, so every app installs for everyone and none asks again
	if needAdmin && !elevated && os.Getenv("FILEION_INSTALLER_ELEVATED") != "1" { // the test harness sets it to see the "rights are needed" message without a prompt
		os.Exit(elevateAndWait(logger, win, *noWindow, logPath, os.Args[1:]))
	}

	// The elevated copy must not install where nobody can see it: if the window was closed during the permission prompt, nothing starts
	if elevated && !*noWindow && !waitForWindow(win, 30*time.Second) {
		logger.Printf("stopped: the window was closed while Windows asked for permission, so nothing was installed")
		os.Exit(1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { <-win.Closed; cancel() }()
	// A crash in the run is logged with its stack and told in the window; it never takes the window down with it
	defer func() {
		if p := recover(); p != nil {
			logger.Printf("PANIC in the run: %v\n%s", p, debug.Stack())
			win.Note("The installer hit an internal error and stopped. The log is at " + logPath)
			win.Finish(0, 0)
			waitForClose(win, *noWindow)
			os.Exit(1)
		}
	}()

	if needAdmin {
		msg := "Administrator rights are needed to install apps, so nothing was installed. Run this file again and choose Yes at the permission prompt."
		logger.Printf("stopped: %s", msg)
		win.Note(msg)
		win.Finish(0, 0)
		if *noWindow {
			fmt.Printf("%s\nLog: %s\n", msg, logPath)
		}
		waitForClose(win, *noWindow)
		os.Exit(1)
	}

	client := run.DefaultClient()
	fetchCtx, stop := context.WithTimeout(ctx, 30*time.Second)
	m, err := manifest.Fetch(fetchCtx, client, *base, *code, osName)
	stop()
	if err != nil {
		logger.Printf("manifest: %v", err)
		msg := "Fileion could not be reached, so the app list could not load. Check the internet connection and run this file again."
		if strings.HasPrefix(err.Error(), "Fileion has no list") { // Fileion's own sentence, no Go error text
			msg = err.Error()
		}
		tell(msg)
		win.Finish(0, 0)
		waitForClose(win, *noWindow)
		os.Exit(1)
	}
	m.Apps = manifest.OrderForRun(m.Apps) // the apps that need the person come last

	index := map[int]int{} // filled once the rows are in their order
	// What is going on, for the heartbeat line in the log: if the program ever dies, the last such line says what it was doing
	var cur atomic.Value
	cur.Store("starting")
	started := time.Now()
	go func() {
		for range time.Tick(30 * time.Second) {
			logger.Printf("alive: %v, %ds into the run", cur.Load(), int(time.Since(started).Seconds()))
		}
	}()

	pf := newPortableFiles(*userDirs, logger)
	win.RowAction = pf.rowAction(win)
	r := &run.Runner{Client: client, TempDir: os.TempDir(), Installed: sys.Installed, Exec: sys.Exec,
		AppTimeout: *appTimeout, Log: logger.Printf,
		Portable: pf.store, AppsDir: pf.appsDir, ExecJob: sys.ExecJob, Front: sys.FrontWindow, Stopped: win.Stopping, StopCh: win.StopChan,
		Arm:      func(a manifest.App) <-chan struct{} { return win.Arm(index[a.ID]) },
		Ask:      func(ctx context.Context, a manifest.App) string { return win.Ask(ctx, index[a.ID]) },
		Placed:   func(a manifest.App, folder, exe string) { pf.placed(win, index[a.ID], a, folder, exe) },
		Transfer: func(a manifest.App, done, total int64) { win.Transfer(index[a.ID], done, total) },
		Progress: func(a manifest.App, s run.Status, msg string, link string) {
			cur.Store(a.Name + ": " + string(s))
			logger.Printf("%s: %s: %s", a.Name, s, msg)
			win.Update(index[a.ID], string(s), ui.Sanitize(msg), link)
			if *noWindow {
				fmt.Printf("%-30s %-12s %s\n", a.Name, s, msg)
			}
		}}

	// Apps that are already up to date go first and are not counted in the heading ("Installing 2 apps"): one pass that touches nothing decides it
	var current []manifest.App
	var rest []manifest.App
	for _, a := range m.Apps {
		if r.Current(ctx, a) {
			current = append(current, a)
		} else {
			rest = append(rest, a)
		}
	}
	m.Apps = append(current, rest...)
	nCurrent := len(current)
	names := make([]string, len(m.Apps))
	for i, a := range m.Apps {
		names[i] = ui.Sanitize(a.Name)
	}
	win.Hold() // "Preparing" until the up-to-date rows are settled (Release below)
	win.SetApps(names)
	for i, a := range m.Apps {
		k, icon := a.EffectiveMode(), ui.SafeIcon(a.Icon)
		win.Mutate(i, func(r *ui.Row) {
			r.Icon = icon
			if k == manifest.ModePortable || k == manifest.ModeZip || k == manifest.ModeInteractive {
				r.Kind = k
			}
		})
	}
	if len(m.Apps) == 0 {
		win.Note("None of the apps in this list can be installed unattended. Get them from their Fileion pages.")
	}

	for i, a := range m.Apps {
		index[a.ID] = i
	}

	results := make([]run.Result, len(m.Apps))
	for i := 0; i < nCurrent; i++ { // the rows already up to date are settled first, so the heading counts only what will run
		results[i] = r.RunAppAhead(ctx, m.Apps[i], nil)
	}
	win.Release()
	runRows := func(only func(run.Result) bool) {
		// the next apps download while the current one installs; installs stay one at a time, in order
		ahead := r.NewAhead(ctx, m.Apps)
		defer ahead.Close()
		want := func(j int) bool { return only == nil || only(results[j]) }
		for i, a := range m.Apps {
			if (only != nil && !only(results[i])) || (only == nil && i < nCurrent) {
				continue
			}
			if win.Stopping() {
				results[i] = run.Result{Status: run.NotRun, Message: "Not run"}
				win.Update(i, string(run.NotRun), "Not run", "")
				continue
			}
			ahead.Fill(i, want)
			results[i] = r.RunAppAhead(ctx, a, ahead.Take(i))
		}
	}
	count := func() (ok, bad int) {
		for _, res := range results {
			if res.OK() {
				ok++
			}
			if res.Bad() {
				bad++
			}
		}
		return
	}
	runRows(nil)
	ok, bad := count()
	win.Finish(ok, bad)
	logger.Printf("finished: %d worked, %d did not", ok, bad)
	// "Retry failed apps" runs only the rows that failed, with the same checks; the one anonymous count is sent when the window is done with
	for !*noWindow && bad > 0 {
		act := waitForAction(win)
		if act != "retry" {
			break
		}
		logger.Printf("retry: the failed apps")
		win.Reopen()
		runRows(func(res run.Result) bool { return res.Bad() })
		ok, bad = count()
		win.Finish(ok, bad)
		logger.Printf("finished again: %d worked, %d did not", ok, bad)
	}
	if *noReport {
		logger.Printf("count not sent: -no-report")
	} else if !win.SendReport() {
		logger.Printf("count not sent: Don't send was ticked")
	} else {
		report(*base, ok, bad, logger)
	}
	waitForClose(win, *noWindow)
	if bad > 0 {
		os.Exit(1)
	}
}

// elevateAndWait is the first (not elevated) copy on Windows: it asks Windows for permission, starts the elevated copy with the hand-off file and the
// person's own folders, keeps its window on "Preparing" and moves it to the elevated copy's page. It returns the exit code for the process.
func elevateAndWait(logger *log.Logger, win *ui.Server, headless bool, logPath string, args []string) int {
	logger.Printf("not an administrator: asking Windows for elevation")
	if old, _ := filepath.Glob(filepath.Join(os.TempDir(), "fileion-installer-*.handoff")); len(old) > 0 { // left by a copy that died: it holds an address that is gone
		for _, f := range old {
			os.Remove(f)
		}
	}
	dl, pr, la := sys.UserDirs()
	hand := filepath.Join(os.TempDir(), fmt.Sprintf("fileion-installer-%d.handoff", time.Now().UnixNano()))
	wait, err := sys.RelaunchAsAdmin(append(append([]string{}, args...), "-handoff", hand, "-user-dirs", dl+"|"+pr+"|"+la))
	if err != nil {
		msg := "Administrator rights are needed to install apps, so nothing was installed. Run this file again and choose Yes at the permission prompt."
		logger.Printf("elevation failed: %v", err)
		win.Note(msg)
		win.Finish(0, 0)
		if headless {
			fmt.Printf("%s\nLog: %s\n", msg, logPath)
		}
		waitForClose(win, headless)
		return 1
	}
	var handed atomic.Bool
	go func() { // the elevated copy writes its window's address; the window moves there
		for !handed.Load() {
			if b, err := os.ReadFile(hand); err == nil && len(b) > 0 {
				win.Redirect(string(b))
				os.Remove(hand)
				handed.Store(true)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}()
	code, err := wait()
	logger.Printf("the elevated copy ended with code %d (%v)", code, err)
	if handed.Load() {
		return code // it ran the whole thing in its own window
	}
	handed.Store(true)
	win.Note("The installer stopped before it started. The log is at " + logPath)
	win.Finish(0, 0)
	waitForClose(win, headless)
	return 1
}

// waitForWindow says whether the window asked for its page within d (it did not when it was closed during the permission prompt).
func waitForWindow(win *ui.Server, d time.Duration) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if win.Polled() {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// waitForAction waits, with the run finished, for Close, "Retry failed apps" or a window that is gone.
func waitForAction(win *ui.Server) string {
	done := time.Now()
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-win.Closed:
			return "close"
		case <-win.Retried():
			return "retry"
		case <-t.C:
			if win.Abandoned(done) {
				return "abandoned"
			}
		}
	}
}

const helpURL = "https://fileion.com/installer-help"

// showBox is the Windows message box for a failure: the message, where the help page is, and a Yes to open it.
func showBox(msg string) {
	if sys.MessageBox("Fileion Installer", msg+"\n\nHelp: "+helpURL+"\n\nOpen the help page now?") {
		sys.OpenWindow(helpURL)
	}
}

// earlyFailure reports a failure from before the progress window exists, so it is never only a line on stderr (a double-clicked program's
// console closes the moment it exits): a message box on Windows, the Fileion window on Linux, and always stderr and the log.
func earlyFailure(logger *log.Logger, logPath *string, osName string, headless bool, msg string) {
	fmt.Fprintln(os.Stderr, msg)
	if logger != nil {
		logger.Printf("stopped: %s", msg)
	}
	if headless {
		return
	}
	if osName == "windows" {
		showBox(msg)
		return
	}
	path := filepath.Join(os.TempDir(), "fileion-installer.log")
	if logPath != nil {
		path = *logPath
	}
	win, url, err := ui.New(path, false)
	if err != nil {
		return
	}
	if logger != nil {
		logger.Printf("window: %s", url)
	}
	win.NoCode(msg)
	if err := sys.OpenWindow(url); err != nil {
		return
	}
	waitForClose(win, false)
}

// report sends the one anonymous count of the run: how many apps worked and how many did not. Nothing else leaves.
func report(base string, ok, bad int, logger *log.Logger) {
	body, _ := json.Marshal(map[string]int{"ok": ok, "failed": bad})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/setup/report", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if res, err := http.DefaultClient.Do(req); err != nil {
		logger.Printf("count not sent: %v", err)
	} else {
		res.Body.Close()
	}
}

func waitForClose(win *ui.Server, headless bool) {
	if headless {
		return
	}
	done := time.Now()
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-win.Closed:
			return
		case <-t.C:
			if win.Abandoned(done) {
				return
			}
		}
	}
}
