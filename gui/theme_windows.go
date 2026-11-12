package main

// Keep native checkbox/combobox behavior, but own the pixels that Windows'
// visual styles otherwise paint with light-theme colors. No global theme APIs
// or undocumented system-wide changes are needed.
// Win32 references:
// https://learn.microsoft.com/windows/win32/api/commctrl/nf-commctrl-setwindowsubclass
// https://learn.microsoft.com/windows/win32/controls/wm-drawitem
// https://learn.microsoft.com/windows/win32/api/winuser/ns-winuser-comboboxinfo
import (
	"math"
	"runtime"
	"syscall"
	"unsafe"
)

const (
	wmPaint           = 0x000F
	wmPrintClient     = 0x0318
	wmEraseBackground = 0x0014
	wmNCDestroy       = 0x0082
)

type PaintStruct struct {
	HDC                uintptr
	Erase              int32
	Paint              Rect
	Restore, IncUpdate int32
	Reserved           [32]byte
}
type ComboBoxInfo struct {
	Size              uint32
	Item, Button      Rect
	ButtonState       uint32
	Combo, Edit, List uintptr
}
type MeasureItemStruct struct {
	CtlType, CtlID, ItemID, ItemWidth, ItemHeight uint32
	ItemData                                      uintptr
}

var checkboxCallback, comboCallback, activityCallback uintptr

func init() {
	checkboxCallback = syscall.NewCallback(checkboxSubclassProc)
	comboCallback = syscall.NewCallback(comboSubclassProc)
	activityCallback = syscall.NewCallback(activitySubclassProc)
}

func themeActivity(h uintptr) {
	comctl32.NewProc("SetWindowSubclass").Call(h, activityCallback, 1, 0)
}

func activitySubclassProc(h uintptr, msg uint32, w, l, id, ref uintptr) uintptr {
	if msg == wmNCDestroy {
		comctl32.NewProc("RemoveWindowSubclass").Call(h, activityCallback, id)
		return defSubclass(h, msg, w, l)
	}
	result := defSubclass(h, msg, w, l)
	if !app.logUpdating {
		switch msg {
		case 0x115, 0x20A, 0x100, 0x202: // vertical scroll, wheel, key, mouse release
			app.logFollowing = logAtBottom(h)
		case 0x200: // selection drag can scroll the edit control
			if w&1 != 0 {
				app.logFollowing = logAtBottom(h)
			}
		}
	}
	return result
}

