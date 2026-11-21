# Fileion Installer

A small program that installs the apps you picked at [software.fileion.com/setup](https://software.fileion.com/setup), one after another, the way Ninite does. Free, MIT licensed, no ads, no extras.

## What it does

1. Reads the short code of your list: first the code Fileion stamped inside the file (the last 32 bytes), so you can rename the file freely (`My special bundle.exe`), keep it on a pendrive and run it on many PCs; then, for older downloads, the code in the file name (`Fileion-VLC-7-Zip-and-4-more-3fa91c0b.exe` → `3fa91c0b`). A file with no code (or any other failure before the window opens) says so in a message box on Windows, or in the Fileion window on Linux, with a link to software.fileion.com/setup or the help page; the log is `%TEMP%\fileion-installer.log` (`/tmp/fileion-installer.log` on Linux).
2. Gets the app list for that code from `https://software.fileion.com/setup/manifest/{code}` (JSON: vendor URL, SHA-256, how the app is handled (`mode`), silent switch, minimum version, product code, for a zip the program inside it, and for apps whose Uninstall key changes with every release a name rule).
3. For each app:
   - **Windows:** every app is downloaded **from the vendor's own https address only** and its SHA-256 is checked against the vendor's before anything is run or unpacked; if it does not match, the file is **not used**, it is deleted and the window says so. What happens next depends on the app's `mode`:
     - **silent** (the usual case): skipped when already installed at the listed version (the product code under the Windows Uninstall registry keys; or, when the list has a name rule, an Uninstall entry whose DisplayName starts with the rule and whose Publisher is equal, in HKLM 64-bit, HKLM 32-bit or HKCU; an older version is updated, a version that cannot be read is updated too); otherwise it runs with the silent switch.
     - **portable** (a program that needs no installer): saved in `Downloads\Fileion Apps\<App>` and **never run by the installer**. The row offers Run (started as you, not as administrator), Add to Start menu and Move…. The installer keeps its own small list of these apps (name, version, folder) in `%LOCALAPPDATA%\Fileion\portable.json`; it stays on the PC. A re-run skips an app that is already at the listed version or newer.
     - **zip**: the zip is verified, unpacked into the same kind of folder (an entry that would land outside the folder fails the unpacking), and the zip is deleted. The row offers Open folder, Add to Start menu (when the program inside is known) and Move….
     - **interactive** ("A few clicks", for an installer with no silent switch): run **last**, after every other app. The window comes to the front, the taskbar button flashes and the row says "Your turn: finish {App}'s setup window" with a hint to untick any extra offers. The setup runs in a Windows job object, so a setup that starts the real setup and exits is still waited for. When it has ended, the installed-programs check decides: found is Done; not found is "Not finished" with Retry and Skip. Skip (or Stop) always works and ends the whole setup. There is no second permission prompt: the setup starts from the already elevated installer.
     - An entry with a mode this program does not know, and an MSIX, APPX or Store package, is not run: the window sends you to the app's Fileion page.
   - **Linux:** installs it from Flathub with Flatpak, or updates it when Flatpak already has it.
4. Shows a plain progress window at once ("Preparing…", before the permission prompt and while the list loads), then one line per app: Waiting, download progress (MB and percent), Verifying SHA-256, Installing with the time, Unpacking, Saved, Done, Up to date, Failed, Skipped, Not run, or "Your turn" / "Not finished" for an app that needs a few clicks. The window fits its content: 720 pixels wide (never under 600), from 330 pixels high up to 80% of the free screen height, and it only grows; past that only the list scrolls. **Cancel** opens one line, "Stop after the current app?" (Stop or Keep going; Esc and the window's close button do the same): the app that is running finishes, the rest are "Not run". When the run is over: Share Fileion Installer (copies the setup page's address), Report a problem (opens the help page's report section), Open log folder, **Retry failed apps** (only the failed ones, with the same checks) and Close. If the installer itself stops, the window says so and shows where the log is.
5. Sends **one anonymous count** at the end: how many apps worked and how many did not. No name, IP-linked id, app list or computer detail is sent. Tick "Don't send" in the window, or use `-no-report`, to send nothing (see Privacy).

A silent installer that does not finish within its time limit (10 minutes, `-app-timeout`) is stopped and marked "Manual" with a link to that app's Fileion download page. An app with no silent switch is not left out: it is an "interactive" app (above), and its own setup gets at least half an hour. An installer's own windows are shown, not hidden, and are never closed by the program before the time limit. If an installer seems to be waiting for you, the window says so after a minute. If the app is already installed but its own installer window stays open, the row says "its installer is still open: you can close it". The SHA-256 check always runs first, before anything is started.

Run the same file again later and it updates the same apps.

Windows asks for administrator permission once at the start, so every app installs for everyone on the PC and none asks again. The first copy of the program shows the "Preparing…" window, starts an elevated copy (the one prompt) and moves the window to it. The Windows file is a GUI program: it never opens a console window. Started from a terminal, `-no-window` prints into that terminal and the prompt returns at once (use `Start-Process -Wait` or `start /wait` to wait for it). On Linux, some desktops need "Allow executing file as program" ticked in the file's Properties once; Flatpak must be installed.

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

`.github/workflows/build.yml` builds, tests and packs both files with GitHub's own actions only; it also checks that the `.exe` is a GUI program (PE subsystem 2, so no console window can appear). The `.exe` and the Linux AppImage are not signed.

Evidence runs on a real Windows machine: `.github/workflows/windows-test.yml` (manual) runs the installer against a local test manifest (install, re-run, wrong SHA-256, hung installer, no admin, no console window, portable, zip, an interactive app that starts its real setup and exits, Not finished and Skip, and the older installer against the new modes). The Windows file is a GUI program, so the cases start it with `Start-Process -Wait` and read its output files and the log and silent-installs a list of vendor installers, reporting for each whether it shows in the Uninstall registry.

### Stamped files

Fileion's server downloads a file with the set code appended: the release file plus 32 bytes (`internal/stamp`; layout and the reasoning, including later code signing, in [docs/STAMPING.md](docs/STAMPING.md)). The release file and `SHA256SUMS` are the unstamped build. To compare a stamped download with `SHA256SUMS`, remove the stamp and hash the copy:

```
go run ./tools/stamp -show "My special bundle.exe"             # prints the code
go run ./tools/stamp -strip "My special bundle.exe" plain.exe  # the release file again
```

The Windows icon and version info (Fileion mark, "Fileion Installer", the tag as the version) are built into the .exe by `build.sh` with [go-winres](https://github.com/tc-hib/go-winres) pinned at v0.3.3; the icon is the Fileion software logo, packed by `tools/mkicon` from `winres/icons/` into `winres/icon.ico`. The stamp is added after the build and touches none of it.

### Options

| Flag | Meaning |
|---|---|
| `-code` | Use this set code instead of the stamped one or the one in the file name |
| `-base` | Server for the list and the count (default `https://software.fileion.com`) |
| `-no-window` | Print progress in the terminal (from a terminal the output appears there; a double click shows nothing) |
| `-app-timeout` | Longest one installer may run (default 10m) |
| `-no-report` | Do not send the anonymous count (the window's "Don't send" box does the same for one run) |

## Known limits

- MSIX, APPX and Microsoft Store packages, and a mode this program does not know, are not run: the window sends you to the app's Fileion page.
- "Run" for a portable app starts it through Explorer so it runs as you; "Add to Start menu" and the folder picker for "Move…" use PowerShell (it ships with Windows).
- The taskbar flash always works; bringing the installer's window to the front can be refused by Windows when another program has the focus.
- A setup started for an interactive app is put in a job object before it runs. If Windows cannot do that, the setup is waited for alone (its child programs are not).
- On Windows an app with neither a product code nor a name rule in the list cannot be told apart from "not installed", so it is installed again on a re-run.
- The list is asked for once more after 3 seconds when Fileion does not answer or does not know the code yet; after that the error is shown.
- Tested with Go unit tests (hash mismatch, skip-if-installed, manifest parsing, version compare, window). The Windows registry check and the permission prompt need a run on a real Windows PC.

## Clean Windows VM test checklist

1. Windows 10/11 VM, no admin user logged in is fine (the prompt appears). Download a file from the page once the switch is on, or rename a build to `Fileion-7-Zip-<code>.exe` with a real code.
2. Run it: expect the "Preparing…" window, then the permission prompt, then the window listing each app (apps that need a few clicks last). Check: installed, "Up to date" on a second run, a deliberately wrong SHA-256 shows "Skipped" and nothing runs, an installer that never finishes is stopped at the limit (try `-app-timeout 30s`) with a Fileion link; an app that installs but leaves its own installer window open is left running, with "its installer is still open: you can close it".
3. Check `%TEMP%\fileion-installer.log`, that downloaded files are gone from `%TEMP%`, and that SmartScreen shows its "unrecognized app" warning (the build is unsigned).
4. Edge must exist (it ships with Windows 10/11); without it the default browser opens the page.
