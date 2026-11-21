//go:build windows

// Package sys holds what differs per operating system: asking whether an app is installed, running programs, opening the window.
package sys

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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

// Exec runs a program and waits for it. No console window is made for it (CREATE_NO_WINDOW) but its own windows are NOT hidden: a silent
// installer shows none, and one that stops to ask something ("close the running app", a finish page) must be visible, not waiting on a
// dialog nobody can see.
func Exec(ctx context.Context, name string, args []string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
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

type shellExecuteInfo struct {
	cbSize         uint32
	fMask          uint32
	hwnd           uintptr
	lpVerb         *uint16
	lpFile         *uint16
	lpParameters   *uint16
	lpDirectory    *uint16
	nShow          int32
	hInstApp       uintptr
	lpIDList       uintptr
	lpClass        *uint16
	hkeyClass      uintptr
	dwHotKey       uint32
	hIconOrMonitor uintptr
	hProcess       syscall.Handle
}

var shellExecuteExW = shell32.NewProc("ShellExecuteExW")

// RelaunchAsAdmin starts this program again through the Windows permission prompt ("runas"). It returns at once with a function that waits for the
// elevated copy to end and gives its exit code; the error is set when the person said No at the prompt (or Windows could not ask).
func RelaunchAsAdmin(args []string) (func() (int, error), error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = syscall.EscapeArg(a)
	}
	verb, _ := syscall.UTF16PtrFromString("runas")
	file, _ := syscall.UTF16PtrFromString(exe)
	params, _ := syscall.UTF16PtrFromString(strings.Join(quoted, " "))
	info := shellExecuteInfo{fMask: 0x40 | 0x100, lpVerb: verb, lpFile: file, lpParameters: params, nShow: 1} // SEE_MASK_NOCLOSEPROCESS | SEE_MASK_NOASYNC
	info.cbSize = uint32(unsafe.Sizeof(info))
	if r, _, e := shellExecuteExW.Call(uintptr(unsafe.Pointer(&info))); r == 0 {
		return nil, e
	}
	h := info.hProcess
	return func() (int, error) {
		defer syscall.CloseHandle(h)
		if _, err := syscall.WaitForSingleObject(h, syscall.INFINITE); err != nil {
			return -1, err
		}
		var code uint32
		err := syscall.GetExitCodeProcess(h, &code)
		return int(code), err
	}, nil
}

// OpenWindow shows the progress page in its own Edge window (Edge ships with Windows 10 and 11); any other browser is the fallback.
func OpenWindow(url string) error {
	for _, edge := range []string{os.Getenv("ProgramFiles(x86)") + `\Microsoft\Edge\Application\msedge.exe`, os.Getenv("ProgramFiles") + `\Microsoft\Edge\Application\msedge.exe`} {
		if _, err := os.Stat(edge); err == nil {
			la := os.Getenv("LOCALAPPDATA")
			if la != "" {
				if what, err := SeedProfile(la); err != nil {
					Log("edge sign-in: method=profile prefs FAILED (%v): the window opens anyway", err)
				} else {
					Log("edge sign-in: method=profile prefs, wrote %s", what)
				}
			}
			args := EdgeArgs(url, la, 420, 260)
			Log("edge args: %s", strings.Join(args[1:], " "))
			return exec.Command(edge, args...).Start()
		}
	}
	Log("edge not found: the default browser opens the page")
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

// OpenURL opens a web address in the person's DEFAULT browser, never in the installer's own window, and as the signed-in person even when this
// process is elevated (an elevated process starting a browser would run it as administrator): ShellExecute when not elevated, Explorer (the
// person's own shell) when elevated.
func OpenURL(raw string) error {
	if IsAdmin() {
		Log("open link: explorer (elevated) %s", raw)
		return exec.Command("explorer.exe", raw).Start()
	}
	Log("open link: ShellExecute %s", raw)
	verb, _ := syscall.UTF16PtrFromString("open")
	file, _ := syscall.UTF16PtrFromString(raw)
	if r, _, e := shellExecuteW.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(file)), 0, 0, 1); r <= 32 {
		return e
	}
	return nil
}

// OpenFolderSelect opens Explorer on the folder of a file with that file selected.
func OpenFolderSelect(path string) error {
	return exec.Command("explorer.exe", "/select,"+path).Start()
}

type rect struct{ left, top, right, bottom int32 }

