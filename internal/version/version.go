// Package version compares the dotted version numbers of installed apps and of the list.
package version

import (
	"strconv"
	"strings"
)

// AtLeast says whether the installed version is the minimum or newer. Versions compare as dotted numbers: "3.0.20" < "3.0.21",
// "10.0" > "9.9", "25" < "25.0.1", "1.2" = "1.2.0", and four parts work ("1.2.3.4"). A missing minimum means any installed copy counts.
// An installed version with no number at the start ("", "unknown", "beta") cannot be told from an old one, so it is never at least a
// minimum: the app is updated, not skipped.
func AtLeast(installed, min string) bool {
	if strings.TrimSpace(min) == "" {
		return true
	}
	a, b := numbers(installed), numbers(min)
	if len(a) == 0 {
		return false
	}
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x > y
		}
	}
	return true
}

func numbers(v string) []int {
	var out []int
	for _, part := range strings.FieldsFunc(strings.TrimLeft(strings.TrimSpace(v), "vV"), func(r rune) bool { return r == '.' || r == '-' || r == '+' || r == '_' }) {
		n, err := strconv.Atoi(strings.TrimRight(part, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"))
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out
}
