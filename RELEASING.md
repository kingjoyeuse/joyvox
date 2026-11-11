# Build and publish JoyVox

## Publish the prepared source kit

Extract the complete source kit to **C:\repos\kingpin-joyvox**, keeping the
`release` folder alongside `app`, `gui`, `tools`, and `defaults`. Double-click
**Publish-to-GitHub.cmd**.

The publisher verifies the portable ZIP's checksum and checks every build-source
hash against `release/source-manifest.json` before making remote changes. It
then finds Git and GitHub CLI, downloading official portable copies into
`.joyvox/publish-tools` if needed and verifying their published SHA-256 hashes.
If GitHub CLI is not signed in, complete its browser sign-in; publication then
continues.

The default destination is **the authenticated account / kingpin-joyvox**, with
**private** visibility for a new repository. The script stages an explicit list
of source files, creates a source commit if needed, pushes `main` and the
version tag, and publishes a release with:

- `joyvox-4.2.4-windows-x64-portable.zip`
- `SHA256SUMS.txt`

The source manifest remains with the local build for verification. Runtime data,
models, microphone samples, local configuration/dictionary files, and release
binaries are ignored by Git. Pristine files under `defaults/` are tracked.

The existing Git identity is reused. If no name or email is configured, the
publisher sets a repository-local identity from the authenticated GitHub account
and its GitHub noreply address. It does not change global Git identity or
credential configuration.

### Choose a different owner or public visibility

From Command Prompt in the source folder:

```bat
Publish-to-GitHub.cmd -Repository YOUR-ACCOUNT/kingpin-joyvox -Visibility public
```

`-Repository` also accepts just a repository name under the authenticated account.
Omit `-Visibility public` to keep the private default. Existing repository
visibility is never changed; pass its existing visibility explicitly when it
differs from the default. `-NoBrowser` skips opening the finished release page.

### Rerun and interruption behavior

Rerunning the same version is safe when source, tag, and assets still match.
Existing release assets are hashed and skipped when identical; missing assets
are uploaded. A different existing asset is never deleted or replaced. A partial
draft release is published after the expected assets have been verified.

The script stops with a concrete error if the origin remote points elsewhere,
the checkout is on another branch, unrelated files are already tracked/staged,
remote `main` is ahead or divergent, or an existing tag identifies another
commit. It never force-pushes, resets remote history, or moves an existing tag.
Reconcile that specific condition before rerunning.

If authentication uses a token with restricted permissions, it needs repository
write access and permission to update workflow files. The browser login requests
the `workflow` scope along with repository access. No token is embedded in the
source or copied into the repository.

## Build a new version

Install Go 1.23 or newer and Python 3.10 or newer. Rebuilding does not require
Visual Studio, a C++ compiler, a Windows SDK, or third-party Go modules. End users
need none of these build tools to run the included EXE.

On Windows, from PowerShell:

```powershell
.\tools\build-release.ps1
```

Or from a Windows, Linux, or macOS checkout:

```text
python tools/build_release.py
```

The Python builder prepares the embedded backend/defaults/setup files and native
Windows manifest/version resources. Go builds the native Win32 GUI for AMD64.
The portable ZIP, checksums and source manifest are placed in `release/`.
The `.syso` resource object and `gui/payload/` directory are generated and are
not tracked. `gui/joyvox.cpp` is only an ignored compatibility placeholder that
replaces the old source when the kit is extracted over a 4.2.0 checkout.

For a new release, update root `VERSION`, the backend VERSION in
`app/joyvox_core.py`, the version constant in `gui/model.go`, and the assembly
version in `gui/joyvox.manifest`. Update release notes/docs and rebuild. The
publisher derives the tag from VERSION, for example `v4.2.4`. Never repurpose an
already-published version for changed source or binaries.

To run the focused checks:

```text
python tools/build_release.py --resources-only
go test ./gui
python -m unittest discover -s tests -v
```

The backend tests need NumPy/SciPy, but do not download models or open real
microphones. A Windows run is still needed to evaluate the actual GUI and game.

## Build with GitHub Actions

After publication, open the repository's **Actions** tab, select **Build Windows
portable**, and choose **Run workflow**. It runs the backend tests, builds with
the Go cross-platform toolchain, and provides the portable files as a downloadable
workflow artifact. The workflow has read-only repository permissions and does
not create releases automatically, so it does not race the one-click publisher.

A rebuilt executable may differ byte-for-byte because compiler versions or build
metadata differ. Publish a new version for a changed binary instead of replacing
the asset of an existing version.
