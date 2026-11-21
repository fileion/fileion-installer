package sys

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheWindowIsAPlainOneWithNoFirstRunAndNoSyncAndItsOwnProfile(t *testing.T) {
	args := strings.Join(EdgeArgs("http://127.0.0.1:1/t/x/", `C:\Users\p\AppData\Local`, 420, 260), " ")
	for _, want := range []string{"--app=http://127.0.0.1:1/t/x/", "--no-first-run", "--no-default-browser-check", "--disable-sync", "--window-size=420,260", `--user-data-dir=`} {
		if !strings.Contains(args, want) {
			t.Errorf("missing %q in %s", want, args)
		}
	}
	if strings.Contains(args, "--inprivate") || strings.Contains(args, "--guest") {
		t.Error("a normal window: its title must not say InPrivate")
	}
	if strings.Contains(strings.Join(EdgeArgs("u", "", 1, 1), " "), "--user-data-dir") {
		t.Error("no profile folder without LocalAppData")
	}
}

func TestCentreUsesTheWorkAreaAndStaysInsideIt(t *testing.T) {
	if x, y := Centre(0, 0, 1920, 1040, 720, 300); x != 600 || y != 370 {
		t.Errorf("%d,%d", x, y)
	}
	if x, y := Centre(1920, 0, 3840, 1040, 720, 300); x != 2520 || y != 370 { // second monitor
		t.Errorf("%d,%d", x, y)
	}
	if x, y := Centre(0, 40, 600, 400, 720, 900); x != 0 || y != 40 { // bigger than the area
		t.Errorf("%d,%d", x, y)
	}
}

func TestSeedingTurnsSignInOffAndKeepsEveryOtherKey(t *testing.T) {
	la := t.TempDir()
	file := filepath.Join(ProfileDir(la), "Default", "Preferences")
	if _, err := SeedProfile(la); err != nil { // no profile yet: created
		t.Fatal(err)
	}
	os.WriteFile(file, []byte(`{"browser":{"window_placement":{"left":10,"top":20}},"signin":{"allowed":true,"other":"keep"}}`), 0o600)
	what, err := SeedProfile(la)
	if err != nil || !strings.Contains(what, "signin.allowed=false") {
		t.Fatal(what, err)
	}
	var got map[string]map[string]any
	b, _ := os.ReadFile(file)
	json.Unmarshal(b, &got)
	if got["signin"]["allowed"] != false || got["signin"]["allowed_on_next_startup"] != false || got["signin"]["other"] != "keep" || got["browser"]["window_placement"] == nil {
		t.Fatalf("%s", b)
	}
	os.WriteFile(file, []byte(`not json`), 0o600) // never clobber what we cannot read
	if _, err := SeedProfile(la); err == nil {
		t.Fatal("must refuse")
	}
	if b, _ := os.ReadFile(file); string(b) != "not json" {
		t.Fatal("the file was changed")
	}
}

