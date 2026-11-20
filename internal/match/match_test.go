package match

import (
	"testing"

	"fileion-installer/internal/manifest"
)

var (
	opera   = manifest.Match{Name: "Opera Stable ", Publisher: "Opera Software"}
	operaGX = manifest.Match{Name: "Opera GX Stable ", Publisher: "Opera Software"}
	power   = manifest.Match{Name: "PowerToys", Publisher: "Microsoft Corporation"}
	netbean = manifest.Match{Name: "Apache NetBeans IDE", Publisher: "Apache Software Foundation"}
)

func TestOperaAndOperaGXAreEachMatchedOnlyToTheirOwnRow(t *testing.T) {
	entries := []Entry{
		{"Opera GX Stable 117.0.5408.35", "Opera Software", "117.0.5408.35"},
		{"Opera Stable 118.0.5461.20", "Opera Software", "118.0.5461.20"},
	}
	if v, ok := Find(entries, opera); !ok || v != "118.0.5461.20" {
		t.Errorf("Opera: %q %v", v, ok)
	}
	if v, ok := Find(entries, operaGX); !ok || v != "117.0.5408.35" {
		t.Errorf("Opera GX: %q %v", v, ok)
	}
	if _, ok := Find(entries[:1], opera); ok {
		t.Error("Opera must not match Opera GX")
	}
	if _, ok := Find(entries[1:], operaGX); ok {
		t.Error("Opera GX must not match Opera")
	}
}

func TestThePublisherMustBeEqualAndTheNameMustStartWithTheRule(t *testing.T) {
	e := []Entry{{"PowerToys (Preview) x64", "Microsoft Corporation", "0.90.1"}}
	if v, ok := Find(e, power); !ok || v != "0.90.1" {
		t.Errorf("PowerToys (Preview): %q %v", v, ok)
	}
	for name, entries := range map[string][]Entry{
		"other publisher":   {{"PowerToys", "Somebody Else", "1.0"}},
		"name not at start": {{"My PowerToys", "Microsoft Corporation", "1.0"}},
		"publisher prefix":  {{"PowerToys", "Microsoft Corporation Ltd", "1.0"}},
		"empty list":        nil,
	} {
		if _, ok := Find(entries, power); ok {
			t.Errorf("%s must not match", name)
		}
	}
	// case does not matter, for either part
	if v, ok := Find([]Entry{{"powertoys 0.91", "MICROSOFT CORPORATION", "0.91.0"}}, power); !ok || v != "0.91.0" {
		t.Errorf("case: %q %v", v, ok)
	}
}

func TestARuleNeedsBothPartsAndTheLatestOfTwoInstallsWins(t *testing.T) {
	e := []Entry{{"Apache NetBeans IDE 24", "Apache Software Foundation", "24"}}
	if _, ok := Find(e, manifest.Match{Name: "Apache NetBeans IDE"}); ok {
		t.Error("no publisher in the rule: no match")
	}
	if _, ok := Find(e, manifest.Match{Publisher: "Apache Software Foundation"}); ok {
		t.Error("no name in the rule: no match")
	}
	if v, ok := Find(append(e, Entry{"Apache NetBeans IDE 25", "Apache Software Foundation", "25.0.1"}, Entry{"Apache NetBeans IDE 23", "Apache Software Foundation", "23"}), netbean); !ok || v != "25.0.1" {
		t.Errorf("highest of three: %q %v", v, ok)
	}
}
