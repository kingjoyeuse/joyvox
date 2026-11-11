# JoyVox 4.2.4

UI update from 4.2.3; no recognition-dependency changes.

- Light, readable NVIDIA checkbox text using native behavior with explicit drawing.
- Consistent dark-cerulean microphone/model dropdowns, including their arrows and lists.
- Grey Install / repair button when setup is ready; brighter blue when required.
- Closer, bottom-aligned “by Joyeuse” credit, placed using the rendered header width.
- Narrower, right-aligned model selector with `auto (resolved-model)` display.
- Auto display refreshes after dependency checks/installation and when the actual
  running model is reported. Configuration still saves the plain `auto` value.
- One brief description of the selected model; no duplicated Auto-selection explanation.
- Five-column, two-row quick-bind grid: small labels above key-only buttons.
- Labels: Hold to talk; Autochat target: public; Autochat target: team;
  Send message: public; Send message: team.
- No successful-bind-update status messages; capture prompts and errors remain.
- Gear icon + Advanced config; play triangle + Start JoyVox; stop square + Stop.
- Small autoexec status moved below dependencies, inside a thin grey outlined box
  containing the dependency/download information.
- Existing icon, exec-chain detection, dictionary, voice-model cache and settings retained.


---

# JoyVox 4.2.1 — 2026-11-10

Fix installed-Python detection, dependency estimates, microphone discovery,
generic game paths/launch captions, configuration labels/layout, recognition
context defaults, and non-blocking autoexec.cfg integration. The native GUI is
now a standard-library-only Go/Win32 build; existing Python recognition code and
user files are retained. See RELEASE_NOTES.md for the detailed changes.

# JoyVox 4.2.0 — 2026-11-10

## Native launcher and configuration

- Add a small Windows x64 `joyvox.exe` that opens before Python or the speech
  model loads, with dependency status, install/repair, start/stop, and live logs.
- Detect Kingpin first in `C:\games\kingpin`, then in Steam's default Kingpin
  folder; provide executable and game/mod folder pickers for other installs.
- Add microphone and model selectors, dictionary/cache pickers, and a separate
  editor for all configuration variables. Preserve existing user settings.
- Embed the application support files in the EXE and extract them under
  `.joyvox`, separate from the repository's editable source files.

## Portable setup and release tooling

- Install private Python and pinned speech packages from the GUI; make NVIDIA
  acceleration optional, and verify actual imports and audio decoding.
- Keep the default model cache beside the app and avoid retaining pip's wheel
  cache. The small initial package downloads AI dependencies and models as needed.
- Add a one-click GitHub publisher for the source, version tag, and portable
  release assets, with local-file exclusions and build/checksum verification.
- Add a native Windows build script and a manual GitHub Actions build workflow.
- Retain 4.1's audio and recognition improvements. The backend regression suite
  passes; Windows GUI, microphone, CUDA, and game operation still require a run
  on the target Windows computer. See the current release notes for limits.

# JoyVox 4.1.0 — 2026-11-08

## Recognition and audio

- The automatic model choice is Whisper large-v3-turbo on a working NVIDIA CUDA
  setup, or small.en on CPU. Explicit model choices remain supported.
- Prefer FP16 GPU inference for the supplied 16 GB RTX 3080 setup; CPU prefers
  INT8. GPU INT8/FP16 remains an explicit memory-saving option.
- Keep the microphone stream open and retain a bounded 150 ms idle pre-roll.
  Complete a 120 ms release tail before handing off an immutable recording.
- Prevent rapid presses from resetting, extending or merging a previous
  utterance; limit recording duration and the waiting queue.
- Use one inference worker. Retain the public/team choice with each recording.
- Resample native-rate microphones correctly when 16 kHz input is unavailable.
- Feed PCM directly to the model, avoiding temporary WAV writes in normal use.
- Use compact, stable vocabulary hints and conservative speech detection.
- Make dictionary rules match full phrases, respect word boundaries, prefer
  longer rules and apply substitutions once without cascading them.

## Startup and installation

- Show the banner and progress before loading audio or recognition libraries.
- Overlap model initialization with microphone, sound and input setup.
- Check local model files before contacting the download service; reuse the
  existing Hugging Face cache. Download only missing/incomplete model files.
- Probe CUDA without PyTorch, register installed NVIDIA runtime DLL folders,
  and check a real inference before reporting GPU readiness.
- Explain CUDA fallback; an explicitly requested CUDA device fails visibly.
- Replace the incomplete installer with an isolated environment and an optional
  NVIDIA runtime installer. Both the configured model and selected device are
  shown, with elapsed startup and transcription times.

## Game integration

- Preserve the supplied game path, Mouse 4, F5/F6, J/K, sounds and formatting.
- Atomically replace chat files. Do not simulate a transmit key after a failed
  output write. Prevent an old cleanup event from erasing a newer result.
- Clear engine aliases explicitly, rather than just writing an empty file.
- Normalize console separators in recognized text so they remain message text.
- Handle named/function keys correctly and preserve text when Kingpin is not
  focused. Add a custom engine's executable name in STT_GAME_EXES as needed.
- Keep late inference results from being transmitted during shutdown.

## Verification and limits

The included tests exercise audio capture with generated PCM and fake devices,
model/cache/fallback behavior with test doubles, text/configuration handling,
and actual worker-thread ordering. They do not measure the user's word-error
rate or establish Windows/CUDA/game performance. The original 4.1 release
included a separate validation report.

The larger default model has a larger first download and may have a longer
cold load than base.en. Cached startup and per-utterance overhead have been
improved; measured end-to-end timing on the user's PC remains to be collected.

JoyVox by joyeuse; this release updates the supplied 4.0.1 program.
