#requires -Version 5.1
[CmdletBinding()]
param()
$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
if (-not (Get-Command go.exe -ErrorAction SilentlyContinue)) {
    throw 'Install Go 1.23 or later to rebuild the launcher, or use the included GitHub Actions workflow. Running joyvox.exe does not need Go.'
}
$python = $null
# No python3: Windows may redirect that alias to the Microsoft Store.
foreach ($command in @(Get-Command python.exe -All -CommandType Application -ErrorAction SilentlyContinue)) {
    if ($command.Source -notmatch '(?i)\\Microsoft\\WindowsApps\\python\.exe$') { $python = $command.Source; break }
}
if (-not $python) {
    $launcher = Get-Command py.exe -ErrorAction SilentlyContinue
    if ($launcher -and $launcher.Source -notmatch '(?i)\\Microsoft\\WindowsApps\\py\.exe$') {
        foreach ($line in @(& $launcher.Source -0p 2>$null)) {
            $m = [regex]::Match([string]$line, '(?i)((?:[a-z]:\\|\\\\)[^\r\n]*?python\.exe)\s*$')
            if ($m.Success -and (Test-Path -LiteralPath $m.Groups[1].Value -PathType Leaf)) { $python = $m.Groups[1].Value; break }
        }
    }
}
if (-not $python) { throw 'Python 3.10 or later is needed to build the resource/package files. Install Python, or use GitHub Actions.' }
Push-Location $root
try {
    & $python (Join-Path $PSScriptRoot 'build_release.py')
    if ($LASTEXITCODE -ne 0) { throw 'Build failed. See the error above.' }
} finally { Pop-Location }
Write-Host 'Release files are in release\.'
