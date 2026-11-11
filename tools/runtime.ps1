#requires -Version 5.1
<#
JoyVox private runtime installer. No elevation, registry writes, or persistent
PATH changes. Status never downloads a speech model or starts audio/input hooks.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('Status', 'Install', 'InstallGpu')]
    [string]$Action,
    [Parameter(Mandatory = $true)]
    [string]$Root,
    [switch]$WithGpu
)

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'
$Utf8 = New-Object System.Text.UTF8Encoding($false)
[Console]::OutputEncoding = $Utf8
$env:PYTHONIOENCODING = 'utf-8'
$env:PYTHONUTF8 = '1'
$env:PYTHONNOUSERSITE = '1'
$env:PYGAME_HIDE_SUPPORT_PROMPT = '1'
# These changes apply to this installer process and its children only.
Remove-Item Env:PYLAUNCHER_ALLOW_INSTALL -ErrorAction SilentlyContinue
Remove-Item Env:PYLAUNCHER_ALWAYS_INSTALL -ErrorAction SilentlyContinue
$env:PYTHON_MANAGER_AUTOMATIC_INSTALL = 'false'

# SHA256 from Python.org's published SPDX SBOM, also verified against the ZIP.
$PythonUrl = 'https://www.python.org/ftp/python/3.12.10/python-3.12.10-embed-amd64.zip'
$PythonSha256 = '4acbed6dd1c744b0376e3b1cf57ce906f9dc9e95e68824584c8099a63025a3c3'
# This immutable official PyPA commit is byte-identical to bootstrap.pypa.io/get-pip.py
# checked for this release. Pinning avoids a changing bootstrap URL/hash pair.
$GetPipUrl = 'https://raw.githubusercontent.com/pypa/get-pip/8c427468071b5d15717839ffd76c53a96df8b703/public/get-pip.py'
$GetPipSha256 = '356e0d0de21cdc348c3394084d90939b85e4246c4d14c76aee6ca4f46646ea98'
$PipVersion = '26.3'

function Safe-Text([object]$Value) {
    $text = [string]$Value
    $text = [regex]::Replace($text, '(https?://)[^/@\s]+:[^/@\s]+@', '$1[credentials]@')
    return $text.Trim()
}

function Write-Log([object]$Value) {
    [Console]::Out.WriteLine((Safe-Text $Value))
}

function Quote-ProcessArgument([string]$Value) {
    $escaped = [regex]::Replace($Value, '(\\*)"', '$1$1\"')
    $escaped = [regex]::Replace($escaped, '(\\+)$', '$1$1')
    return '"' + $escaped + '"'
}

function Invoke-Captured([string]$Executable, [string[]]$Arguments, [int]$TimeoutSeconds = 90) {
    $info = New-Object System.Diagnostics.ProcessStartInfo
    $info.FileName = $Executable
    $info.Arguments = (($Arguments | ForEach-Object { Quote-ProcessArgument $_ }) -join ' ')
    $info.UseShellExecute = $false
    $info.CreateNoWindow = $true
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    $info.StandardOutputEncoding = $Utf8
    $info.StandardErrorEncoding = $Utf8
    $process = New-Object System.Diagnostics.Process
    $process.StartInfo = $info
    try {
        [void]$process.Start()
        $stdout = $process.StandardOutput.ReadToEndAsync()
        $stderr = $process.StandardError.ReadToEndAsync()
        if (-not $process.WaitForExit($TimeoutSeconds * 1000)) {
            try { $process.Kill() } catch { }
            [void]$process.WaitForExit(2000)
            throw "The runtime check timed out after $TimeoutSeconds seconds. Close JoyVox and try again."
        }
        return [pscustomobject]@{ ExitCode = $process.ExitCode; Stdout = $stdout.Result; Stderr = $stderr.Result }
    }
    finally { $process.Dispose() }
}

function Invoke-PythonStep([string]$Executable, [string[]]$Arguments) {
    if (-not [System.IO.File]::Exists($Executable)) { throw 'The local Python executable is missing.' }
    $oldPreference = $ErrorActionPreference
    try {
        # Windows PowerShell 5.1 wraps native stderr in ErrorRecord objects.
        # Treat those as log text; the native exit code decides success/failure.
        $ErrorActionPreference = 'Continue'
        & $Executable @Arguments 2>&1 | ForEach-Object { Write-Log $_.ToString() }
        $nativeExit = $LASTEXITCODE
    }
    finally { $ErrorActionPreference = $oldPreference }
    if ($nativeExit -ne 0) { throw "Python installation command failed (exit $nativeExit). See its output above." }
}

