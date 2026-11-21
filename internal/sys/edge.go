package sys

import "path/filepath"

// Log gets what the window code did (the exact Edge command line, window moves, how a link was opened): a run is checked from the log file.
var Log = func(format string, args ...any) {}

// EdgeArgs is the command line of the installer's window. Sources:
//   - --app, --window-size, --user-data-dir, --no-first-run, --no-default-browser-check, --disable-sync: Chromium switches that Edge shares
//     (Edge publishes no list of its own: https://textslashplain.com/2022/01/05/edge-command-line-arguments says to use Chromium's).
//   - --inprivate: Edge's documented private-window switch. An InPrivate window has no signed-in profile and no sync, so the Windows account is not
//     signed in to it. Edge's own implicit sign-in has no switch: its only documented control is the policy ImplicitSignInEnabled
//     (https://learn.microsoft.com/en-us/deployedge/microsoft-edge-policies/implicitsigninenabled), a registry setting for every Edge window of the
//     person, which a program must not change.
//
// x, y are the window's top-left in pixels, or -1,-1 to leave it to Edge (the window is also centred after it opens: see CenterLoop).
func EdgeArgs(url, localAppData string, w, h int) []string {
	args := []string{"--app=" + url, "--inprivate", "--no-first-run", "--no-default-browser-check", "--disable-sync", "--window-size=" + itoa(w) + "," + itoa(h)}
	if localAppData != "" {
		// Its own profile folder: in the person's default one Edge restores the window size it saved from older runs and ignores --window-size
		args = append(args, "--user-data-dir="+filepath.Join(localAppData, "Fileion", "window"))
	}
	return args
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}

// Centre is the top-left that centres a w x h window on a work area (left, top, right, bottom), kept inside the work area.
func Centre(left, top, right, bottom, w, h int) (x, y int) {
	x, y = left+(right-left-w)/2, top+(bottom-top-h)/2
	if x < left {
		x = left
	}
	if y < top {
		y = top
	}
	return
}
