# Installer stamping (v0.2.0): design note

Problem: the installer reads its set code only from its file name. A download named `Fileion-Installer.exe` (or any renamed file) has no code and exits silently. Goal: rename the file freely, keep it on a pendrive, run it on many PCs.

## 1. Where the code lives: a 32-byte trailer at the end of the file

Appended by srv1 to the pristine release file when someone downloads a set. Fixed layout, last 32 bytes:

| Bytes | Content |
|---|---|
| 0-15 | the set code (8-12 hex chars), padded with 0x00 |
| 16 | code length n (8..12) |
| 17 | format version (1) |
| 18-21 | CRC-32 (IEEE, big endian) of bytes 0-17 |
| 22-31 | magic `FILEIONSET` (ASCII) |

The installer reads the last 32 bytes (seek from the end; no scan, no parse of the exe), checks magic, version, length, that the code is `[a-f0-9]{8,12}`, and the CRC. Anything else = "no stamp", never a guess. The CRC is only a corruption check (a truncated copy, a pendrive error), not security: the code is public (it is in the manifest URL) and the manifest is checked by the installer exactly as before (https URL, vendor SHA-256).

Order of lookup: `-code` flag (explicit, terminal use) > stamp in the file > code in the file name > nothing. (DH asked trailer, name, flag; I put the explicit flag first because someone who types `-code` means it. Say if you want it last: one line.)