func defSubclass(h uintptr, msg uint32, w, l uintptr) uintptr {
	r, _, _ := comctl32.NewProc("DefSubclassProc").Call(h, uintptr(msg), w, l)
	return r
}
func disableControlTheme(h uintptr) {
	p := up("")
	uxtheme.NewProc("SetWindowTheme").Call(h, uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(p)))
	runtime.KeepAlive(p)
}
func measureText(owner, font uintptr, text string) Point {
	dc, _, _ := user32.NewProc("GetDC").Call(owner)
	if dc == 0 {
		return Point{}
	}
	defer user32.NewProc("ReleaseDC").Call(owner, dc)
	return measureTextDC(dc, font, text)
}
func measureTextDC(dc, font uintptr, text string) Point {
	old, _, _ := gdi32.NewProc("SelectObject").Call(dc, font)
	defer gdi32.NewProc("SelectObject").Call(dc, old)
	value, _ := syscall.UTF16FromString(text)
	var size Point
	gdi32.NewProc("GetTextExtentPoint32W").Call(dc, uintptr(unsafe.Pointer(&value[0])), uintptr(len(value)-1), uintptr(unsafe.Pointer(&size)))
	runtime.KeepAlive(value)
	return size
}
func fillRectColor(dc uintptr, r Rect, ink uintptr) {
	brush, _, _ := gdi32.NewProc("CreateSolidBrush").Call(ink)
	user32.NewProc("FillRect").Call(dc, uintptr(unsafe.Pointer(&r)), brush)
	gdi32.NewProc("DeleteObject").Call(brush)
}
func frameRectColor(dc uintptr, r Rect, ink uintptr) {
	brush, _, _ := gdi32.NewProc("CreateSolidBrush").Call(ink)
	user32.NewProc("FrameRect").Call(dc, uintptr(unsafe.Pointer(&r)), brush)
	gdi32.NewProc("DeleteObject").Call(brush)
}
func inset(r Rect, amount int32) Rect {
	return Rect{r.Left + amount, r.Top + amount, r.Right - amount, r.Bottom - amount}
}
func drawText(dc, font uintptr, text string, r Rect, ink uintptr, flags uint32) {
	old, _, _ := gdi32.NewProc("SelectObject").Call(dc, font)
	gdi32.NewProc("SetBkMode").Call(dc, 1)
	gdi32.NewProc("SetTextColor").Call(dc, ink)
	p := up(text)
	user32.NewProc("DrawTextW").Call(dc, uintptr(unsafe.Pointer(p)), ^uintptr(0), uintptr(unsafe.Pointer(&r)), uintptr(flags|0x800)) // DT_NOPREFIX
	runtime.KeepAlive(p)
	gdi32.NewProc("SelectObject").Call(dc, old)
}
func drawLine(dc uintptr, points []Point, ink uintptr, width int) {
	if len(points) < 2 {
		return
	}
	pen, _, _ := gdi32.NewProc("CreatePen").Call(0, uintptr(width), ink)
	old, _, _ := gdi32.NewProc("SelectObject").Call(dc, pen)
	gdi32.NewProc("Polyline").Call(dc, uintptr(unsafe.Pointer(&points[0])), uintptr(len(points)))
	gdi32.NewProc("SelectObject").Call(dc, old)
	gdi32.NewProc("DeleteObject").Call(pen)
	runtime.KeepAlive(points)
}
func fillPolygon(dc uintptr, points []Point, ink uintptr) {
	if len(points) < 3 {
		return
	}
	brush, _, _ := gdi32.NewProc("CreateSolidBrush").Call(ink)
	pen, _, _ := gdi32.NewProc("GetStockObject").Call(8) // NULL_PEN
	oldB, _, _ := gdi32.NewProc("SelectObject").Call(dc, brush)
	oldP, _, _ := gdi32.NewProc("SelectObject").Call(dc, pen)
	gdi32.NewProc("Polygon").Call(dc, uintptr(unsafe.Pointer(&points[0])), uintptr(len(points)))
	gdi32.NewProc("SelectObject").Call(dc, oldB)
	gdi32.NewProc("SelectObject").Call(dc, oldP)
	gdi32.NewProc("DeleteObject").Call(brush)
	runtime.KeepAlive(points)
}
func fillEllipse(dc uintptr, r Rect, ink uintptr) {
	brush, _, _ := gdi32.NewProc("CreateSolidBrush").Call(ink)
	pen, _, _ := gdi32.NewProc("GetStockObject").Call(8)
	oldB, _, _ := gdi32.NewProc("SelectObject").Call(dc, brush)
	oldP, _, _ := gdi32.NewProc("SelectObject").Call(dc, pen)
	gdi32.NewProc("Ellipse").Call(dc, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right), uintptr(r.Bottom))
	gdi32.NewProc("SelectObject").Call(dc, oldB)
	gdi32.NewProc("SelectObject").Call(dc, oldP)
	gdi32.NewProc("DeleteObject").Call(brush)
}
func shade(ink uintptr, delta int) uintptr {
	c := func(n uintptr) uint32 { return uint32(max(0, min(255, int(n&255)+delta))) }
	return color(c(ink), c(ink>>8), c(ink>>16))
}
func isEnabled(h uintptr) bool {
	v, _, _ := user32.NewProc("IsWindowEnabled").Call(h)
	return v != 0
}

