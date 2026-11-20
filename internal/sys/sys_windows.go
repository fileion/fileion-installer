//go:build windows

// Package sys holds what differs per operating system: asking whether an app is installed, running programs, opening the window.
package sys

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"fileion-installer/internal/manifest"
	"fileion-installer/internal/match"
)

const (
	keyWow64_64 = 0x0100
	keyWow64_32 = 0x0200
	uninstall   = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\`
)

// Installed says whether the app is installed and at which version. First its product code is looked up as an Uninstall key name (64-bit,
// 32-bit and the user's own hive); when that is not there, or the app has none, its name rule (manifest.Match: DisplayName starts with
// the rule and Publisher is equal) is tried over every Uninstall entry. An app with neither counts as not installed.
func Installed(a manifest.App) (string, bool) {
	if code := strings.TrimSpace(a.ProductCode); code != "" {
		for _, root := range []syscall.Handle{syscall.HKEY_LOCAL_MACHINE, syscall.HKEY_CURRENT_USER} {
			for _, wow := range []uint32{keyWow64_64, keyWow64_32} {
				if v, ok := displayVersion(root, uninstall+code, wow); ok {
					return v, true
				}
			}
		}
	}
	if a.Match != nil {
		return match.Find(uninstallEntries(), *a.Match)
	}
	return "", false
}

// uninstallEntries reads every Uninstall entry's DisplayName, Publisher and DisplayVersion from HKLM 64-bit, HKLM 32-bit and HKCU.
func uninstallEntries() []match.Entry {
	var out []match.Entry
	path, _ := syscall.UTF16PtrFromString(strings.TrimSuffix(uninstall, `\`))
	for _, root := range []syscall.Handle{syscall.HKEY_LOCAL_MACHINE, syscall.HKEY_CURRENT_USER} {
		for _, wow := range []uint32{keyWow64_64, keyWow64_32} {
			var parent syscall.Handle
			if syscall.RegOpenKeyEx(root, path, 0, syscall.KEY_READ|wow, &parent) != nil {
				continue
			}
			for i := uint32(0); ; i++ {
				name := make([]uint16, 256)
				n := uint32(len(name))
				if err := syscall.RegEnumKeyEx(parent, i, &name[0], &n, nil, nil, nil, nil); err != nil {
					if err == syscall.ERROR_MORE_DATA {
						continue
					}
					break
				}
				var key syscall.Handle
				sub, _ := syscall.UTF16PtrFromString(strings.TrimSuffix(uninstall, `\`) + `\` + string(utf16.Decode(name[:n])))
				if syscall.RegOpenKeyEx(root, sub, 0, syscall.KEY_READ|wow, &key) != nil {
					continue
				}
				out = append(out, match.Entry{Name: regString(key, "DisplayName"), Publisher: regString(key, "Publisher"), Version: regString(key, "DisplayVersion")})
				syscall.RegCloseKey(key)
			}
			syscall.RegCloseKey(parent)
		}
	}
	return out
}

// regString reads one string value of an open key ("" when it is missing or not a string).
func regString(key syscall.Handle, value string) string {
	name, _ := syscall.UTF16PtrFromString(value)
	buf := make([]uint16, 512)
	size := uint32(len(buf) * 2)
	var typ uint32
	if err := syscall.RegQueryValueEx(key, name, nil, &typ, (*byte)(unsafe.Pointer(&buf[0])), &size); err != nil || (typ != syscall.REG_SZ && typ != syscall.REG_EXPAND_SZ) {
		return ""
	}
	return strings.TrimRight(string(utf16.Decode(buf[:size/2])), "\x00")
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
	return regString(key, "DisplayVersion"), true // installed; a version that cannot be read is "" and means update, not skip
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
			return exec.Command(edge, "--app="+url, "--window-size=520,560").Start()
		}
	}
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

var (
	user32           = syscall.NewLazyDLL("user32.dll")
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	messageBoxW      = user32.NewProc("MessageBoxW")
	getConsoleList   = kernel32.NewProc("GetConsoleProcessList")
	freeConsole      = kernel32.NewProc("FreeConsole")
	mbYesNoIconError = uintptr(0x4 | 0x10) // MB_YESNO | MB_ICONERROR
	idYes            = uintptr(6)
)

// MessageBox shows a Windows message box (no new package: user32 through syscall) and says whether the person chose Yes. An early failure
// must never be only a line on stderr: a double-clicked console program closes its window the moment it exits.
func MessageBox(title, text string) bool {
	t, _ := syscall.UTF16PtrFromString(title)
	m, _ := syscall.UTF16PtrFromString(text)
	r, _, _ := messageBoxW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), mbYesNoIconError)
	return r == idYes
}

// HideOwnConsole closes the console window when this program is the only process in it, which is what a double click in Explorer
// gives (run from Command Prompt or PowerShell the shell shares the console, so it stays and output is still shown).
func HideOwnConsole() {
	var pids [4]uint32
	n, _, _ := getConsoleList.Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	if n == 1 {
		freeConsole.Call()
	}
}

// OpenFile opens a file in the person's own program (Notepad for a log), so the window can show the log file itself, not a localhost address.
func OpenFile(path string) error {
	return exec.Command("notepad.exe", path).Start()
}
