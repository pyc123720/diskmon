# Build diskmon on Windows.
#   ./build.ps1                 build app -> dist/diskmon-go.exe
#   ./build.ps1 -Installer      also build dist/diskmon-setup.exe (prepares payload)
#   ./build.ps1 -SkipTests      skip go test
#   ./build.ps1 -Version 2.6.5  stamp the version the binary reports
#
# 版本号必须注入，否则 exe 会一直自称 main.go 里那个占位值——用户贴 diag 报 bug 时，
# 我们分不清他跑的是哪个构建。默认 "dev"，正式发布时显式传 -Version。
param(
    [switch]$Installer,
    [switch]$SkipTests,
    [string]$Version = 'dev'
)
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw 'Go toolchain not found in PATH (need Go 1.27+).'
}
Write-Host ('Go: ' + (go version))

if (-not $SkipTests) {
    Write-Host '== go vet =='
    go vet .
    Write-Host '== go test =='
    go test .
}

New-Item -ItemType Directory -Force dist | Out-Null
Write-Host '== build app =='
go build -trimpath -ldflags "-H windowsgui -s -w -X main.version=$Version" -o dist/diskmon-go.exe .
if ($LASTEXITCODE -ne 0) { throw 'build failed' }

if ($Installer) {
    Write-Host '== prepare installer payload =='
    New-Item -ItemType Directory -Force installer/payload | Out-Null
    Copy-Item dist/diskmon-go.exe installer/payload/diskmon-go.exe -Force
    Copy-Item diskmon.ico installer/payload/icon.ico -Force
    Copy-Item README.md installer/payload/README.md -Force

    $cer = Join-Path $env:LOCALAPPDATA 'diskmon-signing\diskmon-codesign.cer'
    if (Test-Path $cer) {
        Copy-Item $cer installer/payload/diskmon-codesign.cer -Force
    } elseif (-not (Test-Path installer/payload/diskmon-codesign.cer)) {
        # optional signer certificate; empty file keeps the embed happy
        Set-Content -LiteralPath installer/payload/diskmon-codesign.cer -Value '' -NoNewline
    }

    Write-Host '== build installer =='
    go build -trimpath -ldflags '-H windowsgui -s -w' -o dist/diskmon-setup.exe ./installer
    if ($LASTEXITCODE -ne 0) { throw 'installer build failed' }
}

Write-Host '== done =='
Get-ChildItem dist | Select-Object Name, @{n = 'MB'; e = { [math]::Round($_.Length / 1MB, 2) } } | Format-Table -AutoSize
