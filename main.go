// Fileion Installer: reads the short code stamped in its own file (or, failing that, in its name), gets the app list from Fileion, and installs each app
// from its publisher's own download after checking the publisher's SHA-256. Run it again and it updates the same apps.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
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
	flag.Parse()

	osName := runtime.GOOS
	if osName == "windows" {
		sys.HideOwnConsole() // double-clicked: no black console window behind the Fileion window (from a terminal it stays)
	}
	if osName != "windows" && osName != "linux" {
		fmt.Fprintln(os.Stderr, "Fileion Installer runs on Windows and Linux. On a Mac, use the command from software.fileion.com/setup.")
		os.Exit(2)
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		earlyFailure(nil, nil, osName, *noWindow, "This computer's processor ("+runtime.GOARCH+") is not supported by the Fileion Installer. Use the install command from software.fileion.com/setup instead.")
		os.Exit(2)
	}
	logPath := filepath.Join(os.TempDir(), "fileion-installer.log")
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		lf = os.Stderr
	}
	defer lf.Close()
	logger := log.New(lf, "", log.LstdFlags)

	// The list's code: the -code flag, else stamped inside this file by Fileion (so the file can be renamed and copied), else in its name
	var source string
	*code, source = resolveCode(*code, selfPaths())
	if *code == "" {
		logger.Printf("start: version %s, %s: no set code in this file", version, osName)
		earlyFailure(logger, &logPath, osName, *noWindow, "This file has no app list. Get your installer from software.fileion.com/setup")
		os.Exit(2)
	}
	logger.Printf("start: version %s, code %s (from the %s), %s", version, *code, source, osName)

	// Windows: one permission prompt up front, so every app installs for everyone and none asks again
	needAdmin := osName == "windows" && !sys.IsAdmin()
	if needAdmin && os.Getenv("FILEION_INSTALLER_ELEVATED") != "1" {
		logger.Printf("not an administrator: asking Windows for elevation")
		os.Setenv("FILEION_INSTALLER_ELEVATED", "1")
		err := sys.RelaunchAsAdmin(os.Args[1:])
		if err == nil {
			return
		}
		logger.Printf("elevation failed: %v", err)
	}

	win, url, err := ui.New(logPath, osName == "windows")
	if err != nil {
		logger.Printf("could not start the window: %v", err)
		earlyFailure(logger, nil, osName, *noWindow, "The installer could not start its window: "+err.Error())
		os.Exit(1)
	}
	logger.Printf("window: %s", url)
	win.OpenLog = func() {
		if err := sys.OpenFile(logPath); err != nil {
			logger.Printf("could not open the log: %v", err)
		}
	}
	windowShown := *noWindow
	if !*noWindow {
		if err := sys.OpenWindow(url); err != nil {
			logger.Printf("could not open the window: %v", err)
		} else {
			windowShown = true
		}
	}
	// a failure is told in the window; when no window could be opened it is a message box (Windows) so it is never silent
	tell := func(msg string) {
		win.Note(ui.Sanitize(msg))
		if !windowShown && osName == "windows" {
			showBox(msg)
		}
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
		tell(err.Error())
		win.Finish(0, 0)
		waitForClose(win, *noWindow)
		os.Exit(1)
	}

	names := make([]string, len(m.Apps))
	for i, a := range m.Apps {
		names[i] = ui.Sanitize(a.Name)
	}
	win.SetApps(names)
	if len(m.Apps) == 0 {
		win.Note("None of the apps in this list can be installed unattended. Get them from their Fileion pages.")
	}

	index := map[int]int{}
	for i, a := range m.Apps {
		index[a.ID] = i
	}
	// What is going on, for the heartbeat line in the log: if the program ever dies, the last such line says what it was doing
	var cur atomic.Value
	cur.Store("starting")
	started := time.Now()
	go func() {
		for range time.Tick(30 * time.Second) {
			logger.Printf("alive: %v, %ds into the run", cur.Load(), int(time.Since(started).Seconds()))
		}
	}()
	r := &run.Runner{Client: client, TempDir: os.TempDir(), Installed: sys.Installed, Exec: sys.Exec,
		AppTimeout: *appTimeout, Log: logger.Printf,
		Transfer: func(a manifest.App, done, total int64) { win.Transfer(index[a.ID], done, total) },
		Progress: func(a manifest.App, s run.Status, msg string, link string) {
			cur.Store(a.Name + ": " + string(s))
			logger.Printf("%s: %s: %s", a.Name, s, msg)
			win.Update(index[a.ID], string(s), ui.Sanitize(msg), link)
			if *noWindow {
				fmt.Printf("%-30s %-12s %s\n", a.Name, s, msg)
			}
		}}

	ok, bad := 0, 0
	for _, a := range m.Apps {
		res := r.RunApp(ctx, a)
		if res.OK() {
			ok++
		}
		if res.Bad() {
			bad++
		}
	}
	win.Finish(ok, bad)
	logger.Printf("finished: %d worked, %d did not", ok, bad)
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
