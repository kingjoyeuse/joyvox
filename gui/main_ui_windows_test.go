package main

import (
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// The controls are children of a hidden, test-owned window. These checks do
// not operate the user's running launcher or change its settings.
func testParent(t *testing.T, width, height int) (uintptr, func()) {
	t.Helper()
	parentClass, blank := up("STATIC"), up("")
	parent, _, err := user32.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(parentClass)), uintptr(unsafe.Pointer(blank)), 0, 0, 0, uintptr(width), uintptr(height), 0, 0, 0, 0)
	if parent == 0 {
		t.Fatalf("create hidden test parent: %v", err)
	}
	return parent, func() { user32.NewProc("DestroyWindow").Call(parent) }
}

func testControl(t *testing.T, class string, style uint32, width, height int, id int) (uintptr, func()) {
	t.Helper()
	parent, destroy := testParent(t, 500, 500)
	blank := up("")
	p := up(class)
	h, _, err := user32.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(blank)), uintptr(child|visible|style), 0, 0, uintptr(width), uintptr(height), parent, uintptr(id), 0, 0)
	runtime.KeepAlive(p)
	if h == 0 {
		destroy()
		t.Fatalf("create %s test control: %v", class, err)
	}
	return h, destroy
}

func TestLauncherLayoutOrder(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := ole32.NewProc("CoInitializeEx").Call(0, 2)
	if hrOK(hr) {
		defer ole32.NewProc("CoUninitialize").Call()
	}
	if _, err := syscall.LoadDLL("Msftedit.dll"); err != nil {
		t.Fatal(err)
	}
	previous := app
	defer func() { app = previous }()
	app = AppState{dpi: 96, controls: map[int]uintptr{}, labels: map[string]uintptr{}, deps: map[string]string{}, jobs: map[string]*Job{}}
	app.instance, _, _ = kernel32.NewProc("GetModuleHandleW").Call(0)
	setFonts()
	defer func() {
		for _, f := range []uintptr{app.font, app.titleFont, app.logFont, app.smallFont, app.smallItalicFont} {
			gdi32.NewProc("DeleteObject").Call(f)
		}
	}()
	parent, destroy := testParent(t, 920, 870)
	defer destroy()
	app.window = parent
	createMainControls()
	rect := func(h uintptr) Rect {
		t.Helper()
		var r Rect
		if ok, _, _ := user32.NewProc("GetWindowRect").Call(h, uintptr(unsafe.Pointer(&r))); ok == 0 {
			t.Fatal("could not read control geometry")
		}
		return r
	}
	gear, stop, start := rect(app.controls[idConfig]), rect(app.controls[idStop]), rect(app.controls[idStart])
	if !(gear.Right < stop.Left && stop.Right < start.Left && rect(app.controls[idRun]).Top > start.Bottom) {
		t.Fatal("header controls are not ordered gear, stop, start with status below")
	}
	bind, exe, model, activity := rect(app.controls[idBindTalk]), rect(app.controls[idExe]), rect(app.controls[idModel]), rect(app.controls[idLog])
	if !(bind.Bottom < exe.Top && exe.Right < model.Left && model.Bottom < activity.Top) {
		t.Fatal("bindings, left fields, model picker, and activity are not in the requested order")
	}
	if _, exists := app.labels["binds"]; exists {
		t.Fatal("Quick binds heading still exists")
	}
	var iconWidth int32
	for _, id := range []int{idExeBrowse, idOutputBrowse, idMicRefresh} {
		r := rect(app.controls[id])
		if iconWidth == 0 {
			iconWidth = r.Right - r.Left
		} else if r.Right-r.Left != iconWidth {
			t.Fatal("folder and refresh buttons have different widths")
		}
	}
	for _, h := range app.controls {
		if strings.HasPrefix(getText(h), "Launch ") {
			t.Fatal("game launch button is still present")
		}
	}
	app.jobs["backend"] = &Job{}
	refreshButtons()
	if isEnabled(app.controls[idModel]) {
		t.Fatal("model picker remained enabled while recognition was running")
	}
	delete(app.jobs, "backend")
	refreshButtons()
	if !isEnabled(app.controls[idModel]) {
		t.Fatal("model picker did not re-enable after recognition stopped")
	}
}

func TestActivityKeepsOlderTextUntilReaderReturnsToBottom(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := ole32.NewProc("CoInitializeEx").Call(0, 2)
	if hrOK(hr) {
		defer ole32.NewProc("CoUninitialize").Call()
	}
	previous := app
	defer func() { app = previous }()
	if _, err := syscall.LoadDLL("Msftedit.dll"); err != nil {
		t.Fatal(err)
	}
	app = AppState{dpi: 96, controls: map[int]uintptr{}}
	h, destroy := testControl(t, "RICHEDIT50W", 0x200000|4|0x40|0x800, 300, 100, idLog)
	defer destroy()
	app.controls[idLog] = h
	themeActivity(h)
	send(h, 0x435, 0, 16*1024*1024)
	setText(h, "BEGIN "+strings.Repeat("é", 170000))
	send(h, 0x115, 6, 0) // SB_TOP
	appendLog("new activity")
	if got := getText(h); !strings.HasPrefix(got, "BEGIN ") || !strings.Contains(got, "new activity") {
		t.Fatal("older text was removed while reading it")
	}
	send(h, 0x115, 7, 0) // SB_BOTTOM
	appendLog("after returning to the bottom")
	got := getText(h)
	if strings.HasPrefix(got, "BEGIN ") || !strings.Contains(got, "after returning to the bottom") {
		t.Fatal("old activity was not pruned after returning to the bottom")
	}
}