func themeCheckbox(h uintptr) {
	disableControlTheme(h)
	comctl32.NewProc("SetWindowSubclass").Call(h, checkboxCallback, 1, 0)
}
func paintCheckbox(h, dc uintptr) {
	r := getClient(h)
	fillRectColor(dc, r, panel)
	ink := fg
	if !isEnabled(h) {
		ink = muted
	}
	size := int32(sc(14))
	top := (r.Bottom - size) / 2
	box := Rect{0, top, size, top + size}
	fillRectColor(dc, box, cerulean)
	frameRectColor(dc, box, muted)
	if send(h, 0xF0, 0, 0) == 1 {
		x, y := box.Left, box.Top
		drawLine(dc, []Point{{x + int32(sc(3)), y + int32(sc(7))}, {x + int32(sc(6)), y + int32(sc(10))}, {x + int32(sc(12)), y + int32(sc(3))}}, ink, max(1, sc(2)))
	}
	r.Left = size + int32(sc(7))
	drawText(dc, app.font, getText(h), r, ink, 0x20|0x4|0x8000)
	focus, _, _ := user32.NewProc("GetFocus").Call()
	if focus == h {
		focusRect := inset(r, 1)
		user32.NewProc("DrawFocusRect").Call(dc, uintptr(unsafe.Pointer(&focusRect)))
	}
}
func checkboxSubclassProc(h uintptr, msg uint32, w, l, id, ref uintptr) uintptr {
	switch msg {
	case wmPaint:
		var ps PaintStruct
		dc, _, _ := user32.NewProc("BeginPaint").Call(h, uintptr(unsafe.Pointer(&ps)))
		paintCheckbox(h, dc)
		user32.NewProc("EndPaint").Call(h, uintptr(unsafe.Pointer(&ps)))
		return 0
	case wmPrintClient:
		paintCheckbox(h, w)
		return 0
	case wmEraseBackground:
		return 1
	case wmNCDestroy:
		comctl32.NewProc("RemoveWindowSubclass").Call(h, checkboxCallback, id)
	}
	return defSubclass(h, msg, w, l)
}

func sizeCombo(h uintptr) {
	send(h, 0x153, ^uintptr(0), uintptr(sc(23))) // selection-field height
	send(h, 0x153, 0, uintptr(sc(28)))           // list row height
}
func themeCombo(h uintptr) {
	disableControlTheme(h)
	comctl32.NewProc("SetWindowSubclass").Call(h, comboCallback, 1, 0)
	var info ComboBoxInfo
	info.Size = uint32(unsafe.Sizeof(info))
	if ok, _, _ := user32.NewProc("GetComboBoxInfo").Call(h, uintptr(unsafe.Pointer(&info))); ok != 0 {
		if info.Edit != 0 {
			disableControlTheme(info.Edit)
		}
		if info.List != 0 {
			disableControlTheme(info.List)
		}
	}
	sizeCombo(h)
}
func measureComboItem(l uintptr) bool {
	if l == 0 {
		return false
	}
	m := (*MeasureItemStruct)(unsafe.Pointer(l))
	if m.CtlType != 3 {
		return false
	}
	m.ItemHeight = uint32(sc(28))
	return true
}
func measureModelItem(l uintptr) bool {
	if l == 0 {
		return false
	}
	m := (*MeasureItemStruct)(unsafe.Pointer(l))
	if m.CtlType != 2 || int(m.CtlID) != idModel { // ODT_LISTBOX
		return false
	}
	m.ItemHeight = uint32(sc(28))
	return true
}
func comboString(h uintptr, index uint32) string {
	if index == ^uint32(0) {
		return getText(h)
	}
	n := int32(send(h, 0x149, uintptr(index), 0)) // CB_GETLBTEXTLEN
	if n < 0 || n > 32767 {
		return ""
	}
	b := make([]uint16, int(n)+1)
	send(h, 0x148, uintptr(index), uintptr(unsafe.Pointer(&b[0])))
	return syscall.UTF16ToString(b)
}
func drawComboItem(l uintptr) bool {
	if l == 0 {
		return false
	}
	di := (*DrawItemStruct)(unsafe.Pointer(l))
	if di.CtlType != 3 {
		return false
	}
	bg, ink := cerulean, fg
	isField := di.ItemState&0x1000 != 0 // ODS_COMBOBOXEDIT, not a drop-down row
	if di.ItemState&1 != 0 && !isField {
		bg = ceruleanHi
	}
	if !isEnabled(di.HwndItem) {
		ink = muted
	}
	fillRectColor(di.HDC, di.RcItem, bg)
	r := di.RcItem
	r.Left += int32(sc(7))
	r.Right -= int32(sc(5))
	text := comboString(di.HwndItem, di.ItemID)
	drawText(di.HDC, app.font, text, r, ink, 0x20|0x4|0x8000)
	if di.ItemState&0x10 != 0 {
		r = inset(di.RcItem, 2)
		user32.NewProc("DrawFocusRect").Call(di.HDC, uintptr(unsafe.Pointer(&r)))
	}
	return true
}

