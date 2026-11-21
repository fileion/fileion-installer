// Command fakeapp is a stand-in installer for the Windows test workflow.
//
//	fakeapp /S /CODE={id} /VERSION=1.0   writes an Uninstall registry entry, like a real silent install
//	fakeapp /S /CODE={id} /NAME=text /PUBLISHER=text /VERSION=2.0   the same with its own DisplayName and Publisher
//	fakeapp /HANG                        never returns, like an installer waiting on a hidden window
//
// An "a few clicks" app is started with no switch at all, so what it does comes from the NAME of the copy of this file (the test server serves copies):
//
//	int-ok.exe     like a vendor setup that starts the real setup and exits at once: the child waits for the marker file C:\ftest\marker-ok (the person
//	               finishing) and then writes the Uninstall entry {FILEION-INT-OK}
//	int-closed.exe exits at once without installing (the person closed the setup window)
//	int-hang.exe   never returns
//	int-linger.exe installs ({FILEION-INT-LINGER}), then launches "the app" (itself with /LINGER, which just stays open for ten minutes) and exits: a
//	               setup that ends with "Launch the app", so its job never empties
//	run-marker.exe writes C:\ftest\run-marker.txt: "high" when it runs elevated, "medium" when it does not (a portable app started with Run must be medium)
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	switch exe := strings.ToLower(filepath.Base(os.Args[0])); {
	case strings.HasPrefix(exe, "int-closed"):
		return
	case strings.HasPrefix(exe, "int-hang"):
		time.Sleep(time.Hour)
	case strings.HasPrefix(exe, "run-marker"):
		out, _ := exec.Command("whoami", "/groups").Output()
		level := "medium"
		if strings.Contains(string(out), "High Mandatory Level") {
			level = "high"
		}
		os.WriteFile(`C:\ftest\run-marker.txt`, []byte(level), 0o644)
		return
	case strings.HasPrefix(exe, "int-linger"):
		if len(os.Args) > 1 && os.Args[1] == "/LINGER" {
			time.Sleep(10 * time.Minute)
			return
		}
		os.Args = []string{os.Args[0], "/S", "/CODE={FILEION-INT-LINGER}", "/VERSION=1.0"}
		defer func() { exec.Command(exe0(), "/LINGER").Start() }()
	case strings.HasPrefix(exe, "int-ok"):
		if len(os.Args) > 1 && os.Args[1] == "/CHILD" {
			for { // the person finishing the setup: the harness creates the marker
				if _, err := os.Stat(`C:\ftest\marker-ok`); err == nil {
					break
				}
				time.Sleep(200 * time.Millisecond)
			}
			os.Args = []string{os.Args[0], "/S", "/CODE={FILEION-INT-OK}", "/VERSION=1.0"}
			break
		}
		exec.Command(os.Args[0], "/CHILD").Start() // the real setup, started by the first one, which now ends
		return
	}
	code, version, name, publisher := "{FILEION-TEST-0000}", "1.0", "", ""
	for _, a := range os.Args[1:] {
		switch {
		case a == "/HANG":
			time.Sleep(time.Hour)
		case strings.HasPrefix(a, "/CODE="):
			code = strings.TrimPrefix(a, "/CODE=")
		case strings.HasPrefix(a, "/NAME="):
			name = strings.ReplaceAll(strings.TrimPrefix(a, "/NAME="), "_", " ") // no spaces in a silent switch: _ stands for one
		case strings.HasPrefix(a, "/PUBLISHER="):
			publisher = strings.ReplaceAll(strings.TrimPrefix(a, "/PUBLISHER="), "_", " ")
		case strings.HasPrefix(a, "/VERSION="):
			version = strings.TrimPrefix(a, "/VERSION=")
		}
	}
	key := `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\` + code
	if name == "" {
		name = "Fileion Test App " + code
	}
	entry := [][2]string{{"DisplayName", name}, {"DisplayVersion", version}}
	if publisher != "" {
		entry = append(entry, [2]string{"Publisher", publisher})
	}
	for _, kv := range entry {
		if err := exec.Command("reg", "add", key, "/v", kv[0], "/d", kv[1], "/f").Run(); err != nil {
			os.Exit(1)
		}
	}
}

func exe0() string { p, _ := os.Executable(); return p }
