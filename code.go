package main

import (
	"os"

	"fileion-installer/internal/manifest"
	"fileion-installer/internal/stamp"
)

// selfPaths are the files that can carry this installer's set code: inside an AppImage the runtime sets $APPIMAGE to the AppImage
// file (os.Executable() there is the unpacked copy), so it comes first.
func selfPaths() []string {
	var paths []string
	if p := os.Getenv("APPIMAGE"); p != "" {
		paths = append(paths, p)
	}
	if p, err := os.Executable(); err == nil {
		paths = append(paths, p)
	}
	return paths
}

// resolveCode finds the set code and says where it came from: the -code flag, the stamp inside the file, the file name, or "" (none).
func resolveCode(flagCode string, paths []string) (code, source string) {
	if flagCode != "" {
		return flagCode, "flag"
	}
	for _, p := range paths {
		if c := stamp.Read(p); c != "" {
			return c, "stamp"
		}
	}
	for _, p := range paths {
		if c := manifest.CodeFromFileName(p); c != "" {
			return c, "file name"
		}
	}
	return "", ""
}
