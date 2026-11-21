package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const keepLogs = 5

// newLogPath is this run's log file: %LOCALAPPDATA%\Fileion\Logs\installer-<time>.log of the signed-in person (next to portable.json), the newest few kept.
// FILEION_INSTALLER_LOG_IN_TEMP=1 (the test runner) puts it in the temp folder as fileion-installer.log, where the checks read it.
func newLogPath(localAppData string) string {
	if os.Getenv("FILEION_INSTALLER_LOG_IN_TEMP") == "1" {
		return filepath.Join(os.TempDir(), "fileion-installer.log")
	}
	dir := filepath.Join(localAppData, "Fileion", "Logs")
	if os.MkdirAll(dir, 0o700) != nil {
		return filepath.Join(os.TempDir(), "fileion-installer.log")
	}
	pruneLogs(dir, keepLogs-1) // room for the new one
	return filepath.Join(dir, "installer-"+time.Now().Format("20060102-150405")+".log")
}

// pruneLogs deletes all but the newest keep log files of the folder.
func pruneLogs(dir string, keep int) {
	files, _ := filepath.Glob(filepath.Join(dir, "installer-*.log"))
	sort.Strings(files) // the name holds the time, so name order is age order
	for i := 0; i < len(files)-keep; i++ {
		if strings.HasSuffix(files[i], ".log") {
			os.Remove(files[i])
		}
	}
}