func TestModelPickerKeepsSelectionAndCustomValues(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	previous := app
	defer func() { app = previous }()
	app = AppState{dpi: 96, controls: map[int]uintptr{}, deps: map[string]string{}}
	app.config = parseConfig(nil, "", true)
	h, destroy := testControl(t, "LISTBOX", 0x151|0x200000, 400, 196, idModel)
	defer destroy()
	app.controls[idModel] = h
	send(h, 0x1A0, 0, 28)
	custom := `C:\models\my-model`
	setSelectedModel(custom)
	if got := selectedModel(); got != custom {
		t.Fatalf("custom selection changed: %q", got)
	}
	if count := int32(send(h, 0x18B, 0, 0)); count != 8 {
		t.Fatalf("custom choice missing: %d rows", count)
	}
	app.depsChecked = true
	app.deps["gpu"] = "ready"
	refreshModelDisplay()
	if got := selectedModel(); got != custom {
		t.Fatalf("auto label update changed custom selection: %q", got)
	}
	if label := modelListString(h, 0); label != "auto (large-v3-turbo)" {
		t.Fatalf("unexpected automatic model label: %q", label)
	}
	send(h, 0x186, 0, 0)
	if got := selectedModel(); got != "auto" {
		t.Fatalf("auto label leaked into saved model: %q", got)
	}
	app.actualModel = "small.en"
	refreshModelDisplay()
	if got := selectedModel(); got != "auto" || modelListString(h, 0) != "auto (small.en)" {
		t.Fatalf("actual model refresh changed selection: %q", got)
	}
	send(h, 0x100, 0x28, 0) // WM_KEYDOWN, VK_DOWN
	if got := selectedModel(); got != "large-v3-turbo" {
		t.Fatalf("keyboard navigation did not select the next model: %q", got)
	}
	send(h, 0x186, 7, 0)
	if got := selectedModel(); got != custom {
		t.Fatalf("custom choice could not be selected again: %q", got)
	}
	app.config.Path = filepath.Join(t.TempDir(), "joyvox_config.cfg")
	app.config.Values["STT_WHISPER_MODEL"] = selectedModel()
	if err := app.config.Save(); err != nil {
		t.Fatal(err)
	}
	reloaded := loadConfig(app.config.Path, true)
	setSelectedModel(reloaded.Values["STT_WHISPER_MODEL"])
	if got := selectedModel(); got != custom {
		t.Fatalf("custom choice did not survive a config save and reload: %q", got)
	}
}

func TestActivityFollowsBottomAndPreservesScrolledPosition(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := ole32.NewProc("CoInitializeEx").Call(0, 2)
	if hrOK(hr) {
		defer ole32.NewProc("CoUninitialize").Call()
	}
	previous := app
	defer func() { app = previous }()
	if _, err := syscall.LoadDLL("Msftedit.dll"); err != nil {
		t.Fatal(err)
	}
	app = AppState{dpi: 96, controls: map[int]uintptr{}}
	h, destroy := testControl(t, "RICHEDIT50W", 0x200000|4|0x40|0x800, 300, 100, idLog)
	defer destroy()
	app.controls[idLog] = h
	themeActivity(h)
	send(h, 0x435, 0, 16*1024*1024)
	beforeInfo := scrollInfo{Size: uint32(unsafe.Sizeof(scrollInfo{})), Mask: 0x7}
	user32.NewProc("GetScrollInfo").Call(h, 1, uintptr(unsafe.Pointer(&beforeInfo)))
	appendLog(strings.Repeat("An activity line with enough text to scroll.\r\n", 200))
	if !logAtBottom(h) {
		afterInfo := scrollInfo{Size: uint32(unsafe.Sizeof(scrollInfo{})), Mask: 0x7}
		user32.NewProc("GetScrollInfo").Call(h, 1, uintptr(unsafe.Pointer(&afterInfo)))
		t.Fatalf("initial activity did not follow the bottom: before %+v; after %+v", beforeInfo, afterInfo)
	}
	// Simulate a non-user reposition such as a layout change. Following stays
	// enabled until an actual scroll action changes the reader's intent.
	app.logUpdating = true
	send(h, 0x115, 6, 0) // SB_TOP
	app.logUpdating = false
	appendLog("A new line after programmatic repositioning.")
	if !logAtBottom(h) {
		t.Fatal("activity stopped following after a non-user reposition")
	}
	send(h, 0x115, 6, 0) // SB_TOP
	if logAtBottom(h) {
		t.Fatal("test control did not scroll away from the bottom")
	}
	if app.logFollowing {
		t.Fatal("user scroll did not suspend following")
	}
	selection := charRange{Min: 10, Max: 18}
	send(h, 0x437, 0, uintptr(unsafe.Pointer(&selection)))
	var before Point
	send(h, 0x4DD, 0, uintptr(unsafe.Pointer(&before)))
	appendLog("A new line while reading older activity.")
	var after Point
	var actualSelection charRange
	send(h, 0x4DD, 0, uintptr(unsafe.Pointer(&after)))
	send(h, 0x434, 0, uintptr(unsafe.Pointer(&actualSelection)))
	if after != before || actualSelection != selection {
		t.Fatalf("reading position changed: scroll %v -> %v, selection %v -> %v", before, after, selection, actualSelection)
	}
	send(h, 0x115, 7, 0) // SB_BOTTOM
	appendLog("A new line while at the bottom.")
	if !logAtBottom(h) || !strings.Contains(getText(h), "A new line while at the bottom.") {
		t.Fatal("activity did not follow the bottom after scrolling back down")
	}
}