Which file: `os.Executable()`. On Linux inside an AppImage `os.Executable()` is the mounted copy inside the image, not the AppImage file, so the installer also tries `$APPIMAGE` (set by the AppImage runtime) first. This also fixes the file-name lookup under AppImage (it read the inner binary's name before).

## 2. No code at all: a window, not stderr

`This file has no app list. Get your installer from software.fileion.com/setup` with a link button, in the same small window the installer already uses (a double-clicked exe has no console, so stderr was invisible: that is the silent exit). The log notes it. Exit code 2. No elevation prompt and no network call in this case.

## 3. Server (fileion3.x, `/setup/installer/{os}`)

- srv1 keeps the current release asset and its `SHA256SUMS` in `storage/app/installer/`. New `php artisan setup:installer-sync {tag?}` downloads the three files from the GitHub release (latest if no tag), checks each file against SHA256SUMS and only then moves it into place (atomic rename). A failed check leaves the old files untouched.
- On every download the controller re-checks the file against `SHA256SUMS` (result remembered per mtime+size, so it hashes a file once, not per request). Missing sums file, no line for the file, or a different hash: nothing is served, `Log::error`, and the visitor gets a 503 with a clear message and the command alternative. Gate off (`SETUP_INSTALLER_ENABLED`) or no file: 404 as now.
- It streams the asset, then the 32 trailer bytes (nothing is held in memory or written to disk), with `Content-Length` = size + 32, `Content-Disposition` with the friendly name (the existing `Fileion-<apps>-<code>.exe`), `Cache-Control: no-store`. A streamed response is never stored by the response cache, and `no-store` keeps Cloudflare from caching a per-set file. No change to the route line, so no clash with #1200 (manifest cache).
- The release file is the unstamped build: `SHA256SUMS` keeps describing it. A stamped download is that file + 32 bytes; `tools/stamp -show` and `-strip` (in the installer repo) read and remove the stamp, so anyone can compare with `SHA256SUMS`. README says so.
- Linux AppImage: appending 32 bytes after the end of the squashfs image is fine for the type-2 runtime (it uses the offset in the ELF header, the squashfs ignores trailing bytes). CI proves it on every build: stamp the AppImage, run it with `--appimage-extract-and-run`, assert the log says the stamped code.

## 4. Authenticode later (what stays compatible)

Unsigned today, so the trailer sits plainly at the end. Once the .exe is signed, bytes appended after the certificate table are hashed by Authenticode and would break the signature, so the signed range must not move. The way (used by Google's installer tagging): put the stamp INSIDE the certificate table, after the PKCS#7 blob: raise the WIN_CERTIFICATE `dwLength` and the certificate-table size in the PE optional header by the stamp size (8-byte aligned), leaving everything that is hashed untouched. The certificate table is the last thing in the file, so the stamp is still the last 32 bytes and the Go reader needs no change. The server would do those two length patches before appending; they are the only PE-aware code, about 40 lines, written when a signed build exists (not in this PR). Alternative if ever needed: store the code as an unauthenticated attribute of the signature; same property, more fragile to parse. The signature is made once per release on the pristine file, never per download.

## 5. Icon and version info (go-winres, approved)

- Pinned: `go run github.com/tc-hib/go-winres@v0.3.3` in `build.sh` (CI uses `build.sh`, so one place). v0.3.3 is the newest release, published 2024-04-11 (well over two weeks ago). `go run pkg@version` needs no go.mod change, no global install, and the Go module proxy and checksum database verify what is downloaded.
- Icon: the Fileion mark (purple rounded square, white N: the first three paths of `public/assets/images/logo/logo.svg` in fileion3.x), drawn by a small stdlib Go tool (`tools/mkicon`, committed with its output) into `winres/icon.ico` at 16/32/48/256 px. Committed to the repo; nothing is downloaded for it.
- Version info: ProductName "Fileion Installer", CompanyName "Fileion", FileVersion/ProductVersion from the tag (`v0.2.0` becomes `0.2.0.0`; a non-numeric build such as `dev` becomes `0.0.0.0`), LegalCopyright "© Fileion", OriginalFilename "Fileion-Installer.exe".
- The `.syso` is generated by `build.sh` on every build (not committed), only for windows/amd64; Linux builds do not see it. CI's existing "build twice and compare" proves the output is reproducible with it.
- The stamp trailer is appended to the finished file after all of this. It touches no resource and no PE header, so icon and version info are unaffected.

## 6. Tests

Installer: valid trailer; renamed file (stamp wins over the name); no trailer (falls back to the name, then to "no code"); corrupt trailer (bad CRC, wrong magic, bad length, non-hex code, truncated file); `$APPIMAGE` path; the no-code window. Server: stamped bytes = asset + valid trailer (byte compare, the same CRC); bad SHA serves nothing and logs; missing sums serves nothing; gate off gives 404; headers (`no-store`, Content-Length, disposition).

## 7. Connected

| Surface | Change? |
|---|---|
| /installer-help page | Yes: "rename it freely; keep it on a pendrive; the list is stored inside the file" (owner decides wording) |
| /setup notice | Maybe: one clause is enough; owner decides |
| Installer README | Yes: stamping, `tools/stamp`, comparing with SHA256SUMS |
| Privacy text | No: the code was already in the request (the manifest URL); nothing new leaves the PC |
| llms.txt | No (it points at /setup, not at the file) |
| windows-test workflow | Yes: renamed-file and pendrive cases (stamp, copy elsewhere, run twice) |
| v0.2.0 release | Needs the owner's yes in the DH chat; not done here |
| Old v0.1.0 downloads | Still work through the file name; the server serves v0.2.0 only after the sync |

## 8. Cost and order
Installer PR then fileion3.x PR. About 0.7% (Sonnet) for both plus QA ~0.3%. Nothing is released or deployed by these PRs.

## 9. v0.2.0 additions (friend-PC test, QA on #1200, the match plan)

**File-name fallback.** The code is read from the name only when there is no stamp, and now copes with what a rename or a second download does: repeated extensions (`….exe.exe` from Explorer with extensions hidden, `.EXE`), copy suffixes (` (1)`, `(2)`) and spaces, in any combination. The probable cause of "instantly closes" on the friend's PC was the name `…-36ee3881.exe.exe`: the code read as `36ee3881.exe` and the error went to stderr of a console that closed at once.

**Early failures are never only stderr.** Before the progress window exists (no code, unsupported OS or processor, the window could not start): a Windows message box (user32 `MessageBoxW` through `syscall`, no new package) with the message, the help address and "Open the help page now?" (Yes opens https://fileion.com/installer-help); on Linux the Fileion window with the message and links. After the window exists, a manifest failure (no network, 404, a reply that is not JSON) is shown in the window, which links Help; if the window itself could not be opened on Windows, the same text goes to a message box. stderr and `%TEMP%\fileion-installer.log` (`/tmp/fileion-installer.log` on Linux) always get the line too.

**Console or windowsgui: console, on purpose.** `-H windowsgui` would remove the console flash for a double click, but a GUI-subsystem program has no stdout: PowerShell's `&` does not wait for it or fill `$LASTEXITCODE`, so the windows-test cases (and anyone running `fileion-installer.exe -code …` from Command Prompt to see what it prints, which is what the owner is about to do on the friend's PC) would see nothing. Instead the program closes its own console when it is the only process in it (`GetConsoleProcessList` = 1, which is what a double click gives); run from a terminal the console is shared and stays. Every early error has a message box, so nothing depends on the console being visible.

**ARM64.** Windows 11 on ARM runs x64 (amd64) programs under its built-in emulation, so the one `windows/amd64` exe runs there and no `windows/arm64` asset is shipped: it would need a second signed/stamped file and a choice by the browser's architecture for no gain. A processor that is neither amd64 nor arm64 gets the message box. The apps themselves are installed from the list's x64 rows; they also run emulated. If the friend's PC is ARM64 and the exe still closes at once, the first thing to read is the message box and the log (the file name was the likelier cause).

**Manifest fetch retries once.** A network error, a 5xx or a 404 is tried again after 3 seconds (a set created a moment ago, a brief blip); any other answer is final. Then the error shows as above.

**Match rule instead of a key.** Some apps change their Uninstall key name with every release (Opera, NetBeans, PowerToys) or never had one captured (Git). The list may carry `match: {name, publisher}` (only when both are set). The installer first looks for the key as before; if that is not there it reads every Uninstall entry in HKLM 64-bit, HKLM 32-bit and HKCU and takes those whose DisplayName starts with `name` (case-insensitive) AND whose Publisher equals `publisher` (case-insensitive), highest version if several. Then: installed version equal or newer than the list's → skip; older → update; not found → install. Versions compare as dotted numbers (`25` < `25.0.1`, four parts, `117.0.5368.49` > `117.0.5368.5`); a version that cannot be read is updated, never skipped. It only reads the registry and sends nothing.
