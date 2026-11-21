package sys

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Log gets what the window code did (the exact Edge command line, window moves, how a link was opened): a run is checked from the log file.
var Log = func(format string, args ...any) {}

// EdgeArgs is the command line of the installer's window: a normal (not private) app window with its own profile folder. Sources:
//   - --app, --window-size, --user-data-dir, --no-first-run, --no-default-browser-check, --disable-sync: Chromium switches that Edge shares
//     (Edge publishes no list of its own: https://textslashplain.com/2022/01/05/edge-command-line-arguments says to use Chromium's).
//   - Edge's implicit sign-in has no switch; its only documented control is the policy ImplicitSignInEnabled
//     (https://learn.microsoft.com/en-us/deployedge/microsoft-edge-policies/implicitsigninenabled), a registry setting for every Edge window of the
//     person, which a program must not change. So the window's own profile is seeded with Chromium's sign-in prefs instead: see SeedProfile.
//   - --inprivate was tried in v0.3.2 and dropped: the title read "Fileion Installer - [InPrivate]" with a dark title bar.
func EdgeArgs(url, localAppData string, w, h int) []string {
	args := []string{"--app=" + url, "--no-first-run", "--no-default-browser-check", "--disable-sync", "--window-size=" + itoa(w) + "," + itoa(h)}
	if localAppData != "" {
		// Its own profile folder: in the person's default one Edge restores the window size it saved from older runs and ignores --window-size
		args = append(args, "--user-data-dir="+ProfileDir(localAppData))
	}
	return args
}

// ProfileDir is the window's own Edge profile folder.
func ProfileDir(localAppData string) string { return filepath.Join(localAppData, "Fileion", "window") }

// SeedProfile turns the window profile's sign-in off before Edge starts, by merging two keys into Default\Preferences and leaving every other key (the
// saved window placement, Edge's own) as it is:
//   - signin.allowed = false: Chromium's kSigninAllowed, "whether the user may sign in" (components/signin/public/base/signin_pref_names.cc,
//     https://source.chromium.org/chromium/chromium/src/+/main:/components/signin/public/base/signin_pref_names.cc). Chromium recomputes it at
//     startup from signin.allowed_on_next_startup (chrome/browser/signin/account_consistency_mode_manager.cc), so that one is set too.
//   - signin.allowed_on_next_startup = false.
//
// No Edge-specific implicit-sign-in preference is documented anywhere we could find, so none is written. It returns what it did, for the log.
func SeedProfile(localAppData string) (string, error) {
	dir := filepath.Join(ProfileDir(localAppData), "Default")
	file := filepath.Join(dir, "Preferences")
	prefs := map[string]any{}
	b, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("could not read the profile's Preferences (left as it is, no seeding): %w", err)
	}
	if err == nil {
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber() // a big integer in Edge's own keys must come back as it was, not rounded through float64
		if err := dec.Decode(&prefs); err != nil {
			return "", fmt.Errorf("the profile's Preferences is not JSON (left as it is): %w", err)
		}
	}
	si, _ := prefs["signin"].(map[string]any)
	if si == nil {
		si = map[string]any{}
	}
	si["allowed"], si["allowed_on_next_startup"] = false, false
	prefs["signin"] = si
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // keep & < > in Edge's strings as they were
	if err := enc.Encode(prefs); err != nil {
		return "", err
	}
	out := bytes.TrimRight(buf.Bytes(), "\n")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp := file + ".fileion-tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, file); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return "signin.allowed=false signin.allowed_on_next_startup=false in " + file, nil
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

// Settle is how long after a placement a move is still taken to be Edge's own; MoveTolerance (DIPs) is how far a later move must go to be the person's.
const (
	Settle        = 2 * time.Second
	MoveTolerance = 8
	maxPlacements = 12
)

// ScalePx is a length in DIPs (96 per inch) in pixels at dpi, rounded: 420 DIPs is 630 px at 150% (144 dpi) and 840 px at 200% (192 dpi).
func ScalePx(dip, dpi int) int { return (dip*dpi + 48) / 96 }

// PersonMoved says whether a window that is (dx, dy) pixels from where it was put was moved by the person, not nudged by rounding at this dpi.
func PersonMoved(dx, dy, dpi int) bool {
	tol := ScalePx(MoveTolerance, dpi)
	return dx > tol || dx < -tol || dy > tol || dy < -tol
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