function Read-JsonOutput($ProcessResult) {
    $lines = @($ProcessResult.Stdout -split '\r?\n' | Where-Object { $_.TrimStart().StartsWith('{') })
    if ($ProcessResult.ExitCode -ne 0 -or $lines.Count -eq 0) {
        throw ('Runtime check failed: ' + (Safe-Text ($ProcessResult.Stderr + ' ' + $ProcessResult.Stdout)))
    }
    return ($lines[-1] | ConvertFrom-Json)
}

function Get-RuntimeInfo([string]$Executable, [string]$ExpectedPrefix = '', [bool]$RequireVenv = $false) {
    if (-not [System.IO.File]::Exists($Executable)) { return $null }
    $probe = 'import json,struct,sys; print(json.dumps(dict(ok=sys.platform=="win32" and sys.implementation.name=="cpython" and (3,10)<=sys.version_info[:2]<=(3,12) and struct.calcsize("P")==8, version=".".join(map(str,sys.version_info[:3])), executable=sys.executable, prefix=sys.prefix, base_prefix=sys.base_prefix)))'
    try {
        $data = Read-JsonOutput (Invoke-Captured $Executable @('-I', '-X', 'utf8', '-c', $probe) 20)
        if (-not $data.ok) { return $null }
        if ($ExpectedPrefix) {
            $actualPrefix = [System.IO.Path]::GetFullPath([string]$data.prefix).TrimEnd('\', '/')
            $wantedPrefix = [System.IO.Path]::GetFullPath($ExpectedPrefix).TrimEnd('\', '/')
            if (-not $actualPrefix.Equals($wantedPrefix, [StringComparison]::OrdinalIgnoreCase)) { return $null }
        }
        if ($RequireVenv -and ([string]$data.prefix).Equals([string]$data.base_prefix, [StringComparison]::OrdinalIgnoreCase)) { return $null }
        return $data
    }
    catch { return $null }
}

function Find-LocalPython {
    foreach ($candidate in @(
        @{Exe=(Join-Path $RuntimeDir 'python.exe'); Prefix=$RuntimeDir; Venv=$false},
        @{Exe=(Join-Path $StateDir 'venv\Scripts\python.exe'); Prefix=(Join-Path $StateDir 'venv'); Venv=$true},
        @{Exe=(Join-Path $RootDir '.venv\Scripts\python.exe'); Prefix=(Join-Path $RootDir '.venv'); Venv=$true}
    )) {
        $data = Get-RuntimeInfo $candidate.Exe $candidate.Prefix $candidate.Venv
        if ($null -ne $data) { return $data }
    }
    return $null
}

function Is-StoreAlias([string]$Path) {
    # Never execute App Installer's python/python3 aliases (they can open Store).
    return $Path -match '(?i)\\Microsoft\\WindowsApps\\(?:python(?:3(?:\.\d+)?)?|py)\.exe$'
}

function Find-InstalledPython {
    $paths = New-Object 'System.Collections.Generic.List[string]'
    # List registered paths without launching a requested version. This cannot
    # trigger the legacy Store installer or the newer manager's automatic install.
    $launchers = @(Get-Command py.exe -All -CommandType Application -ErrorAction SilentlyContinue)
    foreach ($launcher in $launchers) {
        if (Is-StoreAlias $launcher.Source) { continue }
        try {
            $r = Invoke-Captured $launcher.Source @('-0p') 8
            if ($r.ExitCode -eq 0) {
                foreach ($line in ($r.Stdout -split '\r?\n')) {
                    $match = [regex]::Match($line, '(?i)((?:[a-z]:\\|\\\\)[^\r\n]*?python\.exe)\s*$')
                    if ($match.Success) {
                        $candidate = $match.Groups[1].Value.Trim()
                        if (-not (Is-StoreAlias $candidate)) {
                            $data = Get-RuntimeInfo $candidate
                            if ($null -ne $data) { return $data }
                        }
                    }
                }
            }
        } catch { }
    }
    foreach ($command in @(Get-Command python.exe -All -CommandType Application -ErrorAction SilentlyContinue)) {
        if (-not (Is-StoreAlias $command.Source)) { $paths.Add($command.Source) }
    }
    # PEP 514 registry installs, including per-user Python not on PATH.
    foreach ($hive in @('HKCU:\Software\Python\PythonCore', 'HKLM:\Software\Python\PythonCore')) {
        foreach ($version in @('3.12','3.11','3.10')) {
            try {
                $key = Get-Item -LiteralPath "$hive\$version\InstallPath" -ErrorAction Stop
                $exe = [string]$key.GetValue('ExecutablePath', '')
                if (-not $exe) { $exe = Join-Path ([string]$key.GetValue('')) 'python.exe' }
                if ($exe) { $paths.Add($exe) }
            } catch { }
        }
    }
    foreach ($candidate in @($paths | Select-Object -Unique)) {
        if (Is-StoreAlias $candidate) { continue }
        $data = Get-RuntimeInfo $candidate
        if ($null -ne $data) { return $data }
    }
    return $null
}

function Find-UsablePython {
    $local = Find-LocalPython
    if ($null -ne $local) { return $local }
    return (Find-InstalledPython)
}

function Install-LocalVenv($InstalledPython) {
    $destination = Join-Path $StateDir 'venv'
    if ([IO.Directory]::Exists($destination)) {
        # venvs contain absolute paths, so create at the final path, never rename
        # a completed venv into place. Preserve interrupted state for diagnosis.
        $backup = $destination + '.previous-' + [guid]::NewGuid().ToString('N')
        [IO.Directory]::Move($destination, $backup)
        Write-Log ('Preserved incomplete local environment: ' + $backup)
    }
    Write-Log ('Reusing installed Python ' + $InstalledPython.version + '; creating an isolated JoyVox environment. No global packages will be changed.')
    Invoke-PythonStep $InstalledPython.executable @('-I','-X','utf8','-m','venv',$destination)
    $data = Get-RuntimeInfo (Join-Path $destination 'Scripts\python.exe') $destination $true
    if ($null -eq $data) { throw 'Could not verify the isolated environment created from installed Python.' }
    return $data
}

function Assert-SupportedPlatform {
    if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT -or -not [Environment]::Is64BitOperatingSystem) {
        throw 'JoyVox recognition requires 64-bit Intel/AMD Windows 10 or 11. No downloads were started.'
    }
    $arch = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
    if ($arch -ne 'AMD64') { throw 'This recognition package supports Intel/AMD x64 Windows, not native ARM64 or 32-bit Windows. No downloads were started.' }
    $version = Get-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion'
    $build = [int]$version.CurrentBuildNumber
    if ($build -lt 10240) {
        throw 'This JoyVox dependency set requires Windows 10 or newer. Windows 7/8/8.1 are not compatible with the complete recognition stack; installing an older Python alone would not fix that. No downloads were started.'
    }
    Write-Log ('OS compatibility: Windows build ' + $build + ', Intel/AMD x64. Compatible Python builds: 3.10-3.12.')
}

function Support-File([string]$PrivateRelative, [string]$SourceRelative) {
    $private = Join-Path $StateDir $PrivateRelative
    if ([System.IO.File]::Exists($private)) { return $private }
    $source = Join-Path $RootDir $SourceRelative
    if ([System.IO.File]::Exists($source)) { return $source }
    throw "JoyVox support file is missing: $SourceRelative. Restart JoyVox.exe to restore its private support files."
}

function Missing-Status([string]$Summary) {
    return [pscustomobject]@{ python = 'missing'; core = 'missing'; gpu = 'missing'; python_exe = ''; summary = $Summary; diagnostics = @() }
}

function Current-Status {
    $runtime = Find-UsablePython
    if ($null -eq $runtime) { return (Missing-Status 'No compatible 64-bit Python 3.10-3.12 was found locally, through py.exe, python.exe, or registered installations. Install / repair will download a private compatible runtime.') }
    $checker = Support-File 'tools\check_dependencies.py' 'tools\check_dependencies.py'
    try {
        $check = Invoke-Captured $runtime.executable @('-I', '-X', 'utf8', '-u', $checker, '--root', $RootDir, '--json') 90
        $status = Read-JsonOutput $check
        if ($check.Stderr.Trim()) { $status.diagnostics += (Safe-Text $check.Stderr) }
        return $status
    }
    catch {
        return [pscustomobject]@{ python = 'ready'; core = 'error'; gpu = 'error'; python_exe = $runtime.executable;
            summary = 'The dependency check could not complete. Click Install dependencies to repair the local runtime.';
            diagnostics = @((Safe-Text $_.Exception.Message)) }
    }
}

function Write-Status($Status) {
    [void][System.IO.Directory]::CreateDirectory($StateDir)
    $lines = foreach ($key in @('python', 'core', 'gpu', 'python_exe', 'summary')) {
        $value = [regex]::Replace([string]$Status.$key, '[\r\n]+', ' ')
        $key + '=' + (Safe-Text $value)
    }
    $path = Join-Path $StateDir 'dependency-status.ini'
    $temporary = $path + '.' + [guid]::NewGuid().ToString('N') + '.tmp'
    try {
        [System.IO.File]::WriteAllText($temporary, (($lines -join "`r`n") + "`r`n"), $Utf8)
        if ([System.IO.File]::Exists($path)) { [System.IO.File]::Replace($temporary, $path, [NullString]::Value) }
        else { [System.IO.File]::Move($temporary, $path) }
    }
    finally { if ([System.IO.File]::Exists($temporary)) { [System.IO.File]::Delete($temporary) } }
}

function Show-Status($Status) {
    Write-Log $Status.summary
    if ($Status.python_exe) { Write-Log ('Python: ' + $Status.python_exe) }
    foreach ($message in $Status.diagnostics) { Write-Log $message }
}

function Download-Verified([string]$Url, [string]$Destination, [string]$Sha256, [string]$Label) {
    if ([System.IO.File]::Exists($Destination)) {
        if ((Get-FileHash -LiteralPath $Destination -Algorithm SHA256).Hash -eq $Sha256) {
            Write-Log ("Using verified cached " + $Label + '.')
            return
        }
        Write-Log ("Cached " + $Label + ' is incomplete or changed; downloading a verified copy.')
    }
    [void][System.IO.Directory]::CreateDirectory([System.IO.Path]::GetDirectoryName($Destination))
    $partial = $Destination + '.' + [guid]::NewGuid().ToString('N') + '.part'
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
    try {
        for ($attempt = 1; $attempt -le 2; $attempt++) {
            $response = $null; $downloadStream = $null; $downloadFile = $null
            try {
                Write-Log ("Downloading " + $Label + " (attempt $attempt of 2)...")
                $request = [Net.HttpWebRequest]::Create($Url)
                $request.Timeout = 30000
                $request.ReadWriteTimeout = 30000
                $request.UserAgent = 'JoyVox/4.2.1 runtime installer'
                $response = $request.GetResponse()
                $downloadStream = $response.GetResponseStream()
                $downloadFile = [System.IO.File]::Create($partial)
                $buffer = New-Object byte[] 65536
                $received = 0L
                $timer = [Diagnostics.Stopwatch]::StartNew()
                $lastProgress = -2.0
                while (($count = $downloadStream.Read($buffer, 0, $buffer.Length)) -gt 0) {
                    $downloadFile.Write($buffer, 0, $count)
                    $received += $count
                    if ($timer.Elapsed.TotalSeconds - $lastProgress -ge 1.5) {
                        $megabytes = [math]::Round($received / 1MB, 1)
                        $total = if ($response.ContentLength -gt 0) { ' / ' + [math]::Round($response.ContentLength / 1MB, 1) } else { '' }
                        Write-Log ("Downloading ${Label}: ${megabytes}${total} MiB")
                        $lastProgress = $timer.Elapsed.TotalSeconds
                    }
                }
                $downloadFile.Dispose(); $downloadFile = $null
                break
            }
            catch {
                if ($attempt -ge 2) { throw }
                Write-Log ('Download interrupted: ' + (Safe-Text $_.Exception.Message) + '. Retrying once...')
                Start-Sleep -Seconds 2
            }
            finally {
                if ($null -ne $downloadFile) { $downloadFile.Dispose() }
                if ($null -ne $downloadStream) { $downloadStream.Dispose() }
                if ($null -ne $response) { $response.Dispose() }
            }
        }
        if ((Get-FileHash -LiteralPath $partial -Algorithm SHA256).Hash -ne $Sha256) {
            throw "Downloaded $Label did not match its pinned SHA256. It was not installed. Retry installation."
        }
        if ([System.IO.File]::Exists($Destination)) { [System.IO.File]::Replace($partial, $Destination, [NullString]::Value) }
        else { [System.IO.File]::Move($partial, $Destination) }
        Write-Log ("Verified " + $Label + ' download.')
    }
    finally { if ([System.IO.File]::Exists($partial)) { [System.IO.File]::Delete($partial) } }
}

function Install-PortablePython {
    $archive = Join-Path $DownloadDir 'python-3.12.10-embed-amd64.zip'
    Download-Verified $PythonUrl $archive $PythonSha256 'Python 3.12.10 x64 (about 11 MB)'
    $stage = Join-Path $StateDir ('python.install-' + [guid]::NewGuid().ToString('N'))
    try {
        Write-Log 'Preparing the private Python runtime...'
        Add-Type -AssemblyName System.IO.Compression.FileSystem
        [System.IO.Compression.ZipFile]::ExtractToDirectory($archive, $stage)
        [void][System.IO.Directory]::CreateDirectory((Join-Path $stage 'Lib\site-packages'))
        $paths = "python312.zip`r`n.`r`nLib\site-packages`r`n..\app`r`n..\..\app`r`nimport site`r`n"
        [System.IO.File]::WriteAllText((Join-Path $stage 'python312._pth'), $paths, $Utf8)
        if ($null -eq (Get-RuntimeInfo (Join-Path $stage 'python.exe') $stage)) {
            throw 'The downloaded Python runtime could not start. Check Windows updates and the Microsoft Visual C++ runtime, then retry.'
        }
        if ([System.IO.Directory]::Exists($RuntimeDir)) {
            $backup = Join-Path $StateDir ('python.previous-' + (Get-Date -Format 'yyyyMMdd-HHmmss') + '-' + [guid]::NewGuid().ToString('N').Substring(0, 8))
            [System.IO.Directory]::Move($RuntimeDir, $backup)
            Write-Log ('Preserved the previous incomplete runtime at ' + $backup)
        }
        [System.IO.Directory]::Move($stage, $RuntimeDir)
        Write-Log 'Private Python is installed. No system Python settings were changed.'
    }
    finally {
        if ([System.IO.Directory]::Exists($stage)) { Remove-Item -LiteralPath $stage -Recurse -Force }
    }
}

function Install-Pip([string]$PythonExe) {
    $probe = Invoke-Captured $PythonExe @('-I', '-X', 'utf8', '-m', 'pip', '--version') 20
    $common = @('--isolated', '--disable-pip-version-check', '--no-warn-script-location', '--only-binary=:all:', '--timeout', '20', '--retries', '1', '--index-url', 'https://pypi.org/simple', '--no-cache-dir')
    if ($probe.ExitCode -ne 0) {
        $bootstrap = Join-Path $DownloadDir 'get-pip-8c427468.py'
        Download-Verified $GetPipUrl $bootstrap $GetPipSha256 'pip bootstrap'
        Write-Log 'Installing pip in the private runtime...'
        Invoke-PythonStep $PythonExe (@('-I', '-X', 'utf8', '-u', $bootstrap, '--no-setuptools', '--no-wheel', "pip==$PipVersion") + $common)
    }
    else {
        Write-Log 'Checking the private package installer...'
        Invoke-PythonStep $PythonExe (@('-I', '-X', 'utf8', '-u', '-m', 'pip', 'install', '--upgrade', "pip==$PipVersion") + $common)
    }
}

$installLock = $null
$StateDir = $null
try {
    if (-not [System.IO.Path]::IsPathRooted($Root)) { throw '-Root must be an absolute JoyVox folder.' }
    $RootDir = [System.IO.Path]::GetFullPath($Root)
    if ($RootDir.Length -gt [System.IO.Path]::GetPathRoot($RootDir).Length) { $RootDir = $RootDir.TrimEnd('\', '/') }
    if (-not [System.IO.Directory]::Exists($RootDir)) { throw 'The JoyVox folder does not exist.' }
    $StateDir = Join-Path $RootDir '.joyvox'
    $RuntimeDir = Join-Path $StateDir 'python'
    $DownloadDir = Join-Path $StateDir 'downloads'
    [void][System.IO.Directory]::CreateDirectory($StateDir)
    Assert-SupportedPlatform
    if ($Action -eq 'Status') {
        $status = Current-Status
        Write-Status $status
        Show-Status $status
        exit 0
    }
    try {
        $installLock = [System.IO.File]::Open((Join-Path $StateDir 'install.lock'), [System.IO.FileMode]::OpenOrCreate, [System.IO.FileAccess]::ReadWrite, [System.IO.FileShare]::None)
    }
    catch { throw 'Another JoyVox installation is already running. Wait for it to finish.' }
    Write-Log 'Estimated fresh download: CPU packages 150-250 MB; optional NVIDIA libraries add about 1.1-1.5 GB. Python adds about 11 MB only if no compatible installation is found. Models download separately on first Start (small.en ~500 MB; large-v3-turbo ~1.6 GB). Existing packages/caches can reduce downloads.'
    $beforeInstall = Current-Status
    if ($beforeInstall.python -eq 'ready' -and $beforeInstall.core -eq 'ready' -and -not ($Action -eq 'InstallGpu' -or $WithGpu)) {
        Write-Log 'Compatible speech-recognition dependencies are already ready. No repair is needed; speech models download separately on first Start.'
        Write-Status $beforeInstall
        Show-Status $beforeInstall
        exit 0
    }
    $runtime = Find-LocalPython
    if ($null -eq $runtime) {
        $installed = Find-InstalledPython
        if ($null -ne $installed) { $runtime = Install-LocalVenv $installed }
        else { Install-PortablePython; $runtime = Find-LocalPython }
        if ($null -eq $runtime) { throw 'The private Python runtime could not be verified after installation.' }
    }
    Write-Log ('Using Python ' + $runtime.version + ': ' + $runtime.executable)
    Install-Pip $runtime.executable
    $requirements = Support-File 'requirements.txt' 'requirements.txt'
    $pipOptions = @('--isolated', '--disable-pip-version-check', '--no-warn-script-location', '--only-binary=:all:', '--timeout', '20', '--retries', '1', '--index-url', 'https://pypi.org/simple', '--no-cache-dir')
    $repairOptions = @()
    if ($beforeInstall.core -ne 'ready') {
        # An interrupted install can retain dist-info while losing package files.
        # pip's usual "already satisfied" check cannot repair that situation.
        $repairOptions = @('--force-reinstall')
    }
    Write-Log 'Installing or repairing speech-recognition dependencies...'
    Invoke-PythonStep $runtime.executable (@('-I', '-X', 'utf8', '-u', '-m', 'pip', 'install', '--upgrade', '-r', $requirements) + $repairOptions + $pipOptions)
    if ($Action -eq 'InstallGpu' -or $WithGpu) {
        Write-Log 'Installing optional NVIDIA CUDA 12 / cuDNN 9 libraries locally. These downloads can be large.'
        Invoke-PythonStep $runtime.executable (@('-I', '-X', 'utf8', '-u', '-m', 'pip', 'install', '--upgrade', 'nvidia-cublas-cu12>=12.4,<13', 'nvidia-cudnn-cu12>=9.1,<10') + $pipOptions)
    }
    Invoke-PythonStep $runtime.executable @('-I', '-X', 'utf8', '-u', '-m', 'pip', '--isolated', '--disable-pip-version-check', 'check')
    Write-Log 'Checking actual imports and optional GPU library loading...'
    $status = Current-Status
    Write-Status $status
    Show-Status $status
    if ($status.python -ne 'ready' -or $status.core -ne 'ready') {
        throw 'Installation finished, but the dependency check still reports a problem. See the details above.'
    }
    Write-Log 'Dependency installation complete. Click Start to load speech recognition.'
    exit 0
}
catch {
    $message = Safe-Text $_.Exception.Message
    Write-Log ('ERROR: ' + $message)
    if ($null -ne $StateDir) {
        try {
            $status = Current-Status
            $status.summary = 'Installation/check did not complete: ' + $message
            Write-Status $status
        }
        catch {
            try { Write-Status (Missing-Status ('Installation/check did not complete: ' + $message)) } catch { }
        }
    }
    exit 1
}
finally { if ($null -ne $installLock) { $installLock.Dispose() } }
