# JoyVox 4.2.4

**Speech to in-game chat for Quake 2 engine games.**

Hold a push-to-talk button, speak, then release to prepare or send public/team
text chat. Recognition runs on your computer. The native Windows `joyvox.exe`
opens without Python already installed; heavy recognition components load only
when requested.

## UI update in 4.2.4

The dependency/readiness area has a thin grey outline. The autoexec-chain status
sits directly below the dependency line, in the same small font as the download
information. The NVIDIA checkbox text is drawn in the light foreground color.
Inputs and both combo boxes use the same dark cerulean surface, including their
selection fields, list rows, and arrow areas.

The right-aligned model selector displays `auto (large-v3-turbo)` or the resolved
CPU fallback. It refreshes when the dependency check finishes, including after
CUDA setup; the actual running model also updates it if inference falls back.
The saved value remains `auto`. The short grey-italic description describes only
the chosen model, without repeating automatic-selection rules. Custom model names
and directories are still accepted.

Quick binds are five borderless columns with small labels above key-only buttons.
Click a button to capture a key; Esc cancels. Successful changes update the button
without replacing the existing readiness/status message. Advanced config has a
gear icon; Start and Stop have play/stop icons. The author credit is measured and
placed 10 logical pixels after the header, bottom aligned.

Dictionary and voice-model cache paths remain in **Advanced config**, with their
Browse controls. This UI update does not require reinstalling already working
recognition dependencies or downloading cached models again.

## Update an existing installation

Close JoyVox. Replace **joyvox.exe** in its existing folder with the executable
from `joyvox-4.2.4-windows-x64-portable.zip`, then reopen it. Do not delete your
configuration, dictionary, `.joyvox` folder, or voice-model cache.

For the complete development/publishing update, extract the source kit under
`C:\repos`, allowing its `kingpin-joyvox` folder to merge with your existing
`C:\repos\kingpin-joyvox`. It does not contain a `.git` directory or your live
settings, so existing repository history and personal configuration remain.
The `gui/joyvox.cpp` compatibility placeholder replaces the old implementation;
the active native GUI is now Go/Win32, not C++.

## First setup

1. Open **joyvox.exe** in a writable folder, not inside the ZIP. Support files
   are restored under `.joyvox`; existing settings/dictionary are not replaced.
2. Select your **Game executable**. A saved selection is kept. On a fresh setup,
   detection tries `C:\games\kingpin\kingpin.exe`, then
   `C:\Program Files (x86)\Steam\steamapps\common\Kingpin\kingpin.exe`.
   Otherwise the field stays blank with a browse prompt. Any `.exe` is accepted.
3. Check **Game / mod output folder**. Selecting `Kingpin.exe` defaults it to
   the executable's sibling `main` folder. Any other filename defaults to the
   sibling `baseq2` folder. Choose the actual mod folder when it differs.
4. If dependencies are already ready, skip setup. **Install / repair dependencies**
   is grey when unnecessary and blue when required packages (including requested
   NVIDIA support) are missing. It remains available for a deliberate repair.
   Approximate downloads appear before setup. Recognition models download on Start,
   not when this repair button is clicked.
5. Choose your microphone by name. **Refresh** queries Windows inputs directly,
   then uses PyAudio to show verified numbered inputs when available.
6. Click **Start JoyVox**, wait for **READY**, then **Launch {game name}**.
   The selected EXE is launched from its own directory; choosing an output/mod
   folder does not add game-specific command-line arguments.

If the game is already running, enter `exec joyvox.cfg` in its console once,
or use your configured bindings filename. A game launched after repair normally
loads it from `autoexec.cfg`.

## Python detection and downloads

The launcher distinguishes **Python found, recognition packages missing** from
**no compatible Python found**. Detection checks JoyVox's own runtime, paths
reported by `py -0p`, a real `python.exe` on PATH, and registered CPython installs.
It never invokes `python3` or runs the Microsoft Store's Python aliases.

Compatible 64-bit **CPython 3.10-3.12** is reused to create an isolated environment
under `.joyvox/venv`. Its global packages, PATH, and registry settings are not
modified. An existing working private runtime is retained. If no compatible
installation is found, setup downloads a verified private **Python 3.12.10**
runtime instead. A newer Python that lacks the required compatible wheels is
not used merely because it is newer.

| Component | Approximate fresh download |
| --- | ---: |
| Core recognition packages | 150-250 MB |
| Optional NVIDIA CUDA/cuDNN libraries | An additional 1.1-1.5 GB |
| Private Python, only when needed | 11 MB |
| `small.en` voice model | 500 MB |
| `large-v3-turbo` voice model | 1.6 GB |

These are estimates, not measured download totals. Versions, existing packages,
model choices, and caches affect actual totals. Models download separately on
first Start and are reused afterwards. Installed disk use is larger than the
compressed downloads. This is a **small online setup package**, not a complete
offline AI bundle. Using an existing Python via a local venv means moving the
folder to another PC may require Install / repair on that PC.

### Operating-system support

