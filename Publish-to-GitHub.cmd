@echo off
setlocal EnableExtensions DisableDelayedExpansion
cd /d "%~dp0"
if errorlevel 1 (
    echo Could not open the JoyVox source folder. Extract it to C:\repos\kingpin-joyvox.
    pause
    exit /b 1
)
powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%~dp0tools\publish-github.ps1" %*
set "JOYVOX_PUBLISH_EXIT=%ERRORLEVEL%"
echo.
if "%JOYVOX_PUBLISH_EXIT%"=="0" (
    echo JoyVox publication completed.
) else (
    echo Publication stopped. See the specific reason above.
    echo No force-push or existing release-asset replacement was attempted.
)
pause
exit /b %JOYVOX_PUBLISH_EXIT%
