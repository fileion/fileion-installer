package stamp

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, name string, parts ...[]byte) string {
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, bytes.Join(parts, nil), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

var exe = bytes.Repeat([]byte("MZ-program-bytes-"), 100)

func trailer(t *testing.T, code string) []byte {
	b, err := Encode(code)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAValidTrailerIsRead(t *testing.T) {
	for _, code := range []string{"3fa91c0b", "3fa91c0b12", "0123456789ab"} {
		if got := Read(write(t, "Fileion-Installer.exe", exe, trailer(t, code))); got != code {
			t.Errorf("code %s: read %q", code, got)
		}
	}
	if b := trailer(t, "3fa91c0b"); len(b) != Size || string(b[22:]) != "FILEIONSET" || b[16] != 8 {
		t.Errorf("layout: % x", b)
	}
}

func TestARenamedFileKeepsItsStamp(t *testing.T) {
	p := write(t, "x.exe", exe, trailer(t, "3fa91c0b"))
	for _, name := range []string{"My special bundle.exe", "Fileion-Installer.exe", "renamed (1).exe", "no extension"} {
		q := filepath.Join(filepath.Dir(p), name)
		if err := os.Rename(p, q); err != nil {
			t.Fatal(err)
		}
		p = q
		if got := Read(p); got != "3fa91c0b" {
			t.Errorf("%s: read %q", name, got)
		}
	}
}

func TestNoTrailerGivesNothing(t *testing.T) {
	if Read(write(t, "a.exe", exe)) != "" {
		t.Error("a plain file has no stamp")
	}
	if Read(write(t, "tiny.exe", []byte("MZ"))) != "" {
		t.Error("a file shorter than the trailer has no stamp")
	}
	if Read(filepath.Join(t.TempDir(), "missing.exe")) != "" {
		t.Error("a missing file has no stamp")
	}
}

func TestACorruptTrailerIsIgnored(t *testing.T) {
	good := trailer(t, "3fa91c0b")
	flip := func(i int) []byte { b := append([]byte(nil), good...); b[i] ^= 0xFF; return b }
	cases := map[string][]byte{
		"a changed code byte": flip(0),
		"a changed length":    flip(16),
		"a changed version":   flip(17),
		"a changed checksum":  flip(19),
		"a changed magic":     flip(25),
		"the last byte gone":  good[:Size-1],
		"a byte appended":     append(append([]byte(nil), good...), 0),
		"dirty padding":       func() []byte { b := append([]byte(nil), good...); b[12] = 'x'; return b }(),
		"upper-case code":     func() []byte { b, _ := Encode("3fa91c0b"); b[0] = 'F'; return b }(),
		"a truncated copy":    good[10:],
	}
	for name, tr := range cases {
		if got := Read(write(t, "bad.exe", exe, tr)); got != "" {
			t.Errorf("%s: read %q", name, got)
		}
	}
	// a length that is valid but a code that is not hex, with a matching CRC, is still refused
	b := trailer(t, "3fa91c0b")
	copy(b, "zzzzzzzz")
	if _, ok := Decode(b); ok {
		t.Error("a bad checksum must be refused")
	}
}

func TestEncodeRefusesBadCodes(t *testing.T) {
	for _, c := range []string{"", "abc", "3FA91C0B", "3fa91c0bxx", "3fa91c0b1234x", "../../etc"} {
		if _, err := Encode(c); err == nil {
			t.Errorf("%q should be refused", c)
		}
	}
}
