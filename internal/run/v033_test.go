package run

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"fileion-installer/internal/manifest"
	"fileion-installer/internal/portable"
)

// A portable app that is already saved is skipped (Up to date) and nothing is downloaded, but its row still gets Run, Add to Start menu and Move
// (the runner tells the window where it is, as after a fresh save).
func TestASkippedPortableRowStillGetsItsButtons(t *testing.T) {
	d := t.TempDir()
	folder := filepath.Join(d, "Rufus")
	os.MkdirAll(folder, 0o755)
	st := &portable.Store{Path: filepath.Join(d, "portable.json")}
	st.Put(portable.Entry{WingetID: "Pete.Rufus", Name: "Rufus", Version: "4.5", Folder: folder, Exe: filepath.Join(folder, "rufus.exe")})
	var gotFolder, gotExe string
	r := &Runner{Portable: st, Placed: func(a manifest.App, f, e string) { gotFolder, gotExe = f, e }}
	res := r.runAppWith(context.Background(), manifest.App{Name: "Rufus", WingetID: "Pete.Rufus", Version: "4.5", Mode: manifest.ModePortable, InstallerType: "portable", URL: "https://127.0.0.1:1/never"}, nil)
	if res.Status != UpToDate {
		t.Fatalf("%v", res)
	}
	if gotFolder != folder || gotExe != filepath.Join(folder, "rufus.exe") {
		t.Fatalf("the row was not given its buttons: %q %q", gotFolder, gotExe)
	}
}
