# JoyVox development

## Environment and workflow

- Repository: `https://github.com/kingjoyeuse/joyvox`, branch `main`. Local folder stays `C:\Repos\kingpin-joyvox`.
- Native Windows Go GUI lives in `gui/`. Python recognition backend lives in `app/`.
- Run `powershell.exe -NoProfile -ExecutionPolicy Bypass -File tools\build-dev.ps1` to prepare embedded resources, run launcher tests, and build `JoyVox-dev.exe`. The script installs Go locally from a checksum-verified official archive when needed. It creates `JoyVox - Latest Build.lnk` in the project root.
- For direct Go commands, dot-source `tools\setup-go.ps1` first. Go and its build cache live in ignored `.joyvox/toolchains`; no global PATH changes are required.
- Development builds preserve `joyvox.exe` and `release/`. Both launcher executables reside in the project root so they use the existing settings and runtime. Close the existing launcher before opening the development build because there is one instance per folder.
- Do not overwrite or republish the existing v4.2.4 release. A changed release needs a new version and rebuilt package.
- Inspect Git status and diff, stage only task-owned changes, and push verified source changes to the user-owned repository.
- Use the model explicitly selected by the user. Do not restart a model-selection discussion during an implementation request.
- GUI automation still requires the user's explicit consent. Compilation and logic tests do not constitute visual or in-game verification.

## Remaining UI work

Match the user's second reference image. Header controls go in the upper-right in this order: gear, Stop, Start. Runtime status goes below them. Dependencies come next, then the five binding buttons without the Quick binds heading. Below that, put the executable, output folder, and microphone in a left column, and an always-visible voice-model picker with descriptions in the right column. Activity stays at the bottom.

- Use equal-width folder and refresh icon buttons.
- Remove the Launch game button.
- Replace the model dropdown with an always-visible picker, preserving auto resolution, selected model persistence, keyboard navigation, and disabled state while running.
- Append activity at the bottom only if already at the bottom; preserve the user's position while reading older activity.
- Byline up 5 logical pixels, tagline down 3, and activity font from 13 to 11 were already committed in `8faafaf`. Verify these in the final layout rather than applying them twice.
- Build and verify the completed changes, provide the root shortcut, and clearly distinguish compiled/tested behavior from visual verification.

## Recurring tooling issues

- `gh auth status` can fail because an inactive saved account has expired even when `kingjoyeuse` is authenticated. Check `gh api user` instead. Do not delete unrelated credentials or ask for login when the active identity works.
- Sandbox access to GitHub credentials and HTTPS can fail. Use the normal escalation mechanism for authorized operations; do not conclude credentials are invalid from sandbox access failures.
- A separately launched Codex app-server can read saved settings but cannot update this active chat: settings updates report `thread not found`, and resume reports `already has an active writer`. Do not repeatedly retry that route or edit Codex databases/configuration as a workaround.
- A missing `go` command is a setup issue handled by `tools/setup-go.ps1`, not a reason to abandon implementation.
