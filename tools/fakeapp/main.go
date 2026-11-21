// Command fakeapp is a stand-in installer for the Windows test workflow.
//
//	fakeapp /S /CODE={id} /VERSION=1.0   writes an Uninstall registry entry, like a real silent install
//	fakeapp /S /CODE={id} /NAME=text /PUBLISHER=text /VERSION=2.0   the same with its own DisplayName and Publisher
//	fakeapp /HANG                        never returns, like an installer waiting on a hidden window
package main

import (
	"os"
	"os/exec"
	"strings"
	"time"
)

func main() {
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
