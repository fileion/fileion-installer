package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"fileion-installer/internal/manifest"
	"fileion-installer/internal/portable"
	"fileion-installer/internal/sys"
	"fileion-installer/internal/ui"
)

// portableFiles holds where portable apps and zips live and what the buttons of a saved row do (Run, Open folder, Add to Start menu, Move…).
type portableFiles struct {
	appsDir, startDir string
	store             *portable.Store
	logger            *log.Logger

	mu    sync.Mutex
	known map[int]portable.Entry // row index -> where its app is now
}

// newPortableFiles takes the person's own folders from -user-dirs (the first copy of the installer knows them even when an administrator's name is
// used for elevation), else from Windows for this process.
func newPortableFiles(userDirs string, logger *log.Logger) *portableFiles {
	dl, pr, la := sys.UserDirs()
	dirs := []*string{&dl, &pr, &la}
	if parts := strings.Split(userDirs, "|"); len(parts) == 3 {
		for i, p := range parts {
			if p != "" {
				*dirs[i] = p
			}
		}
	}
	if dl == "" {
		h, _ := os.UserHomeDir()
		dl = filepath.Join(h, "Downloads")
	}
	if la == "" {
		la = os.TempDir()
	}
	return &portableFiles{appsDir: filepath.Join(dl, "Fileion Apps"), startDir: filepath.Join(pr, "Fileion Apps"),
		store: &portable.Store{Path: filepath.Join(la, "Fileion", "portable.json")}, logger: logger, known: map[int]portable.Entry{}}
}

// placed is told by the runner when a portable app or a zip is in its folder: the row gets its buttons.
func (p *portableFiles) placed(win *ui.Server, i int, a manifest.App, folder, exe string) {
	p.mu.Lock()
	e, _ := p.store.Find(a.WingetID)
	if e.WingetID == "" {
		e = portable.Entry{WingetID: a.WingetID, Name: a.Name, Version: a.Version, Folder: folder, Exe: exe}
	}
	p.known[i] = e
	p.mu.Unlock()
	win.Mutate(i, func(r *ui.Row) { r.Folder, r.CanRun, r.InStart = folder, exe != "", e.StartMenu })
}

func (p *portableFiles) shortcut(e portable.Entry) string {
	return filepath.Join(p.startDir, portable.SafeName(e.Name)+".lnk")
}

// rowAction is what the buttons of a saved row do. It never runs an installer: Run starts the saved program as the signed-in person (through Explorer),
// the others touch only the app's folder and a shortcut.
func (p *portableFiles) rowAction(win *ui.Server) func(i int, action string) {
	return func(i int, action string) {
		p.mu.Lock()
		e, ok := p.known[i]
		p.mu.Unlock()
		if !ok {
			return
		}
		var err error
		switch action {
		case "run":
			if e.Exe != "" {
				err = sys.RunAsUser(e.Exe)
			}
		case "open":
			err = sys.OpenFolder(e.Folder)
		case "start":
			if e.Exe != "" {
				if err = sys.CreateShortcut(p.shortcut(e), e.Exe, e.Folder); err == nil {
					e.StartMenu = true
					p.save(i, e)
					win.Mutate(i, func(r *ui.Row) { r.InStart = true })
				}
			}
		case "move":
			var dir string
			if dir, err = sys.PickFolder("Move " + e.Name + " to…"); err == nil && dir != "" {
				err = p.move(win, i, e, filepath.Join(dir, portable.SafeName(e.Name)))
			}
		}
		if err != nil {
			p.logger.Printf("%s: %s: %v", e.Name, action, err)
			win.Mutate(i, func(r *ui.Row) { r.Message = ui.Sanitize("Could not " + action + ": " + err.Error()) })
		}
	}
}

func (p *portableFiles) save(i int, e portable.Entry) {
	p.mu.Lock()
	p.known[i] = e
	p.mu.Unlock()
	if err := p.store.Put(e); err != nil {
		p.logger.Printf("%s: the portable list could not be saved: %v", e.Name, err)
	}
}

// move puts the app's folder in another place and keeps the list (and a Start menu shortcut) pointing at it.
func (p *portableFiles) move(win *ui.Server, i int, e portable.Entry, dst string) error {
	if err := portable.Move(e.Folder, dst); err != nil {
		return err
	}
	if e.Exe != "" {
		if rel, err := filepath.Rel(e.Folder, e.Exe); err == nil {
			e.Exe = filepath.Join(dst, rel)
		}
	}
	e.Folder = dst
	if e.StartMenu && e.Exe != "" {
		_ = sys.CreateShortcut(p.shortcut(e), e.Exe, e.Folder)
	}
	p.save(i, e)
	win.Mutate(i, func(r *ui.Row) { r.Folder, r.Message = dst, ui.Sanitize("Moved to "+dst) })
	return nil
}
