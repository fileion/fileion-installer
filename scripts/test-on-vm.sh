#!/usr/bin/env bash
# Runs the Windows checks on the Windows VM over ssh (host alias fileion-winvm). Run once per release candidate, before tagging.
# Needs on the VM: Go (version in go.mod) and PowerShell. Nothing is installed or changed on fileion.com.
set -euo pipefail
cd "$(dirname "$0")/.."
VM=${VM:-fileion-winvm}
DIR='C:\ftest\src'
rm -f /tmp/fileion-installer-src.tar
git archive --format=tar -o /tmp/fileion-installer-src.tar HEAD
scp /tmp/fileion-installer-src.tar "$VM:C:/ftest/src.tar"
ssh "$VM" "powershell -NoProfile -Command \"Remove-Item -Recurse -Force $DIR -ErrorAction SilentlyContinue; New-Item -ItemType Directory -Force $DIR | Out-Null; tar -xf C:\\ftest\\src.tar -C $DIR; Set-Location $DIR; go vet ./...; if (\$LASTEXITCODE) { exit 1 }; go test ./...; if (\$LASTEXITCODE) { exit 1 }; go build -ldflags='-H=windowsgui' -o C:\\ftest\\Fileion-Installer.exe .; (Get-FileHash C:\\ftest\\Fileion-Installer.exe -Algorithm SHA256).Hash\""
echo "Vet, tests and build passed on the VM. Now run the by-hand cases in RELEASING.md."
rm -f /tmp/fileion-installer-src.tar
