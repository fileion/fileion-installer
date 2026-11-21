//go:build linux

// Package sys holds what differs per operating system: asking whether an app is installed, running programs, opening the window.
package sys

import (
	"context"
	"os/exec"

	"fileion-installer/internal/manifest"
)

// Installed asks Flatpak whether the app is there. Flatpak versions are commits, so the version is not compared: an
// installed app is "updated if needed" by flatpak itself.
func Installed(a manifest.App) (string, bool) {
	if a.FlatpakID == "" {
		return "", false
	}
	return "", exec.Command("flatpak", "info", a.FlatpakID).Run() == nil
}

// Exec runs a program and waits for it.
func Exec(ctx context.Context, name string, args []string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

// IsAdmin is true: Flatpak asks for what it needs itself.
func IsAdmin() bool { return true }

// RelaunchAsAdmin is never needed on Linux.
func RelaunchAsAdmin([]string) error { return nil }

// OpenWindow shows the progress page in its own browser window (Chromium-based apps mode when there is one), else the default browser.
func OpenWindow(url string) error {
	for _, b := range []string{"chromium", "chromium-browser", "google-chrome", "microsoft-edge", "brave-browser"} {
		if p, err := exec.LookPath(b); err == nil {
			return exec.Command(p, "--app="+url, "--window-size=520,560").Start()
		}
	}
	return exec.Command("xdg-open", url).Start()
}

// MessageBox is Windows-only: here it shows nothing and reports "no".
func MessageBox(title, text string) bool { return false }

// AttachParentConsole is Windows-only.
func AttachParentConsole() {}

// OpenFile opens a file with the desktop's default program.
func OpenFile(path string) error { return exec.Command("xdg-open", path).Start() }
