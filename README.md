# Fileion Installer

A small program that installs the apps you picked at [software.fileion.com/setup](https://software.fileion.com/setup), one after another, the way Ninite does. Free, MIT licensed, no ads, no extras.

## What it does

1. Reads the short code of your list: first the code Fileion stamped inside the file (the last 32 bytes), so you can rename the file freely (`My special bundle.exe`), keep it on a pendrive and run it on many PCs; then, for older downloads, the code in the file name (`Fileion-VLC-7-Zip-and-4-more-3fa91c0b.exe` → `3fa91c0b`). A file with no code (or any other failure before the window opens) says so in a message box on Windows, or in the Fileion window on Linux, with a link to software.fileion.com/setup or the help page; the log is `%TEMP%\fileion-installer.log` (`/tmp/fileion-installer.log` on Linux).
2. Gets the app list for that code from `https://software.fileion.com/setup/manifest/{code}` (JSON: vendor URL, SHA-256, silent switch, minimum version, product code, and for apps whose Uninstall key changes with every release a name rule).
3. For each app:
   - **Windows:** skips it when it is already installed at the listed version (the product code under the Windows Uninstall registry keys; or, when the list has a name rule, an Uninstall entry whose DisplayName starts with the rule and whose Publisher is equal, in HKLM 64-bit, HKLM 32-bit or HKCU; an older version is updated, a version that cannot be read is updated too); otherwise downloads the file **from the vendor's own https address only**, checks its SHA-256 against the vendor's, and runs it with the silent switch. If the SHA-256 does not match, the file is **not run**, it is deleted and the window says so.
   - **Linux:** installs it from Flathub with Flatpak, or updates it when Flatpak already has it.
4. Shows a plain progress window: one line per app, a link to the log, a Close button and a Privacy link.
5. Sends **one anonymous count** at the end: how many apps worked and how many did not. No name, IP-linked id, app list or computer detail is sent. Tick "Don't send" in the window, or use `-no-report`, to send nothing (see Privacy).

An installer that shows its own setup window or hangs is stopped after a time limit (10 minutes, `-app-timeout`) and marked "Skipped: needs manual install", with a link to the app's Fileion download page. The SHA-256 check always runs first, before anything is started.

Run the same file again later and it updates the same apps.

Windows asks for administrator permission once at the start, so every app installs for everyone on the PC and none asks again. On Linux, some desktops need "Allow executing file as program" ticked in the file's Properties once; Flatpak must be installed.

## Privacy

Two requests go to `software.fileion.com`, and each app is then downloaded from its vendor.

- **The app list.** The installer asks for your list. The set code (stamped in the file, or in its name) is part of that request's address and stands for the apps you picked, so, like with any page request, Fileion's server can tell which set was fetched.
- **One anonymous count** at the end of a run: how many apps worked and how many did not. The count carries no set code, app name, computer or user identifier.

The progress window shows the count in one line, with a **Don't send** box and a Privacy link. It is sent by default, only when the run ends, and only if the box is not ticked. To turn it off for good, run the file with `-no-report`. Full policy: [fileion.com/privacy-policy](https://fileion.com/privacy-policy).

## Code signing policy

Releases are currently unsigned, so Windows SmartScreen may warn on first run (choose "More info", then "Run anyway"). They are built reproducibly by GitHub Actions from this repository: the `.exe` and the Linux binary are reproducible (see Build), so anyone can compare their SHA-256 with their own build; the AppImage packing may differ byte for byte.

## Build

Needs only Go (the version in `go.mod` or newer). No third-party modules.

```sh
go vet ./... && go test ./...
./build.sh 1.0.0        # dist/Fileion-Installer.exe, dist/Fileion-Installer, dist/SHA256SUMS
```

The `.exe` and the Linux binary build reproducibly: `CGO_ENABLED=0`, `-trimpath`, `-buildvcs=false`, no build ID, and `SOURCE_DATE_EPOCH` fixed. The same source with the same Go version gives the same bytes, so anyone can rebuild and compare SHA-256.

The window is a small page served on `127.0.0.1` (random port and path, Host-checked) and opened in its own app-style window of Edge (Windows) or a Chromium browser (Linux; the default browser otherwise). That keeps the program free of GUI libraries.

### Build pipeline

`.github/workflows/build.yml` builds, tests and packs both files with GitHub's own actions only. The `.exe` and the Linux AppImage are not signed.

Evidence runs on a real Windows machine: `.github/workflows/windows-test.yml` (manual) runs the installer against a local test manifest (install, re-run, wrong SHA-256, hung installer, no admin) and silent-installs a list of vendor installers, reporting for each whether it shows in the Uninstall registry.

### Stamped files

Fileion's server downloads a file with the set code appended: the release file plus 32 bytes (`internal/stamp`; layout and the reasoning, including later code signing, in [docs/STAMPING.md](docs/STAMPING.md)). The release file and `SHA256SUMS` are the unstamped build. To compare a stamped download with `SHA256SUMS`, remove the stamp and hash the copy:

```
go run ./tools/stamp -show "My special bundle.exe"             # prints the code
go run ./tools/stamp -strip "My special bundle.exe" plain.exe  # the release file again
```

The Windows icon and version info (Fileion mark, "Fileion Installer", the tag as the version) are built into the .exe by `build.sh` with [go-winres](https://github.com/tc-hib/go-winres) pinned at v0.3.3; the icon is drawn by `tools/mkicon` into `winres/icon.ico`. The stamp is added after the build and touches none of it.

### Options

| Flag | Meaning |
|---|---|
| `-code` | Use this set code instead of the stamped one or the one in the file name |
| `-base` | Server for the list and the count (default `https://software.fileion.com`) |
| `-no-window` | Print progress in the terminal |
| `-app-timeout` | Longest one installer may run (default 10m) |
| `-no-report` | Do not send the anonymous count (the window's "Don't send" box does the same for one run) |

## Known limits

- Apps whose installer is MSIX, a zip or a portable file, or has no silent switch, are not run: the window sends you to the app's Fileion page.
- On Windows an app with neither a product code nor a name rule in the list cannot be told apart from "not installed", so it is installed again on a re-run.
- The list is asked for once more after 3 seconds when Fileion does not answer or does not know the code yet; after that the error is shown.
- Tested with Go unit tests (hash mismatch, skip-if-installed, manifest parsing, version compare, window). The Windows registry check and the permission prompt need a run on a real Windows PC.

## Clean Windows VM test checklist

1. Windows 10/11 VM, no admin user logged in is fine (the prompt appears). Download a file from the page once the switch is on, or rename a build to `Fileion-7-Zip-<code>.exe` with a real code.
2. Run it: expect the permission prompt, then the window listing each app. Check: installed, "Up to date" on a second run, a deliberately wrong SHA-256 shows "Skipped" and nothing runs, an app with its own setup window is stopped at the limit (try `-app-timeout 30s`) with a Fileion link.
3. Check `%TEMP%\fileion-installer.log`, that downloaded files are gone from `%TEMP%`, and that SmartScreen shows its "unrecognized app" warning (the build is unsigned).
4. Edge must exist (it ships with Windows 10/11); without it the default browser opens the page.
