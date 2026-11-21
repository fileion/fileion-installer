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
