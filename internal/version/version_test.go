package version

import "testing"

func TestAtLeast(t *testing.T) {
	cases := []struct {
		have, min string
		want      bool
	}{
		{"3.0.20", "3.0.20", true}, {"3.0.21", "3.0.20", true}, {"3.0.19", "3.0.20", false},
		{"10.0", "9.9", true}, {"9.9", "10.0", false},
		// short against long: "25" is 25.0.0
		{"25", "25.0.1", false}, {"25.0.1", "25", true}, {"25", "25.0.0", true}, {"25.0.0", "25", true}, {"26", "25.0.1", true},
		{"1.2", "1.2.0", true}, {"1.2", "1.2.1", false}, {"v2.0", "2.0.0", true},
		// four parts
		{"1.2.3.4", "1.2.3.4", true}, {"1.2.3.5", "1.2.3.4", true}, {"1.2.3.3", "1.2.3.4", false}, {"1.2.3", "1.2.3.1", false}, {"1.2.4", "1.2.3.9", true},
		{"117.0.5368.49", "117.0.5368.5", true}, // numbers, not strings
		{"5", "", true}, {"2.0.0-beta", "2.0.1", false},
		// unparsable: never skipped
		{"", "1.0", false}, {"unknown", "1.0", false}, {"beta", "0.0.1", false}, {"(Preview)", "1", false}, {"  ", "2", false},
	}
	for _, c := range cases {
		if got := AtLeast(c.have, c.min); got != c.want {
			t.Errorf("AtLeast(%q, %q) = %v, want %v", c.have, c.min, got, c.want)
		}
	}
}
