// Package match finds an installed app by the name rule of its list entry, for apps whose Windows Uninstall key changes with every release.
package match

import (
	"strings"

	"fileion-installer/internal/manifest"
	"fileion-installer/internal/version"
)

// Entry is what one Uninstall entry in the registry says about itself.
type Entry struct {
	Name, Publisher, Version string
}

// Find looks through the entries (every hive: HKLM 64-bit, HKLM 32-bit and the user's own) for the rule: DisplayName starts with the
// rule's name (case-insensitive) AND Publisher is equal (case-insensitive). With several matches (two installed releases) it returns
// the highest version. Both parts of the rule are required: a rule with an empty part matches nothing.
func Find(entries []Entry, rule manifest.Match) (installed string, found bool) {
	name, pub := strings.ToLower(strings.TrimLeft(rule.Name, " ")), strings.TrimSpace(rule.Publisher) // a trailing space in the name is part of the rule ("Opera Stable ")
	if strings.TrimSpace(name) == "" || pub == "" {
		return "", false
	}
	for _, e := range entries {
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(e.Name)), name) || !strings.EqualFold(strings.TrimSpace(e.Publisher), pub) {
			continue
		}
		if !found || version.AtLeast(e.Version, installed) && !version.AtLeast(installed, e.Version) {
			installed, found = strings.TrimSpace(e.Version), true
		}
	}
	return installed, found
}