func TestSeedingChangesNothingButTheTwoSigninKeysEvenForBigIntegers(t *testing.T) {
	la := t.TempDir()
	file := filepath.Join(ProfileDir(la), "Default", "Preferences")
	os.MkdirAll(filepath.Dir(file), 0o700)
	os.WriteFile(file, []byte(`{"big":1234567890123456789,"deep":{"unknown":{"x":[1,2.5,"a&b<c>"]}},"signin":{"allowed":true}}`), 0o600)
	if _, err := SeedProfile(la); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(file)
	want := `{"big":1234567890123456789,"deep":{"unknown":{"x":[1,2.5,"a&b<c>"]}},"signin":{"allowed":false,"allowed_on_next_startup":false}}`
	if string(got) != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestMissingFileIsSeededButAnyOtherReadErrorLeavesItAloneAndSkips(t *testing.T) {
	la := t.TempDir()
	file := filepath.Join(ProfileDir(la), "Default", "Preferences")
	if _, err := SeedProfile(la); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(file); string(b) != `{"signin":{"allowed":false,"allowed_on_next_startup":false}}` {
		t.Fatalf("%s", b)
	}
	// unreadable for a reason other than "does not exist" (a directory stands in for a locked file): no seeding, nothing replaced
	os.Remove(file)
	if err := os.Mkdir(file, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := SeedProfile(la); err == nil {
		t.Fatal("must skip with an error the caller only logs")
	}
	if fi, err := os.Stat(file); err != nil || !fi.IsDir() {
		t.Fatal("the unreadable Preferences was replaced")
	}
}

func TestBrokenJsonPreferencesIsRefusedAndLeftByteForByte(t *testing.T) {
	la := t.TempDir()
	file := filepath.Join(ProfileDir(la), "Default", "Preferences")
	os.MkdirAll(filepath.Dir(file), 0o700)
	os.WriteFile(file, []byte(`{not json`), 0o600)
	if _, err := SeedProfile(la); err == nil {
		t.Fatal("must return an error")
	}
	if b, _ := os.ReadFile(file); string(b) != `{not json` {
		t.Fatalf("the file was changed: %q", b)
	}
}

// The window is 420 x 260 DIPs (the start screen) or 720 x 330 (the list). At each scale the monitor is the same logical screen (1920 x 1080 DIPs) in real
// pixels, the taskbar takes 40 DIPs at the bottom: the margins left/right and top/bottom must come out equal (to one pixel) in real pixels.
func TestCentredAtEveryScale(t *testing.T) {
	for _, c := range []struct {
		name string
		dpi  int
	}{{"100%", 96}, {"125%", 120}, {"150%", 144}, {"175%", 168}, {"200%", 192}} {
		for _, win := range [][2]int{{420, 260}, {720, 330}} {
			W, H, bar := ScalePx(1920, c.dpi), ScalePx(1080, c.dpi), ScalePx(40, c.dpi)
			w, h := ScalePx(win[0], c.dpi), ScalePx(win[1], c.dpi)
			x, y := Centre(0, 0, W, H-bar, w, h)
			l, r, tp, b := x, W-x-w, y, H-bar-y-h
			if abs(l-r) > 1 || abs(tp-b) > 1 || l < 0 || tp < 0 {
				t.Errorf("%s %dx%d DIPs = %dx%d px: margins l=%d r=%d t=%d b=%d", c.name, win[0], win[1], w, h, l, r, tp, b)
			}
			// not the top-left: DIPs used as pixels would put the window at this x on a PC at 150% or 200%
			if c.dpi > 96 && x == Centre2(ScalePx(1920, 96), win[0]) {
				t.Errorf("%s: centred as if pixels were DIPs", c.name)
			}
		}
		// a second monitor at the right: the work area starts at x=W
		W := ScalePx(1920, c.dpi)
		x, _ := Centre(W, 0, 2*W, ScalePx(1040, c.dpi), ScalePx(420, c.dpi), ScalePx(260, c.dpi))
		if x < W || x+ScalePx(420, c.dpi) > 2*W {
			t.Errorf("%s: window not inside the second monitor (x=%d)", c.name, x)
		}
	}
}

func Centre2(workW, wDIP int) int { return (workW - wDIP) / 2 }

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func TestScalePxAndWhoMovedIt(t *testing.T) {
	for dpi, want := range map[int]int{96: 420, 144: 630, 192: 840} {
		if got := ScalePx(420, dpi); got != want {
			t.Errorf("420 DIPs at %d dpi = %d, want %d", dpi, got, want)
		}
	}
	if PersonMoved(1, -2, 96) || PersonMoved(3, 3, 144) || PersonMoved(0, 0, 192) {
		t.Error("rounding nudges are not the person")
	}
	if !PersonMoved(40, 0, 96) || !PersonMoved(0, -30, 144) || !PersonMoved(17, 0, 192) {
		t.Error("a real move is the person's")
	}
}
