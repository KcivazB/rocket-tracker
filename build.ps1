# Builds dist\rltracker.exe (GUI subsystem: no console window when started at login).
# Usage:  .\build.ps1          -> release build (dist\rltracker.exe)
#         .\build.ps1 -Dev     -> console build for debugging (dist\rltracker-dev.exe)
#         .\build.ps1 -Test    -> go vet + go test before building
param(
    [switch]$Dev,
    [switch]$Test,
    [string]$Version = "0.1.0"
)
$ErrorActionPreference = "Stop"
Set-Location -Path $PSScriptRoot

$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
$env:GOARCH = "amd64"

if ($Test) {
    go vet ./...
    if ($LASTEXITCODE -ne 0) { throw "go vet failed" }
    go test ./...
    if ($LASTEXITCODE -ne 0) { throw "go test failed" }
}

New-Item -ItemType Directory -Force -Path dist | Out-Null

if ($Dev) {
    go build -trimpath -ldflags "-X main.Version=$Version" -o dist\rltracker-dev.exe .
    if ($LASTEXITCODE -ne 0) { throw "build failed" }
    Write-Host "built dist\rltracker-dev.exe (console)"
} else {
    go build -trimpath -ldflags "-H windowsgui -s -w -X main.Version=$Version" -o dist\rltracker.exe .
    if ($LASTEXITCODE -ne 0) { throw "build failed" }
    $size = [math]::Round((Get-Item dist\rltracker.exe).Length / 1MB, 1)
    Write-Host "built dist\rltracker.exe ($size MB, windowsgui)"
}
