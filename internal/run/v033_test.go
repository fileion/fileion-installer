package run

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// Saved in its folder: current, and the line says where. Folder gone: not current, so it is downloaded again as a new app.
func TestASavedPortableAppIsCurrentOnlyWhileItsFolderExists(t *testing.T) {
	d := t.TempDir()
	folder := filepath.Join(d, "Rufus")
	os.MkdirAll(folder, 0o755)
	st := &portable.Store{Path: filepath.Join(d, "portable.json")}
	st.Put(portable.Entry{WingetID: "Pete.Rufus", Name: "Rufus", Version: "4.5", Folder: folder, Exe: filepath.Join(folder, "rufus.exe")})
	r := &Runner{Portable: st}
	a := manifest.App{Name: "Rufus", WingetID: "Pete.Rufus", Version: "4.5", Mode: manifest.ModePortable, InstallerType: "portable"}
	if !r.Current(context.Background(), a) {
		t.Fatal("saved and its folder is there: current")
	}
	if res, _ := r.pre(context.Background(), a); !strings.HasPrefix(res.Message, "Saved in ") {
		t.Fatalf("the line must say Saved in <folder>: %q", res.Message)
	}
	os.RemoveAll(folder)
	if r.Current(context.Background(), a) {
		t.Fatal("folder gone: must be downloaded again")
	}
}
