# Build a separate development launcher, leaving published releases intact.
[CmdletBinding()]
param()
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'setup-go.ps1')
Push-Location $joyRoot
try {
    & python.exe tools/build_release.py --resources-only
    if ($LASTEXITCODE -ne 0) { throw 'Resource generation failed.' }
    $env:GOOS = 'windows'
    $env:GOARCH = 'amd64'
    $env:CGO_ENABLED = '0'
    $env:GOPROXY = 'off'
    $env:GOSUMDB = 'off'
    & $joyGo test ./gui
    if ($LASTEXITCODE -ne 0) { throw 'Launcher tests failed.' }
    New-Item -ItemType Directory -Path build -Force | Out-Null
    $candidate = Join-Path $joyRoot 'build\JoyVox-dev.next.exe'
    & $joyGo build -trimpath -ldflags '-s -w -H=windowsgui' -o $candidate ./gui
    if ($LASTEXITCODE -ne 0) { throw 'Launcher build failed.' }
    $target = Join-Path $joyRoot 'JoyVox-dev.exe'
    Move-Item -LiteralPath $candidate -Destination $target -Force
    $shell = New-Object -ComObject WScript.Shell
    $shortcut = $shell.CreateShortcut((Join-Path $joyRoot 'JoyVox - Latest Build.lnk'))
    $shortcut.TargetPath = $target
    $shortcut.WorkingDirectory = $joyRoot
    $shortcut.Description = 'Latest local JoyVox development build'
    $shortcut.Save()
    Write-Host "Built $target"
} finally {
    Pop-Location
}
