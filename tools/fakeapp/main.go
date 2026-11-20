// Command fakeapp is a stand-in installer for the Windows test workflow.
//
//	fakeapp /S /CODE={id} /VERSION=1.0   writes an Uninstall registry entry, like a real silent install
//	fakeapp /HANG                        never returns, like an installer waiting on a hidden window
package main

import (
	"os"
	"os/exec"
	"strings"
	"time"
)

func main() {
	code, version := "{FILEION-TEST-0000}", "1.0"
	for _, a := range os.Args[1:] {
		switch {
		case a == "/HANG":
			time.Sleep(time.Hour)
		case strings.HasPrefix(a, "/CODE="):
			code = strings.TrimPrefix(a, "/CODE=")
		case strings.HasPrefix(a, "/VERSION="):
			version = strings.TrimPrefix(a, "/VERSION=")
		}
	}
	key := `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\` + code
	for _, kv := range [][2]string{{"DisplayName", "Fileion Test App " + code}, {"DisplayVersion", version}} {
		if err := exec.Command("reg", "add", key, "/v", kv[0], "/d", kv[1], "/f").Run(); err != nil {
			os.Exit(1)
		}
	}
}
