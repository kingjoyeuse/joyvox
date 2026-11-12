# Dot-source this file to use Go in the current process only.
$ErrorActionPreference = 'Stop'
$joyRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$joyToolRoot = Join-Path $joyRoot '.joyvox\toolchains'
$joyGo = Join-Path $joyToolRoot 'go\bin\go.exe'
if (-not (Test-Path -LiteralPath $joyGo)) {
    $existingGo = Get-Command go.exe -ErrorAction SilentlyContinue
    if ($existingGo) {
        $joyGo = $existingGo.Source
    } else {
        $releases = Invoke-RestMethod 'https://go.dev/dl/?mode=json'
        $stable = $releases | Where-Object stable | Select-Object -First 1
        $archive = $stable.files | Where-Object { $_.os -eq 'windows' -and $_.arch -eq 'amd64' -and $_.kind -eq 'archive' } | Select-Object -First 1
        if (-not $archive -or $archive.sha256 -notmatch '^[a-f0-9]{64}$' -or $archive.filename -notmatch '^go[0-9.]+\.windows-amd64\.zip$') {
            throw 'No verified Windows x64 Go archive was supplied by go.dev.'
        }
        New-Item -ItemType Directory -Path $joyToolRoot -Force | Out-Null
        $download = Join-Path $joyToolRoot $archive.filename
        try {
            Write-Host "Downloading $($archive.filename) from go.dev..."
            Invoke-WebRequest -UseBasicParsing -Uri "https://go.dev/dl/$($archive.filename)" -OutFile $download
            if ((Get-FileHash -LiteralPath $download -Algorithm SHA256).Hash -ne $archive.sha256) {
                throw 'Go download checksum mismatch. The archive was not executed.'
            }
            Expand-Archive -LiteralPath $download -DestinationPath $joyToolRoot -Force
        } finally {
            Remove-Item -LiteralPath $download -Force -ErrorAction SilentlyContinue
        }
    }
}
$env:PATH = (Split-Path $joyGo) + ';' + $env:PATH
$env:GOTOOLCHAIN = 'local'
$env:GOCACHE = Join-Path $joyToolRoot 'build-cache'
& $joyGo version
if ($LASTEXITCODE -ne 0) { throw 'Go could not run.' }