var (
	getWindowRect      = user32.NewProc("GetWindowRect")
	setWindowPos       = user32.NewProc("SetWindowPos")
	monitorFromWindow  = user32.NewProc("MonitorFromWindow")
	getMonitorInfoW    = user32.NewProc("GetMonitorInfoW")
	setProcessDPIAware = user32.NewProc("SetProcessDPIAware")
)

// windowHandle finds the installer's Edge window (see FrontWindow).
func windowHandle() uintptr {
	var found uintptr
	cb := syscall.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		if v, _, _ := isWindowVisible.Call(hwnd); v == 0 {
			return 1
		}
		buf := make([]uint16, 128)
		n, _, _ := getWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if !strings.HasPrefix(string(utf16.Decode(buf[:n])), windowTitlePrefix) {
			return 1
		}
		cn := make([]uint16, 64)
		m, _, _ := getClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&cn[0])), uintptr(len(cn)))
		if string(utf16.Decode(cn[:m])) != "Chrome_WidgetWin_1" {
			return 1
		}
		found = hwnd
		return 0
	})
	enumWindows.Call(cb, 0)
	return found
}

// CenterLoop keeps the window centred on the work area (the screen without the taskbar) of the monitor it is on, until stop is closed. It acts when the
// window's size changes (it grows from the start screen to the list) and only while the person has not moved it: once its top-left is not where this
// loop put it, it is left alone. The process is made DPI-aware first, so the window and the work area are in the same (real) pixels. Every move is logged.
func CenterLoop(stop <-chan struct{}) {
	setProcessDPIAware.Call()
	var lastX, lastY, lastW, lastH int32 = -1 << 30, -1 << 30, 0, 0
	moved := false
	t := time.NewTicker(300 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		hwnd := windowHandle()
		if hwnd == 0 || moved {
			continue
		}
		var r rect
		if ok, _, _ := getWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ok == 0 {
			continue
		}
		w, h := r.right-r.left, r.bottom-r.top
		if lastW != 0 && (r.left != lastX || r.top != lastY) {
			moved = true // the person moved it
			Log("window position: left alone, the person moved it (%d,%d)", r.left, r.top)
			continue
		}
		if w == lastW && h == lastH && lastW != 0 {
			continue
		}
		mon, _, _ := monitorFromWindow.Call(hwnd, 2) // MONITOR_DEFAULTTONEAREST
		var mi struct {
			size          uint32
			monitor, work rect
			flags         uint32
		}
		mi.size = uint32(unsafe.Sizeof(mi))
		if ok, _, _ := getMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi))); ok == 0 {
			continue
		}
		x, y := Centre(int(mi.work.left), int(mi.work.top), int(mi.work.right), int(mi.work.bottom), int(w), int(h))
		setWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), 0, 0, 0x1|0x4|0x10) // SWP_NOSIZE | SWP_NOZORDER | SWP_NOACTIVATE
		Log("window position: %dx%d centred at %d,%d in work area %d,%d-%d,%d", w, h, x, y, mi.work.left, mi.work.top, mi.work.right, mi.work.bottom)
		lastX, lastY, lastW, lastH = int32(x), int32(y), w, h
	}
}

var (
	user32           = syscall.NewLazyDLL("user32.dll")
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	messageBoxW      = user32.NewProc("MessageBoxW")
	attachConsole    = kernel32.NewProc("AttachConsole")
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

// AttachParentConsole lets a GUI program (no console of its own) print to the terminal it was started from: -no-window output and early error text show up
// there. Nothing is done when stdout and stderr are already real handles (redirected to a file or a pipe), or when there is no parent console
// (a double click): then nothing is printed, and the log file and the window carry everything.
func AttachParentConsole() {
	for _, f := range []*os.File{os.Stdout, os.Stderr} {
		if f != nil && validHandle(f.Fd()) {
			return
		}
	}
	if r, _, _ := attachConsole.Call(^uintptr(0)); r == 0 { // ATTACH_PARENT_PROCESS = -1
		return
	}
	if out, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout, os.Stderr = out, out
	}
}

func validHandle(h uintptr) bool { return h != 0 && h != ^uintptr(0) }

// OpenFile opens a file in the person's own program (Notepad for a log), so the window can show the log file itself, not a localhost address.
func OpenFile(path string) error {
	return exec.Command("notepad.exe", path).Start()
}

