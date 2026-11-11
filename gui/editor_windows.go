package main

import (
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"unsafe"
)

type EditorState struct {
	Config   Config
	Keys     []string
	Selected int
	Loading  bool
	Controls map[int]uintptr
	Caption  uintptr
}

func setCell(list uintptr, row, column int, value string) {
	p := up(value)
	item := LVItem{Item: int32(row), Sub: int32(column), Text: p}
	send(list, 0x1074, uintptr(row), uintptr(unsafe.Pointer(&item)))
	runtime.KeepAlive(p)
}
func editorCommit() {
	s := app.editorState
	if s == nil || s.Loading || s.Selected < 0 || s.Selected >= len(s.Keys) {
		return
	}
	key := s.Keys[s.Selected]
	value := getText(s.Controls[idCfgValue])
	if isBoolean(key) {
		value = "no"
		if send(s.Controls[idCfgBool], 0x147, 0, 0) == 1 {
			value = "yes"
		}
	}
	s.Config.Values[key] = value
	setCell(s.Controls[idCfgList], s.Selected, 1, value)
}
func editorSelect(index int) {
	s := app.editorState
	if s == nil || index < 0 || index >= len(s.Keys) || s.Loading {
		return
	}
	editorCommit()
	s.Selected = index
	s.Loading = true
	key := s.Keys[index]
	info := settingFor(key)
	setText(s.Caption, info.Label+"  ·  "+key)
	setText(s.Controls[idCfgValue], s.Config.Values[key])
	boolean := isBoolean(key)
	showWindow(s.Controls[idCfgBool], boolean)
	showWindow(s.Controls[idCfgValue], !boolean)
	showWindow(s.Controls[idCfgBrowse], !boolean && (isFolder(key) || isFile(key)))
	if boolean {
		v := uintptr(0)
		if truth(s.Config.Values[key]) {
			v = 1
		}
		send(s.Controls[idCfgBool], 0x14E, v, 0)
	}
	setText(s.Controls[idCfgHelp], info.Help)
	s.Loading = false
}
func editorLayout() {
	s := app.editorState
	if s == nil || s.Controls[idCfgList] == 0 {
		return
	}
	r := getClient(app.editor)
	w, h := int(r.Right), int(r.Bottom)
	margin, gap := sc(18), sc(9)
	listH := max(sc(160), h-sc(235))
	move(s.Controls[idCfgList], margin, margin, w-2*margin, listH)
	// GetClientRect excludes the native vertical scrollbar when it is present.
	// Keep a further small allowance for borders/rounding; never size columns
	// against the outer window width.
	available := int(getClient(s.Controls[idCfgList]).Right)
	widths := fitColumns(available, app.dpi)
	for i, width := range widths {
		send(s.Controls[idCfgList], 0x101E, uintptr(i), uintptr(width))
	}
	user32.NewProc("ShowScrollBar").Call(s.Controls[idCfgList], 0, 0)
	y := margin + listH + gap
	move(s.Caption, margin, y, w-2*margin, sc(23))
	y += sc(27)
	move(s.Controls[idCfgValue], margin, y, w-2*margin-sc(99), sc(30))
	move(s.Controls[idCfgBool], margin, y, sc(150), sc(150))
	move(s.Controls[idCfgBrowse], w-margin-sc(90), y, sc(90), sc(30))
	y += sc(39)
	move(s.Controls[idCfgHelp], margin, y, w-2*margin, max(sc(46), h-y-sc(64)))
	move(s.Controls[idCfgSave], w-margin-sc(225), h-margin-sc(35), sc(125), sc(35))
	move(s.Controls[idCfgCancel], w-margin-sc(90), h-margin-sc(35), sc(90), sc(35))
}
func closeEditor() {
	if app.editor != 0 {
		enabled(app.window, true)
		user32.NewProc("DestroyWindow").Call(app.editor)
		app.editor = 0
		app.editorState = nil
		user32.NewProc("SetForegroundWindow").Call(app.window)
	}
}
func saveEditor() {
	s := app.editorState
	if s == nil {
		return
	}
	editorCommit()
	oldExe := resolve(app.root, app.config.Values["STT_KINGPIN_EXE"])
	exe := resolve(app.root, s.Config.Values["STT_KINGPIN_EXE"])
	if !strings.EqualFold(exe, oldExe) && exe != "" && s.Config.Values["STT_OUTPUT_PATH"] == app.config.Values["STT_OUTPUT_PATH"] {
		s.Config.Values["STT_OUTPUT_PATH"] = defaultOutput(exe)
	}
	for _, key := range []string{"STT_EXEC_FILE", "STT_OUTPUT_FILE"} {
		if !cfgFilename.MatchString(s.Config.Values[key]) {
			messageBox(app.editor, key+" must be a plain filename ending in .cfg, without folders or spaces.", "Configuration", 0x30)
			return
		}
	}
	if strings.EqualFold(s.Config.Values["STT_EXEC_FILE"], s.Config.Values["STT_OUTPUT_FILE"]) {
		messageBox(app.editor, "Game bindings and chat output must use different .cfg filenames.", "Configuration", 0x30)
		return
	}
	if exe != "" {
		addGameAllowList(&s.Config, exe)
	}
	if err := s.Config.Save(); err != nil {
		messageBox(app.editor, "Could not save settings: "+err.Error(), "Configuration", 0x10)
		return
	}
	app.config = s.Config
	closeEditor()
	refreshForm(false)
	nativeMics()
	queryMics()
	appendLog("Configuration saved. New recognition settings apply on the next Start.")
}
func editorBrowse() {
	s := app.editorState
	if s == nil || s.Selected < 0 {
		return
	}
	key := s.Keys[s.Selected]
	initial := resolve(app.root, getText(s.Controls[idCfgValue]))
	chosen := ""
	if isFolder(key) {
		chosen = browseFolder(app.editor, initial)
	} else if isFile(key) {
		chosen = browseFile(app.editor, initial, key == "STT_KINGPIN_EXE")
	}
	if chosen != "" {
		setText(s.Controls[idCfgValue], chosen)
		editorCommit()
	}
}
func editorProc(hwnd uintptr, msg uint32, wparam, lparam uintptr) uintptr {
	switch msg {
	case 0x1: // WM_CREATE
		app.editor = hwnd
		s := app.editorState
		s.Controls = map[int]uintptr{}
		s.Selected = -1
		s.Loading = true
		s.Controls[idCfgList] = control(hwnd, "SysListView32", "", tabstop|0x00200000|1|4|8, idCfgList, 0x200)
		list := s.Controls[idCfgList]
		send(list, 0x1036, 0, 0x20|0x10000|1)
		applyTheme(list)
		send(list, 0x1001, 0, panel)
		send(list, 0x1024, 0, fg)
		send(list, 0x1026, 0, panel)
		for i, title := range []string{"Setting", "Value", "Description"} {
			p := up(title)
			col := LVColumn{Mask: 1 | 2 | 4, Width: 150, Text: p}
			send(list, 0x1061, uintptr(i), uintptr(unsafe.Pointer(&col)))
			runtime.KeepAlive(p)
		}
		s.Caption = control(hwnd, "STATIC", "Select a setting to edit it.", 0, 0, 0)
		s.Controls[idCfgValue] = control(hwnd, "EDIT", "", tabstop|0x80, idCfgValue, 0x200)
		send(s.Controls[idCfgValue], 0xC5, 32767, 0)
		s.Controls[idCfgBool] = control(hwnd, "COMBOBOX", "", tabstop|3|0x200000, idCfgBool, 0)
		addString(s.Controls[idCfgBool], "No")
		addString(s.Controls[idCfgBool], "Yes")
		s.Controls[idCfgBrowse] = control(hwnd, "BUTTON", "Browse…", tabstop, idCfgBrowse, 0)
		s.Controls[idCfgHelp] = control(hwnd, "STATIC", "", 0, idCfgHelp, 0)
		s.Controls[idCfgSave] = control(hwnd, "BUTTON", "Save settings", tabstop|1, idCfgSave, 0)
		s.Controls[idCfgCancel] = control(hwnd, "BUTTON", "Cancel", tabstop, idCfgCancel, 0)
		for i, key := range s.Keys {
			info := settingFor(key)
			p := up(info.Label)
			item := LVItem{Mask: 1, Item: int32(i), Text: p}
			send(list, 0x104D, 0, uintptr(unsafe.Pointer(&item)))
			runtime.KeepAlive(p)
			setCell(list, i, 1, s.Config.Values[key])
			setCell(list, i, 2, info.Help)
		}
		s.Loading = false
		state := LVItem{State: 3, StateMask: 3}
		send(list, 0x102B, 0, uintptr(unsafe.Pointer(&state)))
		editorSelect(0)
		editorLayout()
		return 0
	case 0x2C:
		if measureComboItem(lparam) {
			return 1
		}
		return 0
	case 0x2B:
		if drawOwnerButton(lparam) || drawComboItem(lparam) {
			return 1
		}
		return 0
	case 0x5:
		editorLayout()
		return 0
	case 0x24:
		m := (*MinMax)(unsafe.Pointer(lparam))
		m.MinTrack = Point{int32(sc(760)), int32(sc(560))}
		return 0
	case 0x4e:
		n := (*NMHDR)(unsafe.Pointer(lparam))
		if int(n.ID) == idCfgList && n.Code == -101 {
			v := (*NMListView)(unsafe.Pointer(lparam))
			if v.NewState&2 != 0 && v.OldState&2 == 0 && v.Item >= 0 {
				editorSelect(int(v.Item))
			}
		}
		return 0
	case 0x111:
		id, code := int(wparam&0xffff), int((wparam>>16)&0xffff)
		switch id {
		case idCfgSave:
			if code == 0 {
				saveEditor()
			}
		case idCfgCancel:
			if code == 0 {
				closeEditor()
			}
		case idCfgBrowse:
			if code == 0 {
				editorBrowse()
			}
		}
		return 0
	case 0x133, 0x134:
		gdi32.NewProc("SetTextColor").Call(wparam, fg)
		gdi32.NewProc("SetBkColor").Call(wparam, cerulean)
		return app.inputBackground
	case 0x138: // WM_CTLCOLORSTATIC
		gdi32.NewProc("SetBkMode").Call(wparam, 1)
		gdi32.NewProc("SetTextColor").Call(wparam, fg)
		return app.background
	case 0x10:
		closeEditor()
		return 0
	}
	r, _, _ := user32.NewProc("DefWindowProcW").Call(hwnd, uintptr(msg), wparam, lparam)
	return r
}
func openEditor() {
	if !fieldsUnlocked() || !saveMain(false) {
		return
	}
	if app.editor != 0 {
		user32.NewProc("SetForegroundWindow").Call(app.editor)
		return
	}
	s := &EditorState{Config: app.config.Copy(), Selected: -1}
	seen := map[string]bool{}
	for _, info := range settings {
		s.Keys = append(s.Keys, info.Key)
		seen[info.Key] = true
	}
	var extra []string
	for key := range s.Config.Values {
		if !seen[key] {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)
	s.Keys = append(s.Keys, extra...)
	app.editorState = s
	class, title := up("JoyVoxConfig421"), up("JoyVox configuration")
	var parent Rect
	user32.NewProc("GetWindowRect").Call(app.window, uintptr(unsafe.Pointer(&parent)))
	h, _, err := user32.NewProc("CreateWindowExW").Call(0x10000, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(title)), 0x00CF0000, uintptr(parent.Left+scInt32(15)), uintptr(parent.Top+scInt32(20)), uintptr(sc(960)), uintptr(sc(700)), app.window, 0, app.instance, 0)
	runtime.KeepAlive(class)
	runtime.KeepAlive(title)
	if h == 0 {
		app.editorState = nil
		alert(fmt.Sprintf("Could not open configuration: %v", err))
		return
	}
	app.editor = h
	enabled(app.window, false)
	showWindow(h, true)
	editorLayout()
	user32.NewProc("SetFocus").Call(s.Controls[idCfgList])
}
func scInt32(v int) int32 { return int32(sc(v)) }

// Keep the saved executable spelling for the dynamic launch caption.
func extensionIsExe(path string) bool { return strings.EqualFold(filepath.Ext(path), ".exe") }

var editorCallback = syscall.NewCallback(editorProc)
