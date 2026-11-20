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
	ProductCode   string `json:"product_code"`
	WingetID      string `json:"winget_id"`
	FlatpakID     string `json:"flatpak_id"`
	Page          string `json:"page"` // the app's Fileion download page
}

// Manifest is the answer of /setup/manifest/{code}.
type Manifest struct {
	Code string `json:"code"`
	OS   string `json:"os"`
	Apps []App  `json:"apps"`
}

var codeRe = regexp.MustCompile(`^[a-f0-9]{8,12}$`)

// CodeFromFileName takes the short code from a file name such as "Fileion-VLC-7-Zip-and-4-more-3fa91c0b.exe"
// (also "…-3fa91c0b (1).exe" from a browser that renamed a second download). It returns "" when there is none.
func CodeFromFileName(path string) string {
	name := filepath.Base(path)
	name = strings.TrimSuffix(name, filepath.Ext(name))
	name = strings.TrimSuffix(name, ".AppImage")
	if i := strings.Index(name, " ("); i >= 0 {
		name = name[:i]
	}
	if i := strings.LastIndex(name, "-"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.ToLower(name)
	if codeRe.MatchString(name) {
		return name
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

// Fetch downloads the manifest for a code from base (https://software.fileion.com).
func Fetch(ctx context.Context, c *http.Client, base, code, os string) (*Manifest, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/setup/manifest/"+code+"?os="+os, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	res, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach Fileion: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Fileion has no list for this installer (HTTP %d)", res.StatusCode)
	}
	return Parse(res.Body, os)
}