var (
	ole32             = syscall.NewLazyDLL("ole32.dll")
	getKnownFolder    = shell32.NewProc("SHGetKnownFolderPath")
	coTaskMemFree     = ole32.NewProc("CoTaskMemFree")
	createJobObject   = kernel32.NewProc("CreateJobObjectW")
	assignToJob       = kernel32.NewProc("AssignProcessToJobObject")
	terminateJob      = kernel32.NewProc("TerminateJobObject")
	queryJob          = kernel32.NewProc("QueryInformationJobObject")
	enumWindows       = user32.NewProc("EnumWindows")
	getWindowTextW    = user32.NewProc("GetWindowTextW")
	getClassNameW     = user32.NewProc("GetClassNameW")
	isWindowVisible   = user32.NewProc("IsWindowVisible")
	isIconic          = user32.NewProc("IsIconic")
	showWindow        = user32.NewProc("ShowWindow")
	setForeground     = user32.NewProc("SetForegroundWindow")
	flashWindowEx     = user32.NewProc("FlashWindowEx")
	ntResumeProcess   = syscall.NewLazyDLL("ntdll.dll").NewProc("NtResumeProcess")
	windowTitlePrefix = "Fileion Installer"
)

type guid struct {
	d1 uint32
	d2 uint16
	d3 uint16
	d4 [8]byte
}

var (
	folderDownloads    = guid{0x374DE290, 0x123F, 0x4565, [8]byte{0x91, 0x64, 0x39, 0xC4, 0x92, 0x5E, 0x46, 0x7B}}
	folderPrograms     = guid{0xA77F5D77, 0x2E2B, 0x44C3, [8]byte{0xA6, 0xA2, 0xAB, 0xA6, 0x01, 0x05, 0x4A, 0x51}}
	folderLocalAppData = guid{0xF1B32785, 0x6FBA, 0x4FCF, [8]byte{0x9D, 0x55, 0x7B, 0x8E, 0x7F, 0x15, 0x70, 0x91}}
)

func knownFolder(id guid) string {
	var p *uint16
	if r, _, _ := getKnownFolder.Call(uintptr(unsafe.Pointer(&id)), 0, 0, uintptr(unsafe.Pointer(&p))); r != 0 || p == nil {
		return ""
	}
	defer coTaskMemFree.Call(uintptr(unsafe.Pointer(p)))
	n := 0
	for q := unsafe.Pointer(p); *(*uint16)(q) != 0; q = unsafe.Add(q, 2) {
		n++
	}
	return string(utf16.Decode(unsafe.Slice(p, n)))
}

// UserDirs gives the signed-in person's Downloads folder, Start menu Programs folder and %LOCALAPPDATA% (what the portable apps use). When the installer
// is started by a standard user and elevated with an administrator's name, these must come from the first (the person's own) process.
func UserDirs() (downloads, programs, localAppData string) {
	return knownFolder(folderDownloads), knownFolder(folderPrograms), knownFolder(folderLocalAppData)
}

// ExecJob runs a program inside a job object and waits until the program and everything it started have ended, so a setup that starts the real
// setup and exits is still waited for. When ctx ends the whole job is ended. The program's own windows are shown.
func ExecJob(ctx context.Context, name string, args []string, leave <-chan struct{}) error {
	job, _, _ := createJobObject.Call(0, 0)
	if job == 0 {
		return Exec(ctx, name, args)
	}
	// The job has no kill-on-close limit: closing its handle (leave, or the return) ends nothing
	defer syscall.CloseHandle(syscall.Handle(job))
	// The program starts SUSPENDED, is put in the job, and only then runs: nothing it starts can be outside the job.
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000 | 0x00000004} // CREATE_NO_WINDOW | CREATE_SUSPENDED
	if err := cmd.Start(); err != nil {
		return err
	}
	h, err := syscall.OpenProcess(0x0800|0x0100|0x0001, false, uint32(cmd.Process.Pid)) // SUSPEND_RESUME | SET_QUOTA | TERMINATE
	if err != nil {                                                                     // cannot manage it: end this one and run it plainly
		cmd.Process.Kill()
		cmd.Wait()
		return Exec(ctx, name, args)
	}
	defer syscall.CloseHandle(h)
	inJob := true
	if r, _, _ := assignToJob.Call(job, uintptr(h)); r == 0 {
		inJob = false
	}
	ntResumeProcess.Call(uintptr(h))
	if !inJob {
		return waitTree(ctx, cmd, leave)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	var main error
	haveMain := false
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case main = <-waited:
			haveMain = true
			waited = nil
		case <-ctx.Done():
			terminateJob.Call(job, 1)
			return ctx.Err()
		case <-leave:
			return nil
		case <-tick.C:
		}
		if haveMain && jobActive(job) == 0 {
			return main
		}
	}
}

