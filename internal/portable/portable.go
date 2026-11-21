// Package portable keeps the apps that need no installer: a portable program saved in a folder of its own, and a zip unpacked into one.
// It holds the small list the installer keeps on the PC (%LOCALAPPDATA%\Fileion\portable.json: app, version, folder), so a re-run can skip or
// update them, and the file work (a zip that cannot write outside its folder, a move across drives). It talks to nobody: the list never leaves the PC.
package portable

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Entry is one portable or zip app the installer put on this PC.
type Entry struct {
	WingetID  string `json:"winget_id"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Folder    string `json:"folder"`
	Exe       string `json:"exe,omitempty"` // the program to run (full path); empty for a zip whose program is not known
	StartMenu bool   `json:"start_menu,omitempty"`
}

// Store is the list in a JSON file.
type Store struct {
	Path string
	mu   sync.Mutex
}

func (s *Store) load() []Entry {
	var f struct {
		Apps []Entry `json:"apps"`
	}
	if b, err := os.ReadFile(s.Path); err == nil {
		_ = json.Unmarshal(b, &f) // an unreadable list is an empty one: the app is simply fetched again
	}
	return f.Apps
}

// Find returns the entry of an app, but only while its folder still exists (the person may have deleted it).
func (s *Store) Find(wingetID string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.load() {
		if e.WingetID == wingetID {
			if st, err := os.Stat(e.Folder); err == nil && st.IsDir() {
				return e, true
			}
		}
	}
	return Entry{}, false
}

// Put adds or replaces the entry of an app.
func (s *Store) Put(e Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	apps, found := s.load(), false
	for i := range apps {
		if apps[i].WingetID == e.WingetID {
			apps[i], found = e, true
		}
	}
	if !found {
		apps = append(apps, e)
	}
	b, _ := json.MarshalIndent(map[string]any{"apps": apps}, "", "  ")
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}

// SafeName turns an app name into one folder name: no path characters, no trailing dots or spaces, never empty.
func SafeName(name string) string {
	n := strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return -1
		}
		return r
	}, name)
	n = strings.TrimRight(strings.TrimSpace(n), ". ")
	if n == "" || n == ".." {
		return "App"
	}
	return n
}

// MaxUnpacked is the most a zip may unpack to (a zip bomb stops here, not on a full disk).
const MaxUnpacked = 8 << 30

// Unzip unpacks a zip into dest. An entry that would land outside dest (a name with ".." or a drive) fails the whole unpacking.
func Unzip(zipPath, dest string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("the zip cannot be read: %w", err)
	}
	defer zr.Close()
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	root, _ := filepath.Abs(dest)
	var total int64
	for _, f := range zr.File {
		name := filepath.FromSlash(f.Name)
		target := filepath.Join(root, name)
		if rel, err := filepath.Rel(root, target); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(name) || filepath.VolumeName(name) != "" {
			return fmt.Errorf("the zip has an entry outside its folder (%s)", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if f.Mode()&fs.ModeSymlink != 0 {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		n, err := extract(f, target, MaxUnpacked-total)
		if err != nil {
			return err
		}
		total += n
	}
	return nil
}

func extract(f *zip.File, target string, room int64) (int64, error) {
	rc, err := f.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, io.LimitReader(rc, room+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > room {
		err = errors.New("the zip unpacks to more than the allowed size")
	}
	return n, err
}

// FindExe is the program to run inside an unpacked zip: the manifest's relative path when it names a file inside dir, else the only .exe
// within two folders. "" when there is none or several (the window then offers the folder only).
func FindExe(dir, hint string) string {
	if h := strings.TrimSpace(hint); h != "" {
		p := filepath.Join(dir, filepath.FromSlash(strings.ReplaceAll(h, `\`, "/")))
		if rel, err := filepath.Rel(dir, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p
			}
		}
	}
	var found []string
	root := strings.Count(dir, string(filepath.Separator))
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && strings.Count(p, string(filepath.Separator))-root > 2 {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.EqualFold(filepath.Ext(p), ".exe") {
			found = append(found, p)
		}
		return nil
	})
	if len(found) == 1 {
		return found[0]
	}
	return ""
}

// IsLink says whether a path is a symlink or a junction (or any other reparse point Go does not treat as a plain file or folder). The installer runs
// as administrator, so it never writes through one: a link under Fileion Apps could send an elevated write anywhere.
func IsLink(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0
}

// Move puts a folder somewhere else: a rename when it can, else a copy and then removing the old one (a move to another drive).
func Move(src, dst string) error {
	if rel, err := filepath.Rel(src, dst); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("a folder cannot be moved into itself")
	}
	if IsLink(src) || IsLink(filepath.Dir(dst)) {
		return errors.New("that folder is a link, so nothing was moved")
	}
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("%s already exists", dst)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		t := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(t, 0o755)
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(t, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
	if err != nil {
		os.RemoveAll(dst)
		return err
	}
	return os.RemoveAll(src)
}

// MoveFile moves one file (the temp download into the app's folder), across drives too.
func MoveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		in.Close()
		return err
	}
	_, err = io.Copy(out, in)
	in.Close()
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst)
		return err
	}
	return os.Remove(src)
}
