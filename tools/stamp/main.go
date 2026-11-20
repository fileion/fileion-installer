// Command stamp puts a set code at the end of an installer file, shows it, or removes it. Fileion's server does the same when it
// serves a download; this is for testing, for the pendrive check, and for comparing a stamped file with SHA256SUMS (-strip).
//
//	stamp -code 3fa91c0b in.exe out.exe     copy in.exe to out.exe with the code stamped on
//	stamp -show file.exe                    print the stamped code (exit 1 when there is none)
//	stamp -strip in.exe out.exe             copy without the stamp: the bytes of the release file
package main

import (
	"flag"
	"fmt"
	"os"

	"fileion-installer/internal/stamp"
)

func main() {
	code := flag.String("code", "", "stamp this set code (8-12 hex characters) on a copy: stamp -code CODE in out")
	show := flag.Bool("show", false, "print the stamped code of a file")
	strip := flag.Bool("strip", false, "copy a file without its stamp: stamp -strip in out")
	flag.Parse()
	a := flag.Args()
	switch {
	case *show && len(a) == 1:
		c := stamp.Read(a[0])
		if c == "" {
			fmt.Fprintln(os.Stderr, "no stamp")
			os.Exit(1)
		}
		fmt.Println(c)
	case *strip && len(a) == 2:
		b, err := os.ReadFile(a[0])
		if err != nil || stamp.Read(a[0]) == "" {
			fail("no stamp to remove", err)
		}
		write(a[1], b[:len(b)-stamp.Size])
	case *code != "" && len(a) == 2:
		tr, err := stamp.Encode(*code)
		if err != nil {
			fail("", err)
		}
		b, err := os.ReadFile(a[0])
		if err != nil {
			fail("", err)
		}
		if stamp.Read(a[0]) != "" {
			fail("the file is already stamped; strip it first", nil)
		}
		write(a[1], append(b, tr...))
	default:
		fmt.Fprintln(os.Stderr, "usage: stamp -code CODE in out | stamp -show file | stamp -strip in out")
		os.Exit(2)
	}
}

func write(path string, b []byte) {
	if err := os.WriteFile(path, b, 0o755); err != nil {
		fail("", err)
	}
}

func fail(msg string, err error) {
	if err != nil {
		msg = fmt.Sprint(msg, " ", err)
	}
	fmt.Fprintln(os.Stderr, "stamp:", msg)
	os.Exit(1)
}
