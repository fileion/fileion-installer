# Releasing

Do not add Windows or macOS runners; Windows tests run on the VM.

GitHub Actions has no free minutes to spare: `build.yml` runs only on a `v*` tag or by hand (`gh workflow run build`), on Ubuntu.

## Before tagging: the Windows VM

1. Make sure the VM answers: `ssh fileion-winvm hostname` (if not, the owner restarts the VM).
2. From a clean checkout of the release candidate: `bash scripts/test-on-vm.sh`. It copies the source to the VM, runs `go vet` and `go test` on Windows, builds `Fileion-Installer.exe` and prints its SHA-256.
3. By hand on the VM, the cases in `tools/windows-test.reference.yml` (the old workflow, kept as a reference): the installer against the local test manifest, the window size after an old run (test on a PC that ran an older version), wait-detect, and the type-default list in `testlist/type-default.tsv`.
4. Tag `vX.Y.Z` and push the tag; check the release's `SHA256SUMS` against the files.
