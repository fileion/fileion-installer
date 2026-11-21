package portable

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func makeZip(t *testing.T, files map[string]string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "a.zip")
	f, _ := os.Create(p)
	w := zip.NewWriter(f)
	for n, c := range files {
		e, _ := w.Create(n)
		e.Write([]byte(c))
	}
	w.Close()
	f.Close()
	return p
}

func TestUnzipUnpacksAndRefusesAnEntryOutsideItsFolder(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "App")
	if err := Unzip(makeZip(t, map[string]string{"bin/tool.exe": "x", "readme.txt": "y"}), dest); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "bin", "tool.exe")); string(b) != "x" {
		t.Fatalf("not unpacked: %q", b)
	}
	evil := filepath.Join(t.TempDir(), "evil")
	if err := Unzip(makeZip(t, map[string]string{"../escape.txt": "z"}), evil); err == nil {
		t.Fatal("an entry with .. was accepted")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(evil), "escape.txt")); err == nil {
		t.Fatal("a file was written outside the folder")
	}
}

func TestFindExeUsesTheHintElseTheOnlyExe(t *testing.T) {
	d := t.TempDir()
	os.MkdirAll(filepath.Join(d, "bin"), 0o755)
	os.WriteFile(filepath.Join(d, "bin", "tool.exe"), nil, 0o755)
	if got := FindExe(d, ""); got != filepath.Join(d, "bin", "tool.exe") {
		t.Fatalf("only exe: %q", got)
	}
	os.WriteFile(filepath.Join(d, "other.exe"), nil, 0o755)
	if got := FindExe(d, ""); got != "" {
		t.Fatalf("two exes must give none, got %q", got)
	}
	if got := FindExe(d, `bin\tool.exe`); got != filepath.Join(d, "bin", "tool.exe") {
		t.Fatalf("hint: %q", got)
	}
	if got := FindExe(d, `..\..\etc\x.exe`); got != "" {
		t.Fatalf("a hint outside the folder was used: %q", got)
	}
}

func TestStoreFindsOnlyWhileTheFolderExistsAndSafeNameHasNoPathCharacters(t *testing.T) {
	d := t.TempDir()
	s := &Store{Path: filepath.Join(d, "x", "portable.json")}
	folder := filepath.Join(d, "Rufus")
	os.MkdirAll(folder, 0o755)
	if err := s.Put(Entry{WingetID: "Pete.Rufus", Name: "Rufus", Version: "4.5", Folder: folder}); err != nil {
		t.Fatal(err)
	}
	if e, ok := s.Find("Pete.Rufus"); !ok || e.Version != "4.5" {
		t.Fatalf("not found: %v %v", e, ok)
	}
	s.Put(Entry{WingetID: "Pete.Rufus", Version: "4.6", Folder: folder})
	if e, _ := s.Find("Pete.Rufus"); e.Version != "4.6" {
		t.Fatal("not replaced")
	}
	os.RemoveAll(folder)
	if _, ok := s.Find("Pete.Rufus"); ok {
		t.Fatal("found an app whose folder is gone")
	}
	if SafeName(`a/b\c:d. `) != "abcd" || SafeName("..") != "App" || SafeName("VLC media player") != "VLC media player" {
		t.Fatal("SafeName")
	}
}

func TestMoveAndMoveFile(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "src")
	os.MkdirAll(filepath.Join(src, "sub"), 0o755)
	os.WriteFile(filepath.Join(src, "sub", "f.txt"), []byte("1"), 0o644)
	dst := filepath.Join(d, "to", "App")
	if err := Move(src, dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "sub", "f.txt")); string(b) != "1" {
		t.Fatal("not moved")
	}
	if Move(dst, dst) == nil {
		t.Fatal("moving onto an existing folder must fail")
	}
	a, b := filepath.Join(d, "a"), filepath.Join(d, "b")
	os.WriteFile(a, []byte("z"), 0o644)
	if err := MoveFile(a, b); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a); err == nil {
		t.Fatal("source left")
	}
}
