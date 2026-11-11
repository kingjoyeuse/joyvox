package main

import (
	"runtime"
	"syscall"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	comdlg32 = syscall.NewLazyDLL("comdlg32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	ole32    = syscall.NewLazyDLL("ole32.dll")
	comctl32 = syscall.NewLazyDLL("comctl32.dll")
	uxtheme  = syscall.NewLazyDLL("uxtheme.dll")
)

func up(s string) *uint16 { p, _ := syscall.UTF16PtrFromString(s); return p }
func send(h uintptr, m uint32, w, l uintptr) uintptr {
	r, _, _ := user32.NewProc("SendMessageW").Call(h, uintptr(m), w, l)
	return r
}
func setText(h uintptr, s string) {
	p := up(s)
	user32.NewProc("SetWindowTextW").Call(h, uintptr(unsafe.Pointer(p)))
	runtime.KeepAlive(p)
}
func getText(h uintptr) string {
	n, _, _ := user32.NewProc("GetWindowTextLengthW").Call(h)
	b := make([]uint16, n+1)
	user32.NewProc("GetWindowTextW").Call(h, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)))
	return syscall.UTF16ToString(b)
}
func enabled(h uintptr, b bool) {
	v := uintptr(0)
	if b {
		v = 1
	}
	user32.NewProc("EnableWindow").Call(h, v)
}
func showWindow(h uintptr, b bool) {
	v := uintptr(0)
	if b {
		v = 5
	}
	user32.NewProc("ShowWindow").Call(h, v)
}
func invalidate(h uintptr) { user32.NewProc("InvalidateRect").Call(h, 0, 1) }
func messageBox(owner uintptr, text, title string, flags uint32) uintptr {
	a, b := up(text), up(title)
	r, _, _ := user32.NewProc("MessageBoxW").Call(owner, uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(b)), uintptr(flags))
	runtime.KeepAlive(a)
	runtime.KeepAlive(b)
	return r
}
func alert(text string) { messageBox(app.window, text, "JoyVox", 0x30) }
func post(h uintptr, msg uint32, w, l uintptr) {
	user32.NewProc("PostMessageW").Call(h, uintptr(msg), w, l)
}
func setFont(h, font uintptr)  { send(h, 0x30, font, 1) }
func chooseStockIcon() uintptr { r, _, _ := user32.NewProc("LoadIconW").Call(0, 32512); return r }
func loadAppIcon(instance uintptr, size int) uintptr {
	r, _, _ := user32.NewProc("LoadImageW").Call(instance, 1, 1, uintptr(size), uintptr(size), 0)
	return r
}
func getClient(h uintptr) Rect {
	var r Rect
	user32.NewProc("GetClientRect").Call(h, uintptr(unsafe.Pointer(&r)))
	return r
}
func move(h uintptr, x, y, w, hgt int) {
	user32.NewProc("MoveWindow").Call(h, uintptr(x), uintptr(y), uintptr(w), uintptr(hgt), 1)
}
func color(r, g, b uint32) uintptr { return uintptr(r | (g << 8) | (b << 16)) }
func newFont(size, weight int, face string) uintptr {
	return newFontStyle(size, weight, false, face)
}
func newFontStyle(size, weight int, italic bool, face string) uintptr {
	p := up(face)
	it := uintptr(0)
	if italic {
		it = 1
	}
	r, _, _ := gdi32.NewProc("CreateFontW").Call(uintptr(-size), 0, 0, 0, uintptr(weight), it, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(p)))
	runtime.KeepAlive(p)
	return r
}
func applyTheme(h uintptr) {
	p := up("Explorer")
	uxtheme.NewProc("SetWindowTheme").Call(h, uintptr(unsafe.Pointer(p)), 0)
	runtime.KeepAlive(p)
}
func addString(h uintptr, s string) int {
	p := up(s)
	v := send(h, 0x143, 0, uintptr(unsafe.Pointer(p)))
	runtime.KeepAlive(p)
	return int(v)
}