// jobActive is how many processes are still running in the job.
func jobActive(job uintptr) uint32 {
	var info struct {
		totalUser, totalKernel, periodUser, periodKernel int64
		pageFaults, total, active, terminated            uint32
	}
	if r, _, _ := queryJob.Call(job, 1, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info), 0); r == 0 {
		return 0
	}
	return info.active
}

// waitTree is the fallback when the program cannot be put in a job: wait for it alone, and end its tree on ctx.
func waitTree(ctx context.Context, cmd *exec.Cmd, leave <-chan struct{}) error {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-leave:
		return nil
	case <-ctx.Done():
		exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
		<-done
		return ctx.Err()
	}
}

// FrontWindow brings the installer's window to the front and flashes its taskbar button until it is used. Windows does not let a background
// program take the foreground, so the flash is what always works; the restore and the foreground request are best effort.
func FrontWindow() {
	var found uintptr
	cb := syscall.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		if v, _, _ := isWindowVisible.Call(hwnd); v == 0 {
			return 1
		}
		buf := make([]uint16, 128)
		n, _, _ := getWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if !strings.HasPrefix(string(utf16.Decode(buf[:n])), windowTitlePrefix) {
			return 1
		}
		cn := make([]uint16, 64)
		m, _, _ := getClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&cn[0])), uintptr(len(cn)))
		if string(utf16.Decode(cn[:m])) != "Chrome_WidgetWin_1" { // the browser's app window, not an Explorer window of that name
			return 1
		}
		found = hwnd
		return 0
	})
	enumWindows.Call(cb, 0)
	if found == 0 {
		return
	}
	if v, _, _ := isIconic.Call(found); v != 0 {
		showWindow.Call(found, 9) // SW_RESTORE
	}
	setForeground.Call(found)
	fl := struct {
		size    uint32
		hwnd    uintptr
		flags   uint32
		count   uint32
		timeout uint32
	}{flags: 0x3 | 0xC, count: 0xFFFFFFFF} // FLASHW_ALL | FLASHW_TIMERNOFG: until the window is in front
	fl.size, fl.hwnd = uint32(unsafe.Sizeof(fl)), found
	flashWindowEx.Call(uintptr(unsafe.Pointer(&fl)))
}

// RunAsUser starts a program the way a double click in Explorer does, as the signed-in person: the installer itself runs as administrator, and a portable app should not.
func RunAsUser(path string) error { return exec.Command("explorer.exe", path).Start() }

// OpenFolder shows a folder in Explorer.
func OpenFolder(path string) error { return exec.Command("explorer.exe", path).Start() }

func powershell(env []string, script string) ([]byte, error) {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-STA", "-ExecutionPolicy", "Bypass", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
	cmd.Env = append(os.Environ(), env...)
	return cmd.Output()
}

// PickFolder shows the Windows folder picker and returns the chosen folder ("" when cancelled).
func PickFolder(title string) (string, error) {
	if d := os.Getenv("FILEION_INSTALLER_TEST_PICK"); d != "" { // the test harness has no one to click the dialog
		return d, nil
	}
	out, err := powershell([]string{"FI_TITLE=" + title}, `Add-Type -AssemblyName System.Windows.Forms; $d = New-Object System.Windows.Forms.FolderBrowserDialog; $d.Description = $env:FI_TITLE; if ($d.ShowDialog() -eq 'OK') { [Console]::Out.Write($d.SelectedPath) }`)
	return strings.TrimSpace(string(out)), err
}

// CreateShortcut writes a .lnk (Windows has no plain-file form of it; the Windows scripting object makes it).
func CreateShortcut(lnk, target, workDir string) error {
	if err := os.MkdirAll(filepath.Dir(lnk), 0o755); err != nil {
		return err
	}
	_, err := powershell([]string{"FI_LNK=" + lnk, "FI_TARGET=" + target, "FI_WD=" + workDir},
		`$ErrorActionPreference = 'Stop'; $s = (New-Object -ComObject WScript.Shell).CreateShortcut($env:FI_LNK); $s.TargetPath = $env:FI_TARGET; $s.WorkingDirectory = $env:FI_WD; $s.Save()`)
	return err
}
