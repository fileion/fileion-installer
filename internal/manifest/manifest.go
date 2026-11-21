// Package manifest reads the app list the Fileion Installer installs: the code in the installer's own file name,
// and the JSON at software.fileion.com/setup/manifest/{code}.
package manifest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// App is one title. Windows entries carry URL, SHA256 and SilentSwitch; Linux entries carry FlatpakID.
type App struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	Version       string `json:"version"`
	MinVersion    string `json:"min_version"`
	URL           string `json:"url"`
	SHA256        string `json:"sha256"`
	InstallerType string `json:"installer_type"`
	Architecture  string `json:"architecture"`
	SilentSwitch  string `json:"silent_switch"`
	// Mode is how Fileion says the app is handled: silent, portable, zip or interactive ("a few clicks"). Empty from a server that does not send it yet.
	Mode string `json:"mode"`
	// ZipExe is the program inside a zip (a relative path), when Fileion knows it.
	ZipExe      string `json:"zip_exe"`
	ProductCode string `json:"product_code"`
	WingetID    string `json:"winget_id"`
	FlatpakID   string `json:"flatpak_id"`
	Page        string `json:"page"` // the app's Fileion download page
	Icon        string `json:"icon"` // the app's logo on a fileion.com address (optional; older manifests have none)
	// Match finds an installed copy when its Uninstall key name changes with every release: DisplayName starts with Name and Publisher is
	// equal. Both are always set when the manifest sends it.
	Match *Match `json:"match,omitempty"`
}

// Match is the name rule of an app whose Windows Uninstall key is not stable.
type Match struct {
	Name      string `json:"name"`
	Publisher string `json:"publisher"`
}

// Modes this program can run. An entry with any other mode is not run (a newer Fileion may add modes an older installer must not guess at).
const (
	ModeSilent      = "silent"
	ModePortable    = "portable"
	ModeZip         = "zip"
	ModeInteractive = "interactive"
)

// EffectiveMode is the app's mode; an entry with none (an older server) is silent, as before.
func (a App) EffectiveMode() string {
	if a.Mode == "" {
		return ModeSilent
	}
	return a.Mode
}

// KnownMode says whether this program can run the app's mode.
func (a App) KnownMode() bool {
	switch a.EffectiveMode() {
	case ModeSilent, ModePortable, ModeZip, ModeInteractive:
		return true
	}
	return false
}

// OrderForRun puts the apps that need the person last (interactive), so the unattended ones finish on their own first; the order inside each group is kept.
func OrderForRun(apps []App) []App {
	out := make([]App, 0, len(apps))
	for _, last := range []bool{false, true} {
		for _, a := range apps {
			if (a.EffectiveMode() == ModeInteractive) == last {
				out = append(out, a)
			}
		}
	}
	return out
}

// Manifest is the answer of /setup/manifest/{code}.
type Manifest struct {
	Code string `json:"code"`
	OS   string `json:"os"`
	Apps []App  `json:"apps"`
}

var codeRe = regexp.MustCompile(`^[a-f0-9]{8,12}$`)

// copySuffix is what a browser or Explorer adds to a second copy: " (1)", "(2)".
var copySuffix = regexp.MustCompile(`\s*\(\d+\)\s*$`)

// CodeFromFileName takes the short code from a file name such as "Fileion-VLC-7-Zip-and-4-more-3fa91c0b.exe". It copes with what a
// download or a rename does to the name: a copy suffix ("… (1).exe"), spaces around it, an upper-case extension, and an extension
// written twice ("….exe.exe", from Explorer with extensions hidden). It returns "" when there is no code.
func CodeFromFileName(path string) string {
	name := filepath.Base(path)
	for i := 0; i < 6; i++ {
		name = strings.TrimSpace(copySuffix.ReplaceAllString(strings.TrimSpace(name), ""))
		last := name
		if j := strings.LastIndex(name, "-"); j >= 0 {
			last = name[j+1:]
		}
		if last = strings.ToLower(strings.TrimSpace(last)); codeRe.MatchString(last) {
			return last
		}
		ext := filepath.Ext(name)
		if ext == "" || ext == name {
			return ""
		}
		name = strings.TrimSuffix(name, ext)
	}
	return ""
}

// Parse reads a manifest and drops entries that cannot be verified: Windows entries need an https URL and a
// 64-character SHA-256; Linux entries need a Flatpak ID.
func Parse(r io.Reader, os string) (*Manifest, error) {
	var m Manifest
	if err := json.NewDecoder(io.LimitReader(r, 1<<20)).Decode(&m); err != nil {
		return nil, fmt.Errorf("the manifest is not valid: %w", err)
	}
	kept := m.Apps[:0]
	for _, a := range m.Apps {
		a.SHA256 = strings.ToLower(strings.TrimSpace(a.SHA256))
		if os == "linux" {
			if a.FlatpakID == "" {
				continue
			}
		} else {
			u, err := url.Parse(a.URL)
			if err != nil || u.Scheme != "https" || u.Host == "" || len(a.SHA256) != 64 || strings.Trim(a.SHA256, "0123456789abcdef") != "" {
				continue
			}
		}
		kept = append(kept, a)
	}
	m.Apps = kept
	return &m, nil
}

// retryDelay is the wait before the one retry of a manifest fetch (a set created a moment ago, or a brief blip); tests shorten it.
var retryDelay = 3 * time.Second

// Fetch downloads the manifest for a code from base (https://software.fileion.com). A network error, a 5xx or a 404 is tried once more
// after a few seconds; any other answer is final.
func Fetch(ctx context.Context, c *http.Client, base, code, os string) (*Manifest, error) {
	m, retry, err := fetchOnce(ctx, c, base, code, os)
	if err == nil || !retry {
		return m, err
	}
	select {
	case <-ctx.Done():
		return nil, err
	case <-time.After(retryDelay):
	}
	m, _, err = fetchOnce(ctx, c, base, code, os)
	return m, err
}

func fetchOnce(ctx context.Context, c *http.Client, base, code, os string) (m *Manifest, retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/setup/manifest/"+code+"?os="+os, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Accept", "application/json")
	res, err := c.Do(req)
	if err != nil {
		return nil, ctx.Err() == nil, fmt.Errorf("could not reach Fileion: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, res.StatusCode >= 500 || res.StatusCode == http.StatusNotFound, fmt.Errorf("Fileion has no list for this installer (HTTP %d)", res.StatusCode)
	}
	m, err = Parse(res.Body, os)
	return m, false, err
}
