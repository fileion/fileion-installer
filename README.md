# Fileion Installer

A small program that installs the apps you picked at [software.fileion.com/setup](https://software.fileion.com/setup), one after another, the way Ninite does. Free, MIT licensed, no ads, no extras.

## What it does

1. Reads the short code in its own file name (`Fileion-VLC-7-Zip-and-4-more-3fa91c0b.exe` → `3fa91c0b`).
2. Gets the app list for that code from `https://software.fileion.com/setup/manifest/{code}` (JSON: vendor URL, SHA-256, silent switch, minimum version, product code).
3. For each app:
   - **Windows:** skips it when it is already installed at the listed version (the product code under the Windows Uninstall registry keys); otherwise downloads the file **from the vendor's own https address only**, checks its SHA-256 against the vendor's, and runs it with the silent switch. If the SHA-256 does not match, the file is **not run**, it is deleted and the window says so.
   - **Linux:** installs it from Flathub with Flatpak, or updates it when Flatpak already has it.
4. Shows a plain progress window: one line per app, a link to the log, a Close button, and the SignPath credit.
5. Sends **one anonymous count** at the end: how many apps worked and how many did not. No name, IP-linked id, app list or computer detail is sent (use `-no-report` to send nothing).

An installer that shows its own setup window or hangs is stopped after a time limit (10 minutes, `-app-timeout`) and marked "Skipped: needs manual install", with a link to the app's Fileion download page. The SHA-256 check always runs first, before anything is started.

Run the same file again later and it updates the same apps.

Windows asks for administrator permission once at the start, so every app installs for everyone on the PC and none asks again. On Linux, some desktops need "Allow executing file as program" ticked in the file's Properties once; Flatpak must be installed.

## Build

Needs only Go (the version in `go.mod` or newer). No third-party modules.

```sh
go vet ./... && go test ./...
./build.sh 1.0.0        # dist/Fileion-Installer.exe, dist/Fileion-Installer, dist/SHA256SUMS
```

The build is reproducible: `CGO_ENABLED=0`, `-trimpath`, `-buildvcs=false`, no build ID, and `SOURCE_DATE_EPOCH` fixed. The same source with the same Go version gives the same bytes, so anyone can rebuild and compare SHA-256.

The window is a small page served on `127.0.0.1` (random port and path, Host-checked) and opened in its own app-style window of Edge (Windows) or a Chromium browser (Linux; the default browser otherwise). That keeps the program free of GUI libraries.

### Code signing (SignPath Foundation)

`.github/workflows/build.yml` builds on GitHub Actions and, on a version tag, submits the unsigned `.exe` to SignPath for signing. Signed files show **SignPath Foundation** as the publisher. Set the two `CHANGE-ME` ids and the `SIGNPATH_API_TOKEN` secret first. The Linux AppImage is not signed.

Free code signing provided by SignPath.io, certificate by SignPath Foundation.

### Options

| Flag | Meaning |
|---|---|
| `-code` | Use this set code instead of the one in the file name |
| `-base` | Server for the list and the count (default `https://software.fileion.com`) |
| `-no-window` | Print progress in the terminal |
| `-app-timeout` | Longest one installer may run (default 10m) |
| `-no-report` | Do not send the anonymous count |

## Known limits

- Apps whose installer is MSIX, a zip or a portable file, or has no silent switch, are not run: the window sends you to the app's Fileion page.
- On Windows an app without a product code in the list cannot be told apart from "not installed", so it is installed again on a re-run.
- Tested with Go unit tests (hash mismatch, skip-if-installed, manifest parsing, version compare, window). The Windows registry check and the permission prompt need a run on a real Windows PC.