func drawModelItem(l uintptr) bool {
	if l == 0 {
		return false
	}
	di := (*DrawItemStruct)(unsafe.Pointer(l))
	if di.CtlType != 2 || int(di.CtlID) != idModel {
		return false
	}
	bg, ink := cerulean, fg
	if di.ItemState&1 != 0 { // ODS_SELECTED
		bg = ceruleanHi
	}
	if !isEnabled(di.HwndItem) {
		ink = muted
	}
	fillRectColor(di.HDC, di.RcItem, bg)
	if di.ItemID != ^uint32(0) {
		r := di.RcItem
		r.Left += int32(sc(7))
		r.Right -= int32(sc(5))
		name := modelListString(di.HwndItem, int(di.ItemID))
		if canonicalModel(name) == "auto" {
			drawText(di.HDC, app.font, name, r, ink, 0x20|0x4|0x8000)
		} else {
			nameW := measureTextDC(di.HDC, app.font, name).X
			nameRect := r
			nameRect.Right = min(r.Right, r.Left+nameW+int32(sc(2)))
			drawText(di.HDC, app.font, name, nameRect, ink, 0x20|0x4|0x8000)
			r.Left += nameW + int32(sc(14))
			if r.Left < r.Right {
				drawText(di.HDC, app.smallItalicFont, modelDescription(name), r, muted, 0x20|0x4|0x8000)
			}
		}
	}
	if di.ItemState&0x10 != 0 { // ODS_FOCUS
		r := inset(di.RcItem, 2)
		user32.NewProc("DrawFocusRect").Call(di.HDC, uintptr(unsafe.Pointer(&r)))
	}
	return true
}
func paintComboChrome(h, dc uintptr) {
	var info ComboBoxInfo
	info.Size = uint32(unsafe.Sizeof(info))
	ok, _, _ := user32.NewProc("GetComboBoxInfo").Call(h, uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		return
	}
	bg, ink := cerulean, fg
	if info.ButtonState&8 != 0 {
		bg = ceruleanHi
	}
	if !isEnabled(h) {
		ink = muted
	}
	fillRectColor(dc, info.Button, bg)
	r := info.Button
	cx, cy := (r.Left+r.Right)/2, (r.Top+r.Bottom)/2
	a := int32(sc(4))
	fillPolygon(dc, []Point{{cx - a, cy - 2}, {cx + a, cy - 2}, {cx, cy + 2}}, ink)
	frameRectColor(dc, getClient(h), outline)
}
func comboSubclassProc(h uintptr, msg uint32, w, l, id, ref uintptr) uintptr {
	switch msg {
	case wmPaint:
		result := defSubclass(h, msg, w, l)
		dc, _, _ := user32.NewProc("GetDC").Call(h)
		paintComboChrome(h, dc)
		user32.NewProc("ReleaseDC").Call(h, dc)
		return result
	case wmPrintClient:
		result := defSubclass(h, msg, w, l)
		paintComboChrome(h, w)
		return result
	case 0x133, 0x134, 0x138: // edit/list/static children of the native combo
		gdi32.NewProc("SetTextColor").Call(w, fg)
		gdi32.NewProc("SetBkColor").Call(w, cerulean)
		return app.inputBackground
	case wmNCDestroy:
		comctl32.NewProc("RemoveWindowSubclass").Call(h, comboCallback, id)
	}
	return defSubclass(h, msg, w, l)
}

