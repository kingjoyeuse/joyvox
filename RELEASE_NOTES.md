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

Update: close JoyVox and replace joyvox.exe in its existing folder. Keep .joyvox,
joyvox_config.cfg, joyvox_dictionary.cfg and downloaded models. The full source
kit also includes build sources, this executable, prepared release assets and
the existing GitHub publisher. Nothing has been published to GitHub by this build.

Validation: Windows AMD64 cross-build completed. 81 backend tests and 12 launcher
logic tests passed. These do not establish native Windows visual, audio or GPU
validation: the build environment did not run the Windows GUI.
