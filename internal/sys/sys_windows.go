//go:build windows

// Package sys holds what differs per operating system: asking whether an app is installed, running programs, opening the window.
package sys

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"fileion-installer/internal/manifest"
)

const (
	keyWow64_64 = 0x0100
	keyWow64_32 = 0x0200
	uninstall   = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\`
)

// Installed looks for the app's product code under the Uninstall keys (64-bit, 32-bit and the user's own) and reads its
// DisplayVersion. An app without a product code cannot be told, so it counts as not installed and is installed again.
func Installed(a manifest.App) (string, bool) {
	if strings.TrimSpace(a.ProductCode) == "" {
		return "", false
	}
	for _, root := range []syscall.Handle{syscall.HKEY_LOCAL_MACHINE, syscall.HKEY_CURRENT_USER} {
		for _, wow := range []uint32{keyWow64_64, keyWow64_32} {
			if v, ok := displayVersion(root, uninstall+a.ProductCode, wow); ok {
				return v, true
			}
		}
	}
	return "", false
}

func displayVersion(root syscall.Handle, path string, wow uint32) (string, bool) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return "", false
	}
	var key syscall.Handle
	if err := syscall.RegOpenKeyEx(root, p, 0, syscall.KEY_READ|wow, &key); err != nil {
		return "", false
	}
	defer syscall.RegCloseKey(key)
	name, _ := syscall.UTF16PtrFromString("DisplayVersion")
	buf := make([]uint16, 128)
	size := uint32(len(buf) * 2)
	var typ uint32
	if err := syscall.RegQueryValueEx(key, name, nil, &typ, (*byte)(unsafe.Pointer(&buf[0])), &size); err != nil {
		return "", true // installed, version unreadable: counts as installed with an unknown version
	}
	return strings.TrimRight(string(utf16.Decode(buf[:size/2])), "\x00"), true
}

// Exec runs a program without a console window and waits for it.
func Exec(ctx context.Context, name string, args []string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	// On the time limit, end the installer and everything it started (a setup window is a child process)
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	}
	cmd.WaitDelay = 5 * time.Second
	return cmd.Run()
}

var (
	advapi32          = syscall.NewLazyDLL("advapi32.dll")
	shell32           = syscall.NewLazyDLL("shell32.dll")
	getTokenInfo      = advapi32.NewProc("GetTokenInformation")
	shellExecuteW     = shell32.NewProc("ShellExecuteW")
	tokenElevation    = 20
	errNoElevationAPI = syscall.EINVAL
)

// IsAdmin says whether this process already runs with administrator rights.
func IsAdmin() bool {
	var tok syscall.Token
	h, _ := syscall.GetCurrentProcess()
	if err := syscall.OpenProcessToken(h, syscall.TOKEN_QUERY, &tok); err != nil {
		return false
	}
	defer tok.Close()
	var elevated, n uint32
	r, _, _ := getTokenInfo.Call(uintptr(tok), uintptr(tokenElevation), uintptr(unsafe.Pointer(&elevated)), unsafe.Sizeof(elevated), uintptr(unsafe.Pointer(&n)))
	return r != 0 && elevated != 0
}

// RelaunchAsAdmin starts this program again through the Windows permission prompt ("runas") and returns; the caller then exits.
func RelaunchAsAdmin(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	verb, _ := syscall.UTF16PtrFromString("runas")
	file, _ := syscall.UTF16PtrFromString(exe)
	params, _ := syscall.UTF16PtrFromString(strings.Join(args, " "))
	r, _, _ := shellExecuteW.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(file)), uintptr(unsafe.Pointer(params)), 0, 1)
	if r <= 32 {
		return errNoElevationAPI
	}
	return nil
}

// OpenWindow shows the progress page in its own Edge window (Edge ships with Windows 10 and 11); any other browser is the fallback.
func OpenWindow(url string) error {
	for _, edge := range []string{os.Getenv("ProgramFiles(x86)") + `\Microsoft\Edge\Application\msedge.exe`, os.Getenv("ProgramFiles") + `\Microsoft\Edge\Application\msedge.exe`} {
		if _, err := os.Stat(edge); err == nil {
			return exec.Command(edge, "--app="+url, "--window-size=560,760").Start()
		}
	}
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}
