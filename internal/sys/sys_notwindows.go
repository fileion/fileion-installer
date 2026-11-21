//go:build !windows

package sys

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
)

// What only Windows has (portable apps, interactive setups, the taskbar): here these exist so the packages build and test anywhere.

func ExecJob(ctx context.Context, name string, args []string, leave <-chan struct{}) error {
	return Exec(ctx, name, args)
}

func FrontWindow() {}

func RunAsUser(path string) error { return exec.Command("xdg-open", path).Start() }

func OpenFolder(path string) error { return exec.Command("xdg-open", path).Start() }

func PickFolder(title string) (string, error) { return "", errors.New("no folder picker here") }

func CreateShortcut(lnk, target, workDir string) error { return errors.New("no Start menu here") }

func UserDirs() (downloads, programs, localAppData string) {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "Downloads"), "", filepath.Join(h, ".local", "share")
}

// OpenURL opens an address in the default browser.
func OpenURL(raw string) error { return exec.Command("xdg-open", raw).Start() }

// OpenFolderSelect opens the folder of a file.
func OpenFolderSelect(path string) error { return OpenFolder(filepath.Dir(path)) }

// CenterLoop is Windows-only.
func CenterLoop(stop <-chan struct{}) {}
