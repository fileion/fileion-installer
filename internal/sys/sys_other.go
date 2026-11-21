//go:build !windows && !linux

package sys

import (
	"context"
	"os/exec"

	"fileion-installer/internal/manifest"
)

// Other systems (macOS) are not supported by the installer; these exist so the packages build and test anywhere.
func Installed(manifest.App) (string, bool) { return "", false }

func Exec(ctx context.Context, name string, args []string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

func IsAdmin() bool { return true }

func RelaunchAsAdmin([]string) error { return nil }

func OpenWindow(url string) error { return exec.Command("open", url).Start() }

// MessageBox is Windows-only: here it shows nothing and reports "no".
func MessageBox(title, text string) bool { return false }

// AttachParentConsole is Windows-only.
func AttachParentConsole() {}

func OpenFile(path string) error { return exec.Command("open", path).Start() }
