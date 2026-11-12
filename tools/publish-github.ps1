#requires -Version 5.1
[CmdletBinding()]
param(
    [ValidatePattern('^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)?$')]
    [string]$Repository = 'joyvox',
    [ValidateSet('private', 'public')]
    [string]$Visibility = 'private',
    [switch]$NoBrowser
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$Root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$ToolRoot = Join-Path $Root '.joyvox\publish-tools'
$script:Git = $null
$script:Gh = $null
$script:CredentialArgs = @()

function Invoke-JoyCommand {
    param([string]$File, [string[]]$Arguments, [int[]]$Allowed = @(0), [string]$Label = '')
    $saved = $ErrorActionPreference
    try {
        # Native stderr may be informational (git writes progress there).
        $ErrorActionPreference = 'Continue'
        $lines = @(& $File @Arguments 2>&1 | ForEach-Object { $_.ToString() })
        $code = $LASTEXITCODE
    } finally { $ErrorActionPreference = $saved }
    $result = [PSCustomObject]@{ ExitCode = $code; Lines = $lines; Text = ($lines -join "`n") }
    if ($Allowed -notcontains $code) {
        if (-not $Label) { $Label = [IO.Path]::GetFileName($File) }
        throw "$Label failed (exit $code).`n$($result.Text)"
    }
    return $result
}

function Invoke-JoyGit {
    param([string[]]$Arguments, [int[]]$Allowed = @(0))
    return Invoke-JoyCommand -File $script:Git -Arguments $Arguments -Allowed $Allowed -Label 'Git'
}

function Invoke-JoyGitRemote {
    param([string[]]$Arguments, [int[]]$Allowed = @(0))
    return Invoke-JoyGit -Arguments ($script:CredentialArgs + $Arguments) -Allowed $Allowed
}

function Invoke-JoyGh {
    param([string[]]$Arguments, [int[]]$Allowed = @(0))
    return Invoke-JoyCommand -File $script:Gh -Arguments $Arguments -Allowed $Allowed -Label 'GitHub CLI'
}

function Get-RelativePath([string]$Path) {
    $full = [IO.Path]::GetFullPath($Path)
    $prefix = $Root.TrimEnd('\', '/') + [IO.Path]::DirectorySeparatorChar
    if (-not $full.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'A source path is outside the JoyVox folder.'
    }
    return $full.Substring($prefix.Length).Replace('\', '/')
}

function Test-SourcePath([string]$Path) {
    if ($Path -match '(^|/)(\.\.?|\.joyvox|\.venv|__pycache__|recordings|models)(/|$)') { return $false }
    if ($Path -match '^gui/(?:payload/|(?:generated_)?payload\.(?:h|hpp|rc)$)') { return $false }
    return (
        $Path -match '^(?:AGENTS\.md|README\.md|RELEASING\.md|RELEASE_NOTES\.md|NOTICE\.md|CHANGELOG\.md|VALIDATION\.txt|VERSION|go\.mod|go\.sum|requirements\.txt|Publish-to-GitHub\.cmd|\.gitignore|\.gitattributes)$' -or
        $Path -match '^app/joyvox(?:_[a-zA-Z0-9_]+)?\.py$' -or
        $Path -match '^app/(?:mic_on|mic_off|trans_fail|trans_success)\.ogg$' -or
        $Path -match '^gui/[A-Za-z0-9_.-]+\.(?:go|json|cpp|h|hpp|rc|manifest|ico)$' -or
        $Path -match '^tools/[A-Za-z0-9_.-]+\.(?:py|ps1)$' -or
        $Path -match '^defaults/joyvox_(?:config|dictionary)\.cfg$' -or
        $Path -match '^tests/(?:test_[A-Za-z0-9_]+|__init__)\.py$' -or
        $Path -match '^\.github/workflows/[A-Za-z0-9_.-]+\.ya?ml$'
    )
}

function Get-SourcePaths {
    $candidates = @()
    foreach ($name in @('AGENTS.md', 'README.md', 'RELEASING.md', 'RELEASE_NOTES.md', 'NOTICE.md',
        'CHANGELOG.md', 'VALIDATION.txt', 'VERSION', 'go.mod', 'go.sum', 'requirements.txt', 'Publish-to-GitHub.cmd', '.gitignore', '.gitattributes')) {
        if (Test-Path -LiteralPath (Join-Path $Root $name) -PathType Leaf) { $candidates += $name }
    }
    foreach ($folder in @('app', 'gui', 'tools', 'defaults', 'tests', '.github\workflows')) {
        $path = Join-Path $Root $folder
        if (Test-Path -LiteralPath $path -PathType Container) {
            foreach ($item in Get-ChildItem -LiteralPath $path -File -Recurse -Force) {
                $relative = Get-RelativePath $item.FullName
                if (Test-SourcePath $relative) { $candidates += $relative }
            }
        }
    }
    return @($candidates | Sort-Object -Unique)
}

function Test-BuildInput([string]$Path) {
    # build_release.py records the complete source/docs/tests used for a release.
    # A local validation report is optional metadata, not a packaged build input.
    return ((Test-SourcePath $Path) -and $Path -ne 'VALIDATION.txt')
}

function Confirm-ReleaseFiles([string]$Version) {
    $zipName = "joyvox-$Version-windows-x64-portable.zip"
    $zip = Join-Path $Root "release\$zipName"
    $checksums = Join-Path $Root 'release\SHA256SUMS.txt'
    $manifestPath = Join-Path $Root 'release\source-manifest.json'
    foreach ($path in @($zip, $checksums, $manifestPath)) {
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
            throw "Missing release file: $path. Keep the prepared release folder with the source kit, or run tools\build-release.ps1."
        }
    }
    $match = [regex]::Match((Get-Content -LiteralPath $checksums -Raw),
        '(?im)^([0-9a-f]{64})\s+\*?' + [regex]::Escape($zipName) + '\s*$')
    if (-not $match.Success) { throw "SHA256SUMS.txt does not contain $zipName." }
    if ((Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash -ne $match.Groups[1].Value) {
        throw "The portable ZIP does not match SHA256SUMS.txt. Rebuild or restore the complete release folder."
    }
    $manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
    if ($manifest.version -ne $Version -or -not $manifest.files) {
        throw 'The source manifest version or format does not match VERSION. Rebuild the release.'
    }
    $recorded = @{}
    foreach ($property in $manifest.files.PSObject.Properties) {
        $path = [string]$property.Name
        $hash = [string]$property.Value
        if (-not (Test-BuildInput $path) -or $hash -notmatch '^[0-9a-fA-F]{64}$') {
            throw "Invalid source manifest entry: $path. Rebuild the release."
        }
        if ($recorded.ContainsKey($path)) { throw "Duplicate source manifest entry: $path" }
        $recorded[$path] = $hash
        $local = Join-Path $Root $path
        if (-not (Test-Path -LiteralPath $local -PathType Leaf) -or
            (Get-FileHash -LiteralPath $local -Algorithm SHA256).Hash -ne $hash) {
            throw "Source changed since the portable build: $path. Run tools\build-release.ps1 before publishing."
        }
    }
    foreach ($path in Get-SourcePaths) {
        if ((Test-BuildInput $path) -and -not $recorded.ContainsKey($path)) {
            throw "The build manifest does not include $path. Rebuild before publishing."
        }
    }
    return @($zip, $checksums)
}

function Get-OfficialRelease([string]$Slug) {
    return Invoke-RestMethod -Uri "https://api.github.com/repos/$Slug/releases/latest" -Headers @{
        'User-Agent' = 'JoyVox-Publisher'; 'Accept' = 'application/vnd.github+json'
    } -TimeoutSec 45
}

function Install-PortableTool([ValidateSet('git', 'gh')][string]$Name) {
    $slug = if ($Name -eq 'gh') { 'cli/cli' } else { 'git-for-windows/git' }
    $pattern = if ($Name -eq 'gh') { '^gh_[0-9.]+_windows_amd64\.zip$' } else { '^MinGit-[0-9.]+-64-bit\.zip$' }
    Write-Host "Downloading a verified portable copy of $Name from $slug..."
    $release = Get-OfficialRelease $slug
    $assets = @($release.assets | Where-Object { $_.name -match $pattern })
    if ($assets.Count -ne 1) { throw "Could not identify one Windows x64 $Name ZIP in the official release." }
    $asset = $assets[0]
    $hash = ''
    if ($asset.PSObject.Properties.Name -contains 'digest' -and $asset.digest -match '^sha256:([0-9a-f]{64})$') {
        $hash = $Matches[1]
    }
    if (-not $hash -and $Name -eq 'gh') {
        $checksumAssets = @($release.assets | Where-Object { $_.name -match '^gh_[0-9.]+_checksums\.txt$' })
        if ($checksumAssets.Count -ne 1) { throw 'The official GitHub CLI checksum list is missing.' }
        $checks = (Invoke-WebRequest -UseBasicParsing -Uri $checksumAssets[0].browser_download_url -TimeoutSec 45).Content
        $m = [regex]::Match($checks, '(?im)^([0-9a-f]{64})\s+\*?' + [regex]::Escape($asset.name) + '\s*$')
        if ($m.Success) { $hash = $m.Groups[1].Value }
    }
    if (-not $hash -and $Name -eq 'git') {
        $m = [regex]::Match([string]$release.body,
            '(?im)\|?\s*`?' + [regex]::Escape($asset.name) + '`?\s*\|\s*`?([0-9a-f]{64})`?\s*\|?')
        if ($m.Success) { $hash = $m.Groups[1].Value }
    }
    if (-not $hash) { throw "No SHA-256 was supplied by the official $Name release; the download will not be executed." }
    $downloads = Join-Path $ToolRoot 'downloads'
    New-Item -ItemType Directory -Path $downloads -Force | Out-Null
    $archive = Join-Path $downloads $asset.name
    if (-not (Test-Path -LiteralPath $archive) -or (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash -ne $hash) {
        Invoke-WebRequest -UseBasicParsing -Uri $asset.browser_download_url -OutFile "$archive.partial" -TimeoutSec 180
        if ((Get-FileHash -LiteralPath "$archive.partial" -Algorithm SHA256).Hash -ne $hash) {
            throw "Checksum mismatch for the official $Name download. Nothing from it was executed."
        }
        Move-Item -LiteralPath "$archive.partial" -Destination $archive -Force
    }
    $destination = Join-Path $ToolRoot $Name
    if (Test-Path -LiteralPath $destination) {
        Move-Item -LiteralPath $destination -Destination "$destination.previous-$([Guid]::NewGuid().ToString('N'))"
    }
    Expand-Archive -LiteralPath $archive -DestinationPath $destination
    if ($Name -eq 'git') {
        $exe = Join-Path $destination 'cmd\git.exe'
    } else {
        $found = @(Get-ChildItem -LiteralPath $destination -Filter gh.exe -File -Recurse)
        if ($found.Count -ne 1) { throw 'The downloaded GitHub CLI ZIP has an unexpected layout.' }
        $exe = $found[0].FullName
    }
    if (-not (Test-Path -LiteralPath $exe -PathType Leaf)) { throw "The downloaded $Name archive did not contain the expected executable." }
    return $exe
}

function Find-JoyTool([string]$Name) {
    $command = Get-Command "$Name.exe" -ErrorAction SilentlyContinue
    if ($command) { return $command.Source }
    $cachedRoot = Join-Path $ToolRoot $Name
    if (Test-Path -LiteralPath $cachedRoot) {
        $cached = @(Get-ChildItem -LiteralPath $cachedRoot -Filter "$Name.exe" -File -Recurse)
        if ($Name -eq 'git') { $cached = @($cached | Where-Object { $_.FullName -match '[\\/]cmd[\\/]git\.exe$' }) }
        if ($cached.Count -eq 1) { return $cached[0].FullName }
    }
    # Local portable tools need no machine installation, PATH edit or UAC prompt.
    return Install-PortableTool $Name
}

function Get-GitHubJson([string]$Endpoint, [switch]$AllowMissing) {
    $result = Invoke-JoyGh -Arguments @('api', $Endpoint) -Allowed @(0, 1)
    if ($result.ExitCode -eq 0) { return $result.Text | ConvertFrom-Json }
    if ($AllowMissing -and $result.Text -match '(HTTP 404|"status"\s*:\s*"?404)') { return $null }
    throw "GitHub could not read $Endpoint.`n$($result.Text)"
}

function Get-RemoteRepository([string]$Url) {
    if ($Url -match '^(?:https://github\.com/|git@github\.com:|ssh://git@github\.com/)([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(?:\.git)?/?$') {
        return "$($Matches[1])/$($Matches[2])"
    }
    return ''
}

function Confirm-ExistingAssets($Release, [string[]]$Assets, [string]$Target, [string]$Tag) {
    if (-not $Release) { return $Assets }
    $missing = @()
    foreach ($file in $Assets) {
        $name = [IO.Path]::GetFileName($file)
        $remoteAssets = @($Release.assets | Where-Object { $_.name -ceq $name })
        if ($remoteAssets.Count -eq 0) { $missing += $file; continue }
        if ($remoteAssets.Count -ne 1) { throw "Release $Tag contains duplicate asset names: $name" }
        $asset = $remoteAssets[0]
        $expected = (Get-FileHash -LiteralPath $file -Algorithm SHA256).Hash
        $remoteHash = ''
        if ($asset.PSObject.Properties.Name -contains 'digest' -and $asset.digest -match '^sha256:([0-9a-f]{64})$') {
            $remoteHash = $Matches[1]
        } else {
            $checkDir = Join-Path $ToolRoot ('release-check-' + [Guid]::NewGuid().ToString('N'))
            New-Item -ItemType Directory -Path $checkDir -Force | Out-Null
            try {
                Invoke-JoyGh -Arguments @('release', 'download', $Tag, '--repo', $Target,
                    '--pattern', $name, '--dir', $checkDir) | Out-Null
                $remoteHash = (Get-FileHash -LiteralPath (Join-Path $checkDir $name) -Algorithm SHA256).Hash
            } finally { Remove-Item -LiteralPath $checkDir -Recurse -Force -ErrorAction SilentlyContinue }
        }
        if ($remoteHash -ne $expected) {
            throw "Release asset $name already exists with different bytes. It was not replaced. Use a new version for a changed build."
        }
        Write-Host "Already uploaded and verified: $name"
    }
    return $missing
}

Push-Location $Root
try {
    if ($env:OS -ne 'Windows_NT' -or -not [Environment]::Is64BitOperatingSystem) {
        throw 'Run Publish-to-GitHub.cmd on 64-bit Windows from the extracted source kit.'
    }
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
    $version = (Get-Content -LiteralPath (Join-Path $Root 'VERSION') -Raw).Trim()
    if ($version -notmatch '^\d+\.\d+\.\d+(?:-[A-Za-z0-9.-]+)?$') { throw 'VERSION must contain one semantic version, such as 4.2.0.' }
    $tag = "v$version"
    $assets = @(Confirm-ReleaseFiles $version)
    if (-not (Test-Path -LiteralPath 'RELEASE_NOTES.md' -PathType Leaf)) { throw 'RELEASE_NOTES.md is missing.' }
    Write-Host "JoyVox $version - source and portable release verified."

    $script:Git = Find-JoyTool 'git'
    $script:Gh = Find-JoyTool 'gh'
    $env:PATH = ([IO.Path]::GetDirectoryName($script:Git)) + ';' + ([IO.Path]::GetDirectoryName($script:Gh)) + ';' + $env:PATH
    # auth status fails if any saved account has expired, even when the active
    # account works. Check the identity that will actually publish instead.
    $auth = Invoke-JoyGh -Arguments @('api', 'user') -Allowed @(0, 1)
    if ($auth.ExitCode -ne 0) {
        Write-Host 'Complete the GitHub browser sign-in, then this publisher will continue.'
        & $script:Gh auth login --hostname github.com --git-protocol https --web --scopes repo,read:org,workflow
        if ($LASTEXITCODE -ne 0) { throw 'GitHub sign-in did not complete. Run the publisher again after signing in.' }
    }
    $user = Get-GitHubJson 'user'
    $target = if ($Repository.Contains('/')) { $Repository } else { "$($user.login)/$Repository" }
    if (@($target.Split('/') | Where-Object { $_ -in @('.', '..') }).Count -gt 0) { throw 'Invalid owner/repository name.' }
    Write-Host "Target: https://github.com/$target ($Visibility for a new repository)"
    # Use the selected gh account for this process only; do not change global Git credentials.
    $ghForShell = $script:Gh.Replace('\', '/')
    # Git executes a ! helper through its POSIX shell. Single quotes preserve
    # spaces, dollar signs and backticks; an embedded apostrophe closes the
    # quote, emits one escaped apostrophe, then opens the quote again.
    $quotedGh = "'" + $ghForShell.Replace("'", "'\''") + "'"
    $script:CredentialArgs = @('-c', 'credential.helper=', '-c', ('credential.helper=!' + $quotedGh + ' auth git-credential'))

    $repo = Get-GitHubJson "repos/$target" -AllowMissing
    if ($repo) {
        $existingVisibility = if ($repo.private) { 'private' } else { 'public' }
        if ($existingVisibility -ne $Visibility) {
            throw "The existing repository is $existingVisibility. This run requested $Visibility. Pass -Visibility $existingVisibility to publish there intentionally; visibility was not changed."
        }
        if (-not $repo.permissions.push) { throw "The signed-in account cannot push to $target." }
    }

    $top = Invoke-JoyGit -Arguments @('rev-parse', '--show-toplevel') -Allowed @(0, 128)
    if ($top.ExitCode -eq 0) {
        if ([IO.Path]::GetFullPath($top.Text.Trim()) -ne $Root) {
            throw 'This folder is inside another Git repository. Move the source kit into its own C:\repos\kingpin-joyvox folder.'
        }
    } else {
        Invoke-JoyGit -Arguments @('init', '--initial-branch=main') | Out-Null
    }
    $branch = (Invoke-JoyGit -Arguments @('symbolic-ref', '--quiet', '--short', 'HEAD') -Allowed @(0, 1)).Text.Trim()
    if ($branch -ne 'main') { throw "The current Git branch is '$branch'. Switch to main before publishing; no branch was changed." }
    $staged = (Invoke-JoyGit -Arguments @('diff', '--cached', '--name-only')).Lines
    if ($staged.Count -gt 0) { throw 'There are already staged changes. Commit or unstage them before publishing so this script does not combine unrelated work.' }
    $tracked = (Invoke-JoyGit -Arguments @('ls-files')).Lines
    $outside = @($tracked | Where-Object { -not (Test-SourcePath $_) })
    if ($outside.Count -gt 0) {
        throw "The repository already tracks files outside the JoyVox source list: $($outside -join ', '). Review them before publishing; they were not removed."
    }
    $origin = Invoke-JoyGit -Arguments @('remote', 'get-url', 'origin') -Allowed @(0, 2, 128)
    if ($origin.ExitCode -eq 0 -and (Get-RemoteRepository $origin.Text.Trim()) -ne $target) {
        throw 'The origin remote points somewhere else. It was not changed. Use the correct source checkout or the matching -Repository option.'
    }
    $headResult = Invoke-JoyGit -Arguments @('rev-parse', '--verify', 'HEAD') -Allowed @(0, 128)
    $head = if ($headResult.ExitCode -eq 0) { $headResult.Text.Trim() } else { '' }
    $remoteTagObject = ''
    $remoteTagCommit = ''
    if ($repo) {
        if ($origin.ExitCode -ne 0) {
            Invoke-JoyGit -Arguments @('remote', 'add', 'origin', "https://github.com/$target.git") | Out-Null
        }
        $refs = (Invoke-JoyGitRemote -Arguments @('ls-remote', '--heads', 'origin')).Lines
        if ($refs.Count -gt 0) {
            $main = @($refs | Where-Object { $_ -match '\srefs/heads/main$' })
            if ($main.Count -ne 1 -or -not $head) {
                throw 'The existing remote has history that this folder does not share. Clone that repository and apply the source kit there; no remote history was overwritten.'
            }
            Invoke-JoyGitRemote -Arguments @('fetch', '--no-tags', 'origin', 'main') | Out-Null
            $ancestor = Invoke-JoyGit -Arguments @('merge-base', '--is-ancestor', 'FETCH_HEAD', 'HEAD') -Allowed @(0, 1)
            if ($ancestor.ExitCode -ne 0) { throw 'Remote main is ahead of or diverges from local main. Reconcile it before publishing; no force-push was attempted.' }
        }
        foreach ($line in (Invoke-JoyGitRemote -Arguments @('ls-remote', 'origin', "refs/tags/$tag", "refs/tags/$tag^{}")).Lines) {
            $parts = $line -split '\s+', 2
            if ($parts[1] -eq "refs/tags/$tag") { $remoteTagObject = $parts[0] }
            if ($parts[1] -eq "refs/tags/$tag^{}") { $remoteTagCommit = $parts[0] }
        }
        if (-not $remoteTagCommit) { $remoteTagCommit = $remoteTagObject }
    }
    $release = if ($repo) { Get-GitHubJson "repos/$target/releases/tags/$tag" -AllowMissing } else { $null }
    $missingAssets = @(Confirm-ExistingAssets -Release $release -Assets $assets -Target $target -Tag $tag)

    $source = @(Get-SourcePaths)
    $paths = @($source + $tracked | Sort-Object -Unique)
    Invoke-JoyGit -Arguments (@('add', '--all', '--') + $paths) | Out-Null
    $changed = (Invoke-JoyGit -Arguments @('diff', '--cached', '--name-only')).Lines
    $localTag = Invoke-JoyGit -Arguments @('rev-parse', '--verify', "refs/tags/$tag") -Allowed @(0, 128)
    if (($localTag.ExitCode -eq 0 -or $remoteTagCommit) -and $changed.Count -gt 0) {
        # Only this publisher's allowlisted changes were staged, so reset just those paths.
        if ($head) { Invoke-JoyGit -Arguments (@('reset', '--quiet', '--') + $paths) | Out-Null }
        throw "Version $version is already tagged, but the source has changes. Choose a new VERSION, update RELEASE_NOTES.md and rebuild. No tag was moved."
    }
    if ($changed.Count -gt 0) {
        $name = Invoke-JoyGit -Arguments @('config', '--get', 'user.name') -Allowed @(0, 1)
        if (-not $name.Text.Trim()) {
            $displayName = if ($user.name) { $user.name } else { $user.login }
            Invoke-JoyGit -Arguments @('config', '--local', 'user.name', $displayName) | Out-Null
        }
        $email = Invoke-JoyGit -Arguments @('config', '--get', 'user.email') -Allowed @(0, 1)
        if (-not $email.Text.Trim()) {
            Invoke-JoyGit -Arguments @('config', '--local', 'user.email', "$($user.id)+$($user.login)@users.noreply.github.com") | Out-Null
        }
        Invoke-JoyGit -Arguments @('commit', '-m', "Release JoyVox $version") | Out-Null
        $head = (Invoke-JoyGit -Arguments @('rev-parse', 'HEAD')).Text.Trim()
    }
    if (-not $head) { throw 'There is no source commit to publish.' }
    if ($remoteTagCommit -and $remoteTagCommit -ne $head) { throw "Remote tag $tag points to another commit. It was not changed." }
    if ($localTag.ExitCode -eq 0) {
        $localCommit = (Invoke-JoyGit -Arguments @('rev-parse', "refs/tags/$tag^{}")).Text.Trim()
        if ($localCommit -ne $head) { throw "Local tag $tag points to another commit. It was not moved." }
        if ($remoteTagObject -and $localTag.Text.Trim() -ne $remoteTagObject) {
            throw "Local and remote $tag are different tag objects. Review them before publishing; neither was overwritten."
        }
    } elseif ($remoteTagObject) {
        Invoke-JoyGitRemote -Arguments @('fetch', '--no-tags', 'origin', "refs/tags/${tag}:refs/tags/$tag") | Out-Null
    } else {
        # Git also needs an identity to create an annotated tag after a pre-existing commit.
        $name = Invoke-JoyGit -Arguments @('config', '--get', 'user.name') -Allowed @(0, 1)
        if (-not $name.Text.Trim()) { Invoke-JoyGit -Arguments @('config', '--local', 'user.name', $user.login) | Out-Null }
        $email = Invoke-JoyGit -Arguments @('config', '--get', 'user.email') -Allowed @(0, 1)
        if (-not $email.Text.Trim()) { Invoke-JoyGit -Arguments @('config', '--local', 'user.email', "$($user.id)+$($user.login)@users.noreply.github.com") | Out-Null }
        Invoke-JoyGit -Arguments @('tag', '-a', $tag, '-m', "JoyVox $version", $head) | Out-Null
    }
    # No repository or release is created until the local release and source are reviewable.
    if (-not $repo) {
        Invoke-JoyGh -Arguments @('repo', 'create', $target, "--$Visibility", '--description', 'JoyVox speech to in-game chat for Quake 2 engine games') | Out-Null
        if ($origin.ExitCode -ne 0) {
            Invoke-JoyGit -Arguments @('remote', 'add', 'origin', "https://github.com/$target.git") | Out-Null
        }
    }
    Invoke-JoyGitRemote -Arguments @('push', '--atomic', '--set-upstream', 'origin',
        'HEAD:refs/heads/main', "refs/tags/${tag}:refs/tags/$tag") | Out-Null
    if (-not $release) {
        Invoke-JoyGh -Arguments (@('release', 'create', $tag, '--repo', $target, '--verify-tag',
            '--title', "JoyVox $version", '--notes-file', (Join-Path $Root 'RELEASE_NOTES.md')) + $assets) | Out-Null
    } else {
        if ($missingAssets.Count -gt 0) {
            Invoke-JoyGh -Arguments (@('release', 'upload', $tag, '--repo', $target) + $missingAssets) | Out-Null
        }
        if ($release.draft) {
            Invoke-JoyGh -Arguments @('release', 'edit', $tag, '--repo', $target, '--draft=false') | Out-Null
        }
    }
    $verified = Get-GitHubJson "repos/$target/releases/tags/$tag"
    $stillMissing = @(Confirm-ExistingAssets -Release $verified -Assets $assets -Target $target -Tag $tag)
    if ($stillMissing.Count -gt 0) { throw 'GitHub is missing one or more expected assets. Run the publisher again to resume.' }
    Write-Host ''
    Write-Host "Source:  https://github.com/$target" -ForegroundColor Green
    Write-Host "Release: $($verified.html_url)" -ForegroundColor Green
    Write-Host "Commit:  $head"
    if (-not $NoBrowser) { Start-Process $verified.html_url }
    exit 0
} catch {
    Write-Host ''
    Write-Host $_.Exception.Message -ForegroundColor Red
    Write-Host 'The publisher never force-pushes, moves an existing tag, or replaces an existing release asset.'
    exit 1
} finally {
    Pop-Location
}
