package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTheLogLivesInLocalAppDataAndOnlyTheNewestFewStay(t *testing.T) {
	t.Setenv("FILEION_INSTALLER_LOG_IN_TEMP", "")
	la := t.TempDir()
	dir := filepath.Join(la, "Fileion", "Logs")
	os.MkdirAll(dir, 0o700)
	for _, n := range []string{"20260101-000001", "20260101-000002", "20260101-000003", "20260101-000004", "20260101-000005", "20260101-000006"} {
		os.WriteFile(filepath.Join(dir, "installer-"+n+".log"), nil, 0o600)
	}
	os.WriteFile(filepath.Join(dir, "other.txt"), nil, 0o600)
	p := newLogPath(la)
	if filepath.Dir(p) != dir {
		t.Fatal(p)
	}
	left, _ := filepath.Glob(filepath.Join(dir, "installer-*.log"))
	if len(left) != keepLogs-1 {
		t.Fatalf("%v", left)
	}
	if _, err := os.Stat(filepath.Join(dir, "other.txt")); err != nil {
		t.Fatal("only installer logs are pruned")
	}
	t.Setenv("FILEION_INSTALLER_LOG_IN_TEMP", "1")
	if filepath.Base(newLogPath(la)) != "fileion-installer.log" {
		t.Fatal("the test runner's location")
	}
}