type Point struct{ X, Y int32 }
type Rect struct{ Left, Top, Right, Bottom int32 }
type WinMsg struct {
	Hwnd           uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	Pt             Point
	Private        uint32
}
type WndClass struct {
	Size, Style                        uint32
	Proc                               uintptr
	ClsExtra, WndExtra                 int32
	Instance, Icon, Cursor, Background uintptr
	Menu, Name                         *uint16
	IconSmall                          uintptr
}
type MinMax struct{ Reserved, MaxSize, MaxPosition, MinTrack, MaxTrack Point }
type NMHDR struct {
	Hwnd, ID uintptr
	Code     int32
}
type NMListView struct {
	Header                      NMHDR
	Item, SubItem               int32
	NewState, OldState, Changed uint32
	Action                      Point
	Param                       uintptr
}
type LVColumn struct {
	Mask                                                       uint32
	Fmt, Width                                                 int32
	Text                                                       *uint16
	Max, Sub, Image, Order, MinWidth, DefaultWidth, IdealWidth int32
}
type LVItem struct {
	Mask                 uint32
	Item, Sub            int32
	State, StateMask     uint32
	Text                 *uint16
	Max, Image           int32
	Param                uintptr
	Indent, Group        int32
	Columns              uint32
	ColumnPtr, FormatPtr uintptr
	GroupIndex           int32
}
type CharFormat struct {
	Size, Mask, Effects uint32
	Height, Offset      int32
	Color               uint32
	CharSet, Pitch      byte
	Face                [32]uint16
}
type OpenFileName struct {
	Size                      uint32
	Owner, Instance           uintptr
	Filter, CustomFilter      *uint16
	MaxCustom, FilterIndex    uint32
	File                      *uint16
	MaxFile                   uint32
	FileTitle                 *uint16
	MaxFileTitle              uint32
	InitialDir, Title         *uint16
	Flags                     uint32
	FileOffset, FileExtension uint16
	DefExt                    *uint16
	CustomData, Hook          uintptr
	Template                  *uint16
	Reserved                  uintptr
	ReservedDword, FlagsEx    uint32
}
type GUID struct {
	D1     uint32
	D2, D3 uint16
	D4     [8]byte
}

func guid(s string) GUID {
	var g GUID
	p := up(s)
	ole32.NewProc("CLSIDFromString").Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&g)))
	return g
}
func comCall(obj uintptr, slot int, args ...uintptr) uintptr {
	if obj == 0 {
		return 0x80004003
	}
	table := *(*uintptr)(unsafe.Pointer(obj))
	fn := *(*uintptr)(unsafe.Pointer(table + uintptr(slot)*unsafe.Sizeof(uintptr(0))))
	all := append([]uintptr{obj}, args...)
	r, _, _ := syscall.SyscallN(fn, all...)
	return r
}
func hrOK(hr uintptr) bool { return int32(uint32(hr)) >= 0 }
func release(obj uintptr) {
	if obj != 0 {
		comCall(obj, 2)
	}
}
func wideAt(p uintptr) string {
	if p == 0 {
		return ""
	}
	b := make([]uint16, 0, 128)
	for i := uintptr(0); i < 32768; i++ {
		c := *(*uint16)(unsafe.Pointer(p + i*2))
		if c == 0 {
			break
		}
		b = append(b, c)
	}
	return syscall.UTF16ToString(b)
}
func browseFile(owner uintptr, initial string, executable bool) string {
	b := make([]uint16, 32768)
	copy(b, syscall.StringToUTF16(initial))
	filter := "Configuration / text files\x00*.cfg;*.txt\x00All files\x00*.*\x00\x00"
	title := "Choose the correction/custom dictionary"
	if executable {
		filter = "Windows programs (*.exe)\x00*.exe\x00All files\x00*.*\x00\x00"
		title = "Choose the game executable"
	}
	// StringToUTF16 rejects embedded NULs; build this double-NUL filter manually.
	f := make([]uint16, 0, len(filter))
	for _, c := range filter {
		f = append(f, uint16(c))
	}
	d := OpenFileName{Owner: owner, File: &b[0], MaxFile: uint32(len(b)), Filter: &f[0], Title: up(title), Flags: 0x1000 | 0x800 | 0x80000 | 8}
	d.Size = uint32(unsafe.Sizeof(d))
	ok, _, _ := comdlg32.NewProc("GetOpenFileNameW").Call(uintptr(unsafe.Pointer(&d)))
	runtime.KeepAlive(d)
	runtime.KeepAlive(f)
	if ok != 0 {
		return syscall.UTF16ToString(b)
	}
	return ""
}
func browseFolder(owner uintptr, initial string) string {
	cls := guid("{DC1C5A9C-E88A-4DDE-A5A1-60F82A20AEF7}")
	iid := guid("{D57C7288-D4AD-4768-BE02-9D969532D960}")
	var dlg uintptr
	r, _, _ := ole32.NewProc("CoCreateInstance").Call(uintptr(unsafe.Pointer(&cls)), 0, 1, uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&dlg)))
	if !hrOK(r) {
		return ""
	}
	defer release(dlg)
	var opts uint32
	comCall(dlg, 10, uintptr(unsafe.Pointer(&opts)))
	comCall(dlg, 9, uintptr(opts|0x20|0x40|0x800|8))
	title := up("Choose a folder")
	comCall(dlg, 17, uintptr(unsafe.Pointer(title)))
	if initial != "" {
		shellIID := guid("{43826D1E-E718-42EE-BC55-A1E261C37BFE}")
		var item uintptr
		p := up(initial)
		h, _, _ := shell32.NewProc("SHCreateItemFromParsingName").Call(uintptr(unsafe.Pointer(p)), 0, uintptr(unsafe.Pointer(&shellIID)), uintptr(unsafe.Pointer(&item)))
		if hrOK(h) {
			comCall(dlg, 12, item)
			release(item)
		}
		runtime.KeepAlive(p)
	}
	if !hrOK(comCall(dlg, 3, owner)) {
		return ""
	}
	var item uintptr
	if !hrOK(comCall(dlg, 20, uintptr(unsafe.Pointer(&item)))) {
		return ""
	}
	defer release(item)
	var p uintptr
	if !hrOK(comCall(item, 5, 0x80058000, uintptr(unsafe.Pointer(&p)))) {
		return ""
	}
	value := wideAt(p)
	ole32.NewProc("CoTaskMemFree").Call(p)
	return value
}