This release supports **Windows 10/11, Intel/AMD x64**. Setup checks Windows build
and architecture before any download. Windows 7/8/8.1, native ARM64, and 32-bit
Windows are not supported by this complete recognition stack. Installing Python
3.8 would not make the rest of the stack compatible; no unsupported legacy
installation is attempted. The native launcher itself also targets Windows 10+.

References: [Python Windows support](https://docs.python.org/3.12/using/windows.html),
[ONNX Runtime target environments](https://onnxruntime.ai/docs/build/inferencing.html).

## Game configuration and non-blocking errors

Install / repair checks `autoexec.cfg` in the selected output folder and adds
one line using **STT_EXEC_FILE**, normally:

```text
exec joyvox.cfg
```

The full `.cfg` filename is intentional: the original Quake II `exec` command
loads the filename supplied rather than adding an extension. Existing contents
and their line endings are preserved. Before modifying an existing file, its
original bytes are saved to **autoexec.cfg.joyvox.bak**; a later repair does not
overwrite that first backup. A matching active command is not duplicated. The check follows active `exec`
commands through other CFG files, with cycle protection.
Commented-out examples or a command embedded inside an alias do not count.

A missing/unreadable autoexec entry appears **red**, but does **not** disable
Start JoyVox or Launch. Other genuine prerequisites, such as missing recognition
packages or a missing output folder, still have to be corrected for recognition.
The game can launch independently. UTF-16 autoexec files are not rewritten;
convert them to the plain-text encoding your game uses first.

## Controls and settings

| Control | Default |
| --- | --- |
| Hold Mouse 4, release | Record and transcribe |
| F5 / F6 | Choose public / team for later recordings |
| J / K | Manually send prepared public / team text |

**Advanced config** (gear icon) opens all variables in a separate resizable window. The
Value column is compact; Description fills the actual remaining client width,
including allowance for the vertical scrollbar, without a horizontal scrollbar
at the initial layout. Select a row to edit its full value below the table.
Boolean settings use Yes/No and path settings offer Browse.

The old generic recognition-context default is replaced with example chat and
vocabulary for Quake II/Kingpin. Existing custom prompts and intentionally blank
prompts are preserved. The original configuration is backed up before migrating
an exact old default. The internal `STT_KINGPIN_EXE` key is retained for config
compatibility, but its label and behavior are now **Game executable**.

Windows microphone names are available before recognition dependencies finish
installing. Named selections prefer an exact WASAPI match. An unavailable or
ambiguous name is reported rather than silently selecting another device;
refresh and select a numbered input to disambiguate. The foreground-game safety
check automatically includes the selected executable filename.

## Recognition and privacy

The existing 4.1 capture/recognition improvements remain: persistent capture,
150 ms pre-roll, 120 ms tail, separate immutable utterances, ordered recognition,
phrase corrections, and cache-first model loading. Auto uses `large-v3-turbo`
on CUDA when usable, or `small.en` on CPU. Keep recognition running to avoid
reloading its model between messages. Recognition quality still depends on the
voice, microphone, noise, and selected model; no measured accuracy gain or
startup-speed percentage is claimed for this update.

| Location | Contents |
| --- | --- |
| `joyvox.exe` | Native GUI and embedded support files |
| `joyvox_config.cfg` / `joyvox_dictionary.cfg` | Your live settings and corrections |
| `.joyvox/app`, `.joyvox/tools` | Automatically refreshed support files |
| `.joyvox/python` or `.joyvox/venv` | Local runtime and packages |
| `.joyvox/downloads` | Verified setup-download cache |
| `.joyvox/dependency-status.ini` | Most recent dependency-check result |

No idle microphone recording is written to disk. The optional
`STT_SAVE_RECORDINGS=yes` troubleshooting mode saves the latest utterance/result
only; it is off by default. Microphone audio is not uploaded to a transcription
service. Downloading setup packages and uncached models needs Internet access.

## Source, build, and publication

Double-click **Publish-to-GitHub.cmd** in the complete source kit to publish
version **v4.2.4** with the prepared release ZIP/checksums. It defaults to a private
`kingpin-joyvox` repository under the signed-in account. Nothing is published by
opening the app. Live settings, dictionaries, runtime files, models and recordings
are excluded. See [RELEASING.md](RELEASING.md) for the existing publication flow.

The source layout is `app/` (Python recognition), `gui/` (native Go/Win32),
`tools/` (setup/build/publication), `defaults/`, and `tests/`. Rebuilding needs
Go 1.23+ and Python 3.10+; using the EXE does not need Go. There are no external
Go module dependencies, GUI frameworks, or system-wide Python changes.

## Validation status

The Windows x64 EXE was cross-compiled and its native resources/package checked.
**81 Python backend/integration tests and 12 Go launcher-logic tests passed.**
Those include named microphone resolution, configuration preservation, output
folder selection, custom exec filenames, idempotent append/backup, and table
column-width calculations, Auto-label transitions, canonical saved model names,
setup-needed button state, and the five-column bind layout. They are not a
Windows GUI or audio-device test.

The build host cannot run Windows: dependency installation, the actual controls,
microphone/CUDA, and in-game delivery still need validation on a Windows PC.
The executable is unsigned. No new open-source license has been assigned to the
project; [NOTICE.md](NOTICE.md) records the licensing position and Go notices.
