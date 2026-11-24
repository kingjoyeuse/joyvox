[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Executable,
    [Parameter(Mandatory = $true)][string]$OutputDirectory
)
$ErrorActionPreference = 'Stop'
$ExePath = (Resolve-Path -LiteralPath $Executable).Path
New-Item -ItemType Directory -Force -Path $OutputDirectory | Out-Null
$OutputDirectory = (Resolve-Path -LiteralPath $OutputDirectory).Path

# Capture the unmodified release's actual native windows. Do not install speech
# packages or models, provide simulated device/status data, or start recognition.
Add-Type -AssemblyName System.Drawing
Add-Type -TypeDefinition @'
using System;
using System.Collections.Generic;
using System.Drawing;
using System.Drawing.Imaging;
using System.Runtime.InteropServices;
using System.Text;

public static class JoyVoxCapture {
    public delegate bool EnumProc(IntPtr window, IntPtr param);
    [StructLayout(LayoutKind.Sequential)] public struct RECT { public int Left, Top, Right, Bottom; }
    [StructLayout(LayoutKind.Sequential, CharSet=CharSet.Unicode)] public struct DEVMODE {
        [MarshalAs(UnmanagedType.ByValTStr, SizeConst=32)] public string dmDeviceName;
        public ushort dmSpecVersion, dmDriverVersion, dmSize, dmDriverExtra;
        public uint dmFields;
        public int dmPositionX, dmPositionY;
        public uint dmDisplayOrientation, dmDisplayFixedOutput;
        public short dmColor, dmDuplex, dmYResolution, dmTTOption, dmCollate;
        [MarshalAs(UnmanagedType.ByValTStr, SizeConst=32)] public string dmFormName;
        public ushort dmLogPixels;
        public uint dmBitsPerPel, dmPelsWidth, dmPelsHeight, dmDisplayFlags, dmDisplayFrequency;
        public uint dmICMMethod, dmICMIntent, dmMediaType, dmDitherType, dmReserved1, dmReserved2;
        public uint dmPanningWidth, dmPanningHeight;
    }
    [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern bool EnumDisplaySettings(string device, int mode, ref DEVMODE data);
    [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern int ChangeDisplaySettings(ref DEVMODE data, uint flags);
    [DllImport("user32.dll")] public static extern int GetSystemMetrics(int index);
    public static string DesktopSize() { return GetSystemMetrics(0) + "x" + GetSystemMetrics(1); }
    static DEVMODE NewDisplayMode() { var mode = new DEVMODE(); mode.dmSize = (ushort)Marshal.SizeOf(typeof(DEVMODE)); return mode; }
    public static string PrepareDesktop() {
        string before = DesktopSize();
        if (GetSystemMetrics(0) >= 1280 && GetSystemMetrics(1) >= 1000) return "Desktop already sufficient: " + before;
        var target = NewDisplayMode();
        if (!EnumDisplaySettings(null, -1, ref target)) return "Cannot read display mode; retaining actual desktop " + before;
        target.dmPelsWidth = 1920; target.dmPelsHeight = 1080;
        target.dmFields = 0x00080000 | 0x00100000; // DM_PELSWIDTH | DM_PELSHEIGHT
        int test = ChangeDisplaySettings(ref target, 2); // CDS_TEST, no persistent change
        int change = -99;
        if (test == 0) change = ChangeDisplaySettings(ref target, 0); // session only; no CDS_UPDATEREGISTRY
        if (test != 0 || change != 0) {
            // If Full HD is unsupported, choose a real enumerated mode tall enough
            // to show the complete launcher. Do not synthesize a larger screenshot.
            var candidate = NewDisplayMode(); bool found = false; ulong smallest = UInt64.MaxValue;
            for (int index = 0; index < 2048; index++) {
                var mode = NewDisplayMode();
                if (!EnumDisplaySettings(null, index, ref mode)) break;
                ulong area = (ulong)mode.dmPelsWidth * mode.dmPelsHeight;
                if (mode.dmPelsWidth >= 1280 && mode.dmPelsHeight >= 1000 && area < smallest) { candidate = mode; smallest = area; found = true; }
            }
            if (found) {
                candidate.dmFields = 0x00080000 | 0x00100000;
                int fallbackTest = ChangeDisplaySettings(ref candidate, 2);
                int fallbackChange = fallbackTest == 0 ? ChangeDisplaySettings(ref candidate, 0) : -99;
                return "Requested 1920x1080: test=" + test + ", change=" + change + "; supported fallback " + candidate.dmPelsWidth + "x" + candidate.dmPelsHeight + ": test=" + fallbackTest + ", change=" + fallbackChange + "; actual=" + DesktopSize();
            }
        }
        return "Desktop before=" + before + "; requested 1920x1080: test=" + test + ", change=" + change + "; actual=" + DesktopSize();
    }
    [DllImport("user32.dll")] public static extern bool SetProcessDPIAware();
    [DllImport("user32.dll")] public static extern bool EnumWindows(EnumProc cb, IntPtr p);
    [DllImport("user32.dll")] public static extern bool EnumChildWindows(IntPtr parent, EnumProc cb, IntPtr p);
    [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern int GetClassName(IntPtr w, StringBuilder s, int n);
    [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern int GetWindowText(IntPtr w, StringBuilder s, int n);
    [DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr w, out uint id);
    [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr w);
    [DllImport("user32.dll")] public static extern bool IsWindowEnabled(IntPtr w);
    [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr w, out RECT r);
    [DllImport("user32.dll")] public static extern bool MoveWindow(IntPtr w, int x, int y, int width, int height, bool repaint);
    [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr w);
    [DllImport("user32.dll")] public static extern bool ShowWindow(IntPtr w, int command);
    [DllImport("user32.dll")] public static extern bool UpdateWindow(IntPtr w);
    [DllImport("user32.dll")] public static extern IntPtr SendMessage(IntPtr w, uint msg, IntPtr wp, IntPtr lp);
    [DllImport("user32.dll", EntryPoint="SendMessageW", CharSet=CharSet.Unicode)] public static extern IntPtr SendMessageText(IntPtr w, uint msg, IntPtr wp, StringBuilder text);
    [DllImport("user32.dll")] public static extern bool PostMessage(IntPtr w, uint msg, IntPtr wp, IntPtr lp);
    [DllImport("user32.dll")] public static extern IntPtr GetDlgItem(IntPtr w, int id);
    [DllImport("user32.dll")] public static extern bool PrintWindow(IntPtr w, IntPtr dc, uint flags);
    public static string Title(IntPtr w) { var s = new StringBuilder(8192); SendMessageText(w, 0x000D, (IntPtr)s.Capacity, s); return s.ToString(); }
    public static string Class(IntPtr w) { var s = new StringBuilder(512); GetClassName(w, s, s.Capacity); return s.ToString(); }
    public static IntPtr FindProcessWindow(int processId, string className) {
        IntPtr found = IntPtr.Zero;
        EnumWindows(delegate(IntPtr w, IntPtr p) { uint pid; GetWindowThreadProcessId(w, out pid); if (pid == processId && Class(w) == className && IsWindowVisible(w)) { found = w; return false; } return true; }, IntPtr.Zero);
        return found;
    }
    public static IntPtr FindButton(IntPtr parent, string caption) {
        IntPtr found = IntPtr.Zero;
        EnumChildWindows(parent, delegate(IntPtr w, IntPtr p) { if (Class(w) == "Button" || Class(w) == "BUTTON") { if (Title(w) == caption) { found = w; return false; } } return true; }, IntPtr.Zero);
        return found;
    }
    public static string SaveWindow(IntPtr window, string file) {
        RECT r; if (!GetWindowRect(window, out r)) throw new Exception("GetWindowRect failed.");
        int width = r.Right-r.Left, height = r.Bottom-r.Top;
        if (width < 100 || height < 100) throw new Exception("Window is too small to capture.");
        using (var bitmap = new Bitmap(width, height, PixelFormat.Format32bppArgb)) {
            using (var graphics = Graphics.FromImage(bitmap)) {
                graphics.Clear(Color.Magenta);
                IntPtr dc = graphics.GetHdc();
                bool ok;
                try { ok = PrintWindow(window, dc, 2); } finally { graphics.ReleaseHdc(dc); }
                if (!ok) throw new Exception("PrintWindow failed. This runner may not have an interactive desktop.");
            }
            var colors = new HashSet<int>();
            for (int y = 0; y < height; y += 4) for (int x = 0; x < width; x += 4) colors.Add(bitmap.GetPixel(x,y).ToArgb());
            if (colors.Count < 20) throw new Exception("Capture appears blank; refusing to save a misleading screenshot.");
            bitmap.Save(file, ImageFormat.Png);
        }
        return width + "x" + height;
    }
}
'@ -ReferencedAssemblies System.Drawing, System, System.Core
[void][JoyVoxCapture]::SetProcessDPIAware()
# A small CI desktop clamps Win32 window sizes. Enlarge only this disposable
# runner's current display mode before starting JoyVox; never alter app layout.
$displaySetup = [JoyVoxCapture]::PrepareDesktop()
Write-Host $displaySetup
Start-Sleep -Milliseconds 750
$desktopSize = [JoyVoxCapture]::DesktopSize()
Write-Host "Actual capture desktop: $desktopSize"

function Wait-JoyVoxWindow([int]$ProcessId, [string]$ClassName, [int]$Seconds = 40) {
    $deadline = [DateTime]::UtcNow.AddSeconds($Seconds)
    while ([DateTime]::UtcNow -lt $deadline) {
        $handle = [JoyVoxCapture]::FindProcessWindow($ProcessId, $ClassName)
        if ($handle -ne [IntPtr]::Zero) { return $handle }
        Start-Sleep -Milliseconds 250
    }
    throw "Timed out waiting for $ClassName for process $ProcessId."
}

$app = $null
$main = [IntPtr]::Zero
$settings = [IntPtr]::Zero
try {
    $app = Start-Process -FilePath $ExePath -WorkingDirectory (Split-Path -Parent $ExePath) -PassThru
    [void]$app.WaitForInputIdle(30000)
    $main = Wait-JoyVoxWindow $app.Id 'JoyVoxMain421'
    [void][JoyVoxCapture]::ShowWindow($main, 9)
    [void][JoyVoxCapture]::MoveWindow($main, 40, 40, 940, 850, $true)
    [void][JoyVoxCapture]::SetForegroundWindow($main)
    # Wait for the real startup status check; no recognition installation is run.
    $deadline = [DateTime]::UtcNow.AddSeconds(60)
    do {
        $status = [JoyVoxCapture]::Title([JoyVoxCapture]::GetDlgItem($main, 116))
        $gear = [JoyVoxCapture]::FindButton($main, 'Advanced settings')
        $finished = ($status -ne '' -and $status -notmatch '^Checking ' -and $gear -ne [IntPtr]::Zero -and [JoyVoxCapture]::IsWindowEnabled($gear))
        if (!$finished) { Start-Sleep -Milliseconds 500 }
    } while (!$finished -and [DateTime]::UtcNow -lt $deadline)
    if (!$finished) { throw "Startup did not complete in time. Actual status: $status" }
    # Use the app's own Clear button so setup diagnostics do not distract from
    # the controls being documented. The real readiness/status labels remain.
    $clear = [JoyVoxCapture]::FindButton($main, 'Clear')
    if ($clear -ne [IntPtr]::Zero) { [void][JoyVoxCapture]::SendMessage($clear, 0x00F5, [IntPtr]::Zero, [IntPtr]::Zero) }
    [void][JoyVoxCapture]::UpdateWindow($main)
    Start-Sleep -Milliseconds 600
    $mainSize = [JoyVoxCapture]::SaveWindow($main, (Join-Path $OutputDirectory 'joyvox-main.png'))
    # BM_CLICK sends the same control notification as a normal gear-button click.
    [void][JoyVoxCapture]::SendMessage($gear, 0x00F5, [IntPtr]::Zero, [IntPtr]::Zero)
    $settings = Wait-JoyVoxWindow $app.Id 'JoyVoxConfig421' 15
    [void][JoyVoxCapture]::MoveWindow($settings, 50, 50, 1060, 760, $true)
    [void][JoyVoxCapture]::SetForegroundWindow($settings)
    # settings.json row 19 is Names and vocabulary (row 18 is Recognition context).
    # Use normal list-view keyboard navigation; do not send cross-process pointers.
    $list = [JoyVoxCapture]::GetDlgItem($settings, 500)
    if ($list -eq [IntPtr]::Zero) { throw 'Settings list was not found.' }
    [void][JoyVoxCapture]::SendMessage($list, 0x0100, [IntPtr]0x24, [IntPtr]::Zero)
    [void][JoyVoxCapture]::SendMessage($list, 0x0101, [IntPtr]0x24, [IntPtr]::Zero)
    for ($row = 0; $row -lt 19; $row++) {
        [void][JoyVoxCapture]::SendMessage($list, 0x0100, [IntPtr]0x28, [IntPtr]::Zero)
        [void][JoyVoxCapture]::SendMessage($list, 0x0101, [IntPtr]0x28, [IntPtr]::Zero)
    }
    $selected = [JoyVoxCapture]::SendMessage($list, 0x100C, [IntPtr](-1), [IntPtr]2).ToInt64()
    if ($selected -ne 19) { throw "Expected vocabulary row 19; actual selection is $selected." }
    [void][JoyVoxCapture]::UpdateWindow($settings)
    Start-Sleep -Milliseconds 600
    $settingsSize = [JoyVoxCapture]::SaveWindow($settings, (Join-Path $OutputDirectory 'joyvox-settings.png'))
    @{
        application = 'JoyVox'
        version = '4.2.5'
        executable_sha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $ExePath).Hash.ToLowerInvariant()
        capture_method = 'Win32 PrintWindow of the unmodified release executable'
        environment = [System.Environment]::OSVersion.VersionString
        desktop_resolution = $desktopSize
        desktop_setup = $displaySetup
        initial_state = 'Fresh launch; recognition dependencies and models were not installed; microphone/game were not configured.'
        activity_panel = 'Cleared using the application Clear button before capture.'
        dependency_status = $status
        main = @{ title = [JoyVoxCapture]::Title($main); size = $mainSize }
        settings = @{ title = [JoyVoxCapture]::Title($settings); size = $settingsSize; selected_setting = 'Names and vocabulary' }
    } | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath (Join-Path $OutputDirectory 'capture-provenance.json') -Encoding utf8
    Write-Host "Captured main ($mainSize) and settings ($settingsSize). Actual dependency status: $status"
}
finally {
    if ($settings -ne [IntPtr]::Zero) { [void][JoyVoxCapture]::SendMessage($settings, 0x0010, [IntPtr]::Zero, [IntPtr]::Zero) }
    if ($main -ne [IntPtr]::Zero) { [void][JoyVoxCapture]::PostMessage($main, 0x0010, [IntPtr]::Zero, [IntPtr]::Zero) }
    if ($null -ne $app) {
        try { if (!$app.WaitForExit(10000)) { Stop-Process -Id $app.Id -Force } } catch { }
    }
}