type MicOption struct{ Value, Name string }

// Query Windows Core Audio directly, before Python or PyAudio is installed.
func nativeMicrophones() ([]MicOption, string, error) {
	cls := guid("{BCDE0395-E52F-467C-8E3D-C4579291692E}")
	iid := guid("{A95664D2-9614-4F35-A746-DE8DB63617E6}")
	var enumerator uintptr
	r, _, e := ole32.NewProc("CoCreateInstance").Call(uintptr(unsafe.Pointer(&cls)), 0, 1, uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&enumerator)))
	if !hrOK(r) {
		return nil, "", e
	}
	defer release(enumerator)
	getName := func(device uintptr) string {
		var store uintptr
		if !hrOK(comCall(device, 4, 0, uintptr(unsafe.Pointer(&store)))) {
			return ""
		}
		defer release(store)
		key := struct {
			ID  GUID
			PID uint32
		}{guid("{A45C254E-DF1C-4EFD-8020-67D146A850E0}"), 14}
		v := struct {
			Type         uint16
			Reserved     [3]uint16
			Value, Extra uintptr
		}{}
		if !hrOK(comCall(store, 5, uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(&v)))) {
			return ""
		}
		name := ""
		if v.Type == 31 {
			name = wideAt(v.Value)
		}
		ole32.NewProc("PropVariantClear").Call(uintptr(unsafe.Pointer(&v)))
		return name
	}
	var def uintptr
	defaultName := ""
	if hrOK(comCall(enumerator, 4, 1, 0, uintptr(unsafe.Pointer(&def)))) {
		defaultName = getName(def)
		release(def)
	}
	var collection uintptr
	if !hrOK(comCall(enumerator, 3, 1, 1, uintptr(unsafe.Pointer(&collection)))) {
		return nil, defaultName, nil
	}
	defer release(collection)
	var count uint32
	comCall(collection, 3, uintptr(unsafe.Pointer(&count)))
	out := []MicOption{}
	seen := map[string]bool{}
	for i := uint32(0); i < count; i++ {
		var dev uintptr
		if !hrOK(comCall(collection, 4, uintptr(i), uintptr(unsafe.Pointer(&dev)))) {
			continue
		}
		name := getName(dev)
		release(dev)
		if name != "" && !seen[name] {
			out = append(out, MicOption{"name:" + name, name})
			seen[name] = true
		}
	}
	return out, defaultName, nil
}