func buttonIcon(dc uintptr, id int, x, y, size int32, ink, bg uintptr) {
	if id == idStart {
		fillPolygon(dc, []Point{{x, y}, {x, y + size}, {x + size, y + size/2}}, ink)
	} else if id == idStop {
		fillRectColor(dc, Rect{x, y, x + size, y + size}, ink)
	} else if id == idConfig {
		cx, cy := float64(x+size/2), float64(y+size/2)
		points := make([]Point, 32)
		for i := range points {
			radius := float64(size) / 2
			if i%4 == 0 || i%4 == 3 {
				radius *= .70
			}
			angle := float64(i) * math.Pi / 16
			points[i] = Point{int32(math.Round(cx + math.Cos(angle)*radius)), int32(math.Round(cy + math.Sin(angle)*radius))}
		}
		fillPolygon(dc, points, ink)
		center := size / 4
		fillEllipse(dc, Rect{x + size/2 - center, y + size/2 - center, x + size/2 + center + 1, y + size/2 + center + 1}, bg)
	} else if id == idExeBrowse || id == idOutputBrowse {
		stroke := max(1, sc(2))
		drawLine(dc, []Point{{x + size/10, y + size*2/5}, {x + size/10, y + size/5}, {x + size*2/5, y + size/5}, {x + size/2, y + size*3/10}, {x + size*9/10, y + size*3/10}, {x + size*9/10, y + size*2/5}}, ink, stroke)
		drawLine(dc, []Point{{x + size/10, y + size*4/5}, {x + size*2/10, y + size*2/5}, {x + size, y + size*2/5}, {x + size*8/10, y + size*4/5}, {x + size/10, y + size*4/5}}, ink, stroke)
	} else if id == idMicRefresh {
		stroke := max(1, sc(2))
		drawLine(dc, []Point{{x + size/5, y + size/2}, {x + size/5, y + size/3}, {x + size*2/5, y + size/6}, {x + size*3/5, y + size/6}, {x + size*4/5, y + size/3}}, ink, stroke)
		fillPolygon(dc, []Point{{x + size*4/5, y + size/7}, {x + size*9/10, y + size*2/5}, {x + size*3/5, y + size/3}}, ink)
		drawLine(dc, []Point{{x + size*4/5, y + size/2}, {x + size*4/5, y + size*2/3}, {x + size*3/5, y + size*5/6}, {x + size*2/5, y + size*5/6}, {x + size/5, y + size*2/3}}, ink, stroke)
		fillPolygon(dc, []Point{{x + size/5, y + size*6/7}, {x + size/10, y + size*3/5}, {x + size*2/5, y + size*2/3}}, ink)
	}
}
func drawOwnerButton(l uintptr) bool {
	if l == 0 {
		return false
	}
	di := (*DrawItemStruct)(unsafe.Pointer(l))
	if di.CtlType != 4 {
		return false
	}
	id := int(di.CtlID)
	bg, ink := greyBtn, fg
	if id == idConfig {
		bg = panel
	}
	enabled := isEnabled(di.HwndItem) && di.ItemState&4 == 0
	if enabled {
		switch id {
		case idInstall:
			if !isRunning("install") && needsDependencyInstall() {
				bg = installBlue
			}
		case idStart:
			if !isRunning("backend") {
				bg = greenDark
			}
		case idStop:
			if isRunning("backend") {
				bg = redDark
			}
		}
		if di.ItemState&1 != 0 {
			bg = shade(bg, -12)
		} else if di.ItemState&0x40 != 0 {
			bg = shade(bg, 9)
		}
	} else {
		ink = muted
	}
	fillRectColor(di.HDC, di.RcItem, bg)
	if id != idConfig {
		frameRectColor(di.HDC, di.RcItem, shade(bg, 10))
	}
	font := send(di.HwndItem, 0x31, 0, 0)
	if font == 0 {
		font = app.font
	}
	r := inset(di.RcItem, int32(sc(6)))
	if di.ItemState&1 != 0 {
		r.Top++
		r.Left++
	}
	text := getText(di.HwndItem)
	if id == idConfig || id == idExeBrowse || id == idOutputBrowse || id == idMicRefresh {
		size := int32(sc(22))
		if id == idConfig {
			size = int32(sc(24))
		}
		buttonIcon(di.HDC, id, (r.Left+r.Right-size)/2, (r.Top+r.Bottom-size)/2, size, ink, bg)
	} else if id == idStart || id == idStop {
		size, gap := int32(sc(13)), int32(sc(8))
		textW := measureTextDC(di.HDC, font, text).X
		x := r.Left + max(0, (r.Right-r.Left-textW-size-gap)/2)
		y := (r.Top + r.Bottom - size) / 2
		buttonIcon(di.HDC, id, x, y, size, ink, bg)
		r.Left = x + size + gap
		drawText(di.HDC, font, text, r, ink, 0x20|0x4|0x8000)
	} else {
		drawText(di.HDC, font, text, r, ink, 1|0x4|0x20|0x8000)
	}
	if di.ItemState&0x10 != 0 {
		r = inset(di.RcItem, int32(sc(3)))
		user32.NewProc("DrawFocusRect").Call(di.HDC, uintptr(unsafe.Pointer(&r)))
	}
	return true
}
func paintMainFrame(h uintptr) {
	var ps PaintStruct
	dc, _, _ := user32.NewProc("BeginPaint").Call(h, uintptr(unsafe.Pointer(&ps)))
	frameRectColor(dc, app.dependencyRect, outline)
	user32.NewProc("EndPaint").Call(h, uintptr(unsafe.Pointer(&ps)))
}
