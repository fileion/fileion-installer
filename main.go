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
	if osName != "windows" && osName != "linux" {
		fmt.Fprintln(os.Stderr, "Fileion Installer runs on Windows and Linux. On a Mac, use the command from software.fileion.com/setup.")
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
		noCode(logPath, logger, osName == "windows", *noWindow)
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
		fmt.Fprintln(os.Stderr, "Could not start:", err)
		os.Exit(1)
	}
	logger.Printf("window: %s", url)
	if !*noWindow {
		if err := sys.OpenWindow(url); err != nil {
			logger.Printf("could not open the window: %v", err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { <-win.Closed; cancel() }()

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
		win.Note(ui.Sanitize(err.Error()))
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
	r := &run.Runner{Client: client, TempDir: os.TempDir(), Installed: sys.Installed, Exec: sys.Exec,
		AppTimeout: *appTimeout, Log: logger.Printf,
		Progress: func(a manifest.App, s run.Status, msg string, link string) {
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

// noCode tells the person, in the window (a double-clicked file has no console, so a message on stderr would never be seen), that this
// file has no app list, and where to get one.
func noCode(logPath string, logger *log.Logger, windows, headless bool) {
	const msg = "This file has no app list. Get your installer from software.fileion.com/setup"
	fmt.Fprintln(os.Stderr, msg)
	win, url, err := ui.New(logPath, false)
	if err != nil {
		return
	}
	logger.Printf("window: %s", url)
	win.NoCode(msg)
	if !headless {
		if err := sys.OpenWindow(url); err != nil {
			logger.Printf("could not open the window: %v", err)
		}
	}
	waitForClose(win, headless)
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
