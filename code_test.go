package main

import (
	"os"
	"path/filepath"
	"testing"

	"fileion-installer/internal/stamp"
)

func file(t *testing.T, name, code string) string {
	p := filepath.Join(t.TempDir(), name)
	b := []byte("MZ program")
	if code != "" {
		tr, _ := stamp.Encode(code)
		b = append(b, tr...)
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTheStampBeatsTheFileNameAndASpecialNameWorks(t *testing.T) {
	p := file(t, "My special bundle.exe", "3fa91c0b")
	if c, src := resolveCode("", []string{p}); c != "3fa91c0b" || src != "stamp" {
		t.Errorf("renamed + stamped: %q from %s", c, src)
	}
	p = file(t, "Fileion-VLC-aaaaaaaa.exe", "3fa91c0b") // the name says one thing, the stamp another: the stamp wins
	if c, src := resolveCode("", []string{p}); c != "3fa91c0b" || src != "stamp" {
		t.Errorf("stamp vs name: %q from %s", c, src)
	}
}

func TestWithoutAStampTheFileNameStillWorks(t *testing.T) {
	p := file(t, "Fileion-VLC-7-Zip-and-4-more-3fa91c0b.exe", "")
	if c, src := resolveCode("", []string{p}); c != "3fa91c0b" || src != "file name" {
		t.Errorf("name only: %q from %s", c, src)
	}
}

func TestACorruptStampFallsBackToTheNameThenToNothing(t *testing.T) {
	p := file(t, "Fileion-VLC-3fa91c0b.exe", "")
	os.WriteFile(p, append([]byte("MZ"), make([]byte, 40)...), 0o600) // no valid trailer
	if c, src := resolveCode("", []string{p}); c != "3fa91c0b" || src != "file name" {
		t.Errorf("no stamp, name: %q from %s", c, src)
	}
	q := file(t, "Fileion-Installer.exe", "")
	if c, src := resolveCode("", []string{q}); c != "" || src != "" {
		t.Errorf("nothing anywhere: %q from %s", c, src)
	}
}

func TestTheFlagWinsAndAnAppImagePathComesFirst(t *testing.T) {
	p := file(t, "x.exe", "3fa91c0b")
	if c, src := resolveCode("deadbeef", []string{p}); c != "deadbeef" || src != "flag" {
		t.Errorf("flag: %q from %s", c, src)
	}
	appImage := file(t, "Fileion-A-0123456789.AppImage", "")
	inner := file(t, "fileion-installer", "")
	if c, src := resolveCode("", []string{appImage, inner}); c != "0123456789" || src != "file name" {
		t.Errorf("the AppImage's own name: %q from %s", c, src)
	}
	stamped := file(t, "App.AppImage", "abcdef0123")
	if c, _ := resolveCode("", []string{stamped, inner}); c != "abcdef0123" {
		t.Errorf("a stamped AppImage: %q", c)
	}
}
