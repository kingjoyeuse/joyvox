// Native Windows launcher. Builds with the Go standard library only; no GUI
// framework, Python, .NET build step, or compiler is needed by end users.
package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

//go:embed payload
var payload embed.FS

const (
	wmEvent      = 0x8001
	wmInitialize = 0x8002
	idInstall    = 100 + iota
	idGPU
	idExe
	idExeBrowse
	idOutput
	idOutputBrowse
	idMic
	idMicRefresh
	idModel
	idModelInfo
	idConfig
	idLaunch
	idStart
	idStop
	idLog
	idClear
	idDeps
	idRun
	idEstimate
	idAutoexec
	idLogo
	idByline
	idBindTalk
	idBindPublicAuto
	idBindTeamAuto
	idBindPublicSend
	idBindTeamSend
)
const (
	idCfgList = 500 + iota
	idCfgValue
	idCfgBool
	idCfgBrowse
	idCfgSave
	idCfgCancel
	idCfgHelp
)
const child = 0x40000000
const visible = 0x10000000
const tabstop = 0x00010000

var fg = color(226, 235, 247)
var muted = color(171, 184, 199)
var blue = color(106, 171, 223)
var green = color(96, 189, 120)
var red = color(232, 120, 120)
var panel = color(19, 28, 39)
var panel2 = color(24, 37, 50)
var cerulean = color(43, 72, 95)
var ceruleanHi = color(54, 92, 120)
var greenDark = color(35, 92, 56)
var redDark = color(110, 42, 48)
var greyBtn = color(72, 82, 96)
var installBlue = color(34, 105, 151)
var outline = color(83, 96, 111)

type AppState struct {
	root                                                                                                                              string
	window, instance, editor                                                                                                          uintptr
	controls                                                                                                                          map[int]uintptr
	labels                                                                                                                            map[string]uintptr
	font, titleFont, logFont, smallFont, smallItalicFont, background, inputBackground, panelBackground, logBackground, inputEdgeBrush uintptr
	titleIcon                                                                                                                         uintptr
	dpi                                                                                                                               int
	config                                                                                                                            Config
	deps                                                                                                                              map[string]string
	jobs                                                                                                                              map[string]*Job
	micOptions                                                                                                                        []MicOption
	defaultMic, lastExe                                                                                                               string
	ready, initialized, closing, stopping, depError, runError, autoError                                                              bool
	stopDeadline, closeDeadline, lastAutoCheck                                                                                        time.Time
	pendingBindings                                                                                                                   bool
	capturingBind                                                                                                                     int
	editorState                                                                                                                       *EditorState
	depsChecked, modelUpdating                                                                                                        bool
	autoModelLabel, actualModel                                                                                                       string
	bindPreviousStatus                                                                                                                string
	bindPreviousError                                                                                                                 bool
	dependencyRect                                                                                                                    Rect
}

var app AppState

func sc(v int) int {
	if app.dpi == 0 {
		return v
	}
	return v * app.dpi / 96
}
func control(parent uintptr, class, text string, style uint32, id int, ex uint32) uintptr {
	if class == "BUTTON" && id != idGPU {
		style = (style &^ 0xF) | 0xB // BS_OWNERDRAW, with tab/navigation flags preserved
	}
	if class == "COMBOBOX" {
		style |= 0x10 | 0x200 // CBS_OWNERDRAWFIXED | CBS_HASSTRINGS
	}
	c, t := up(class), up(text)
	h, _, _ := user32.NewProc("CreateWindowExW").Call(uintptr(ex), uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(t)), uintptr(child|visible|style), 0, 0, 10, 10, parent, uintptr(id), app.instance, 0)
	runtime.KeepAlive(c)
	runtime.KeepAlive(t)
	setFont(h, app.font)
	switch class {
	case "COMBOBOX":
		themeCombo(h)
	case "BUTTON":
		if id == idGPU {
			themeCheckbox(h)
		}
	case "EDIT":
		disableControlTheme(h)
	}
	return h
}
func add(id int, class, text string, style uint32, ex uint32) uintptr {
	h := control(app.window, class, text, style, id, ex)
	app.controls[id] = h
	return h
}
func label(key, text string) uintptr {
	h := control(app.window, "STATIC", text, 0, 0, 0)
	app.labels[key] = h
	return h
}
func place(h uintptr, x, y, w, height int) { move(h, sc(x), sc(y), sc(w), sc(height)) }
func setFonts() {
	old := []uintptr{app.font, app.titleFont, app.logFont, app.smallFont, app.smallItalicFont}
	app.font = newFont(sc(15), 400, "Segoe UI")
	app.titleFont = newFont(sc(30), 600, "Segoe UI")
	app.logFont = newFont(sc(13), 400, "Consolas")
	app.smallFont = newFont(sc(12), 400, "Segoe UI")
	app.smallItalicFont = newFontStyle(sc(12), 400, true, "Segoe UI")
	for id, h := range app.controls {
		f := app.font
		switch id {
		case idLog:
			f = app.logFont
		case idEstimate, idAutoexec:
			f = app.smallFont
		case idModelInfo, idByline:
			f = app.smallItalicFont
		}
		setFont(h, f)
	}
	for key, h := range app.labels {
		f := app.font
		switch key {
		case "title":
			f = app.titleFont
		case "tagline":
			f = app.font
		}
		if strings.HasPrefix(key, "bind-") {
			f = app.smallFont
		}
		setFont(h, f)
	}
	for _, id := range []int{idMic, idModel} {
		if h := app.controls[id]; h != 0 {
			sizeCombo(h)
		}
	}
	for _, h := range old {
		if h != 0 {
			gdi32.NewProc("DeleteObject").Call(h)
		}
	}
}
func layout() {
	if app.controls[idLog] == 0 {
		return
	}
	r := getClient(app.window)
	w, h := int(r.Right)*96/app.dpi, int(r.Bottom)*96/app.dpi
	inner := w - 48
	place(app.controls[idLogo], 24, 14, 52, 52)
	// Measure the real caption instead of reserving 260px of empty space.
	titleSize := measureText(app.window, app.titleFont, getText(app.labels["title"]))
	bySize := measureText(app.window, app.smallItalicFont, "by Joyeuse")
	titleW := (int(titleSize.X)*96 + app.dpi - 1) / app.dpi
	place(app.labels["title"], 88, 14, titleW+2, 43)
	move(app.controls[idByline], sc(88)+int(titleSize.X)+sc(10), sc(14)+int(titleSize.Y-bySize.Y), int(bySize.X)+sc(4), int(bySize.Y)+sc(3))
	place(app.labels["tagline"], 88, 52, inner-64, 22)

	app.dependencyRect = Rect{int32(sc(24)), int32(sc(92)), int32(sc(w - 24)), int32(sc(238))}
	place(app.controls[idDeps], 38, 105, inner-292, 23)
	place(app.controls[idAutoexec], 38, 131, inner-292, 30)
	place(app.controls[idInstall], w-277, 105, 239, 35)
	place(app.controls[idGPU], 38, 165, inner-28, 23)
	place(app.controls[idEstimate], 38, 194, inner-28, 32)

	for i, id := range []int{idExe, idOutput} {
		y := 254 + i*57
		place(app.labels[strconv.Itoa(id)], 24, y, inner, 20)
		place(app.controls[id], 24, y+21, inner-95, 29)
		browse := idExeBrowse
		if id == idOutput {
			browse = idOutputBrowse
		}
		place(app.controls[browse], w-111, y+21, 87, 29)
	}
	// Keep the model selector compact and right aligned; give microphones more room.
	modelW := min(310, inner*36/100)
	modelX := w - 24 - modelW
	micW := modelX - 24 - 24
	place(app.labels["mic"], 24, 378, micW, 20)
	place(app.labels["model"], modelX, 378, modelW, 20)
	place(app.controls[idMic], 24, 399, micW-85, 270)
	place(app.controls[idMicRefresh], 24+micW-77, 399, 77, 29)
	place(app.controls[idModel], modelX, 399, modelW, 260)
	place(app.controls[idModelInfo], modelX, 433, modelW, 42)

	place(app.labels["binds"], 24, 485, inner, 20)
	ids := []int{idBindTalk, idBindPublicAuto, idBindTeamAuto, idBindPublicSend, idBindTeamSend}
	for i, c := range quickBindColumns(24, inner, 12) {
		place(app.labels["bind-"+strconv.Itoa(i)], c.X, 513, c.Width, 17)
		place(app.controls[ids[i]], c.X, 534, c.Width, 32)
	}
	place(app.controls[idConfig], 24, 590, 164, 36)
	place(app.controls[idLaunch], 200, 590, min(260, inner-426), 36)
	place(app.controls[idStart], w-277, 590, 145, 36)
	place(app.controls[idStop], w-120, 590, 96, 36)
	place(app.controls[idRun], 24, 638, inner, 32)
	place(app.labels["activity"], 24, 684, inner-95, 22)
	place(app.controls[idClear], w-104, 679, 80, 28)
	place(app.controls[idLog], 24, 714, inner, max(24, h-738))
	invalidate(app.window)
}
func setRun(text string, isError bool) {
	app.runError = isError
	setText(app.controls[idRun], text)
	invalidate(app.controls[idRun])
}
func appendLog(text string) {
	if text == "" {
		return
	}
	h := app.controls[idLog]
	old := getText(h)
	if len(old) > 160000 {
		send(h, 0xB1, 0, uintptr(len(old)-120000))
		p := up("")
		send(h, 0xC2, 0, uintptr(unsafe.Pointer(p)))
	}
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	text = strings.ReplaceAll(text, "\n", "\r\n")
	if !strings.HasSuffix(text, "\r\n") {
		text += "\r\n"
	}
	send(h, 0xB1, ^uintptr(0), ^uintptr(0))
	ink := color(227, 235, 247)
	upper := strings.ToUpper(text)
	if strings.Contains(upper, "ERROR:") || strings.Contains(upper, "MISSING:") {
		ink = color(255, 137, 137)
	}
	cf := CharFormat{Mask: 0x40000000, Color: uint32(ink)}
	cf.Size = uint32(unsafe.Sizeof(cf))
	send(h, 0x444, 1, uintptr(unsafe.Pointer(&cf)))
	p := up(text)
	send(h, 0xC2, 0, uintptr(unsafe.Pointer(p)))
	runtime.KeepAlive(p)
	send(h, 0xB7, 0, 0)
}
func isRunning(kind string) bool { return app.jobs[kind] != nil }
func coreReady() bool            { return app.deps["python"] == "ready" && app.deps["core"] == "ready" }
func fieldsUnlocked() bool {
	return !app.closing && !isRunning("backend") && !isRunning("install") && !isRunning("bindings")
}
func bindLabel(configKey string) string {
	value := strings.TrimSpace(app.config.Values[configKey])
	if value == "" {
		return "(unset)"
	}
	return value
}
func refreshBindButtons() {
	setText(app.controls[idBindTalk], bindLabel("STT_LISTEN"))
	setText(app.controls[idBindPublicAuto], bindLabel("STT_KEY_AUTO"))
	setText(app.controls[idBindTeamAuto], bindLabel("STT_TEAMKEY_AUTO"))
	setText(app.controls[idBindPublicSend], bindLabel("STT_KEY"))
	setText(app.controls[idBindTeamSend], bindLabel("STT_TEAMKEY"))
	if app.capturingBind != 0 {
		setText(app.controls[app.capturingBind], "Press key…")
	}
}
func captureBindLabel(id int) string {
	switch id {
	case idBindTalk:
		return "push-to-talk"
	case idBindPublicAuto:
		return "select public chat"
	case idBindTeamAuto:
		return "select team chat"
	case idBindPublicSend:
		return "send public chat"
	case idBindTeamSend:
		return "send team chat"
	}
	return "binding"
}
func bindConfigKey(id int) string {
	switch id {
	case idBindTalk:
		return "STT_LISTEN"
	case idBindPublicAuto:
		return "STT_KEY_AUTO"
	case idBindTeamAuto:
		return "STT_TEAMKEY_AUTO"
	case idBindPublicSend:
		return "STT_KEY"
	case idBindTeamSend:
		return "STT_TEAMKEY"
	}
	return ""
}
func checkAutoexec() {
	folder := resolve(app.root, getText(app.controls[idOutput]))
	name := app.config.Values["STT_EXEC_FILE"]
	ok, text := autoexecStatus(folder, name)
	app.autoError = !ok
	if getText(app.controls[idAutoexec]) != text {
		setText(app.controls[idAutoexec], text)
	}
	invalidate(app.controls[idAutoexec])
}
func estimates() string {
	text := "Fresh downloads   recognition packages ~150–250 MB"
	if app.deps["python"] != "ready" {
		text += "; Python ~11 MB if needed"
	}
	if send(app.controls[idGPU], 0xF0, 0, 0) == 1 {
		text += "; NVIDIA adds ~1.1–1.5 GB"
	}
	text += ".\r\n"
	if selectedModel() == "auto" && !app.depsChecked && app.actualModel == "" {
		text += "Voice model on first Start: checking available acceleration…"
	} else {
		text += modelEstimate(modelForDisplay(), app.config.Values["STT_CPU_MODEL"], app.config.Values["STT_WHISPER_DEVICE"], app.deps["gpu"] == "ready")
	}
	return text
}
func refreshButtons() {
	if app.controls[idStart] == 0 {
		return
	}
	exe := resolve(app.root, getText(app.controls[idExe]))
	validExe := fileExists(exe) && strings.EqualFold(filepath.Ext(exe), ".exe")
	validOutput := dirExists(resolve(app.root, getText(app.controls[idOutput])))
	for _, id := range []int{idExe, idExeBrowse, idOutput, idOutputBrowse, idMic, idMicRefresh, idModel, idConfig, idGPU, idBindTalk, idBindPublicAuto, idBindTeamAuto, idBindPublicSend, idBindTeamSend} {
		enabled(app.controls[id], fieldsUnlocked())
	}
	enabled(app.controls[idStart], app.ready && coreReady() && validExe && validOutput && fieldsUnlocked() && !isRunning("status"))
	enabled(app.controls[idStop], isRunning("backend") && !app.stopping && !app.closing)
	enabled(app.controls[idInstall], app.ready && fieldsUnlocked() && !isRunning("status"))
	enabled(app.controls[idLaunch], validExe && !app.closing)
	setText(app.controls[idLaunch], "Launch "+gameName(exe))
	if isRunning("install") {
		setText(app.controls[idInstall], "Installing dependencies…")
	} else {
		setText(app.controls[idInstall], "Install / repair dependencies")
	}
	refreshModelDisplay()
	setText(app.controls[idEstimate], estimates())
	for _, id := range []int{idInstall, idStart, idStop, idGPU} {
		invalidate(app.controls[id])
	}
	refreshBindButtons()
	if app.initialized {
		checkAutoexec()
	}
	invalidate(app.window)
}
func selectedMic() string {
	index := int(send(app.controls[idMic], 0x147, 0, 0))
	if index >= 0 && index < len(app.micOptions) {
		return app.micOptions[index].Value
	}
	return app.config.Values["STT_AUDIO_INPUT_INDEX"]
}
func fillMics(options []MicOption, wanted string) {
	h := app.controls[idMic]
	send(h, 0x14B, 0, 0)
	name := "Default microphone (auto)"
	if app.defaultMic != "" {
		name = "Default: " + app.defaultMic
	}
	all := []MicOption{{"auto", name}}
	seen := map[string]bool{"auto": true}
	for _, item := range options {
		if !seen[item.Value] {
			all = append(all, item)
			seen[item.Value] = true
		}
	}
	if wanted != "" && !seen[wanted] {
		name := wanted
		if strings.HasPrefix(name, "name:") {
			name = strings.TrimPrefix(name, "name:") + " (saved selection)"
		} else {
			name = "Saved device index " + name
		}
		all = append(all, MicOption{wanted, name})
	}
	index := 0
	for i, o := range all {
		addString(h, o.Name)
		if o.Value == wanted {
			index = i
		}
	}
	app.micOptions = all
	send(h, 0x14E, uintptr(index), 0)
	send(h, 0x160, uintptr(sc(620)), 0)
}
func nativeMics() {
	wanted := selectedMic()
	options, defaultName, err := nativeMicrophones()
	app.defaultMic = defaultName
	if err != nil {
		appendLog("Windows microphone enumeration could not complete; installed PyAudio will be tried.")
	}
	fillMics(options, wanted)
}
func refreshForm(detect bool) {
	exe := resolve(app.root, app.config.Values["STT_KINGPIN_EXE"])
	if detect && !fileExists(exe) {
		exe = ""
		for _, p := range []string{`C:\games\kingpin\kingpin.exe`, `C:\Program Files (x86)\Steam\steamapps\common\Kingpin\kingpin.exe`} {
			if fileExists(p) {
				exe = p
				break
			}
		}
		app.config.Values["STT_KINGPIN_EXE"] = exe
	}
	if exe != "" && strings.TrimSpace(app.config.Values["STT_OUTPUT_PATH"]) == "" {
		app.config.Values["STT_OUTPUT_PATH"] = defaultOutput(exe)
	}
	app.lastExe = exe
	for id, key := range map[int]string{idExe: "STT_KINGPIN_EXE", idOutput: "STT_OUTPUT_PATH"} {
		setText(app.controls[id], app.config.Values[key])
	}
	app.actualModel = ""
	setSelectedModel(app.config.Values["STT_WHISPER_MODEL"])
	fillMics(app.micOptions, app.config.Values["STT_AUDIO_INPUT_INDEX"])
	refreshButtons()
}
func gameChanged() {
	exe := resolve(app.root, getText(app.controls[idExe]))
	if fileExists(exe) && strings.EqualFold(filepath.Ext(exe), ".exe") && !strings.EqualFold(exe, app.lastExe) {
		setText(app.controls[idOutput], defaultOutput(exe))
		app.lastExe = exe
	}
	refreshButtons()
}
func saveMain(validate bool) bool {
	exe := resolve(app.root, getText(app.controls[idExe]))
	folder := resolve(app.root, getText(app.controls[idOutput]))
	if validate && (!fileExists(exe) || !strings.EqualFold(filepath.Ext(exe), ".exe")) {
		alert("Choose an existing game executable before starting.")
		return false
	}
	if validate && !dirExists(folder) {
		alert("Choose an existing game / mod output folder before starting.")
		return false
	}
	for id, key := range map[int]string{idExe: "STT_KINGPIN_EXE", idOutput: "STT_OUTPUT_PATH"} {
		app.config.Values[key] = strings.TrimSpace(getText(app.controls[id]))
	}
	app.config.Values["STT_WHISPER_MODEL"] = selectedModel()
	app.config.Values["STT_AUDIO_INPUT_INDEX"] = selectedMic()
	if fileExists(exe) {
		addGameAllowList(&app.config, exe)
	}
	if err := app.config.Save(); err != nil {
		alert("Could not save configuration: " + err.Error())
		return false
	}
	return true
}
func extractPayload() error {
	return fs.WalkDir(payload, "payload", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		relative := strings.TrimPrefix(path, "payload/")
		preserve := strings.HasPrefix(relative, "defaults/")
		target := filepath.Join(app.root, ".joyvox", filepath.FromSlash(relative))
		if preserve {
			target = filepath.Join(app.root, filepath.Base(relative))
		}
		if preserve && fileExists(target) {
			return nil
		}
		data, err := payload.ReadFile(path)
		if err != nil {
			return err
		}
		existing, _ := os.ReadFile(target)
		if bytes.Equal(existing, data) {
			return nil
		}
		return atomicWrite(target, data)
	})
}

type Job struct {
	kind    string
	cmd     *exec.Cmd
	mu      sync.Mutex
	group   uintptr
	output  bytes.Buffer
	partial string
}
type Event struct {
	kind, text string
	job        *Job
	err        error
	done       bool
}

var events = make(chan Event, 512)

func emit(e Event) { events <- e; post(app.window, wmEvent, 0, 0) }
func (j *Job) Write(data []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	n := len(data)
	if j.output.Len() < 1024*1024 {
		j.output.Write(data[:min(n, 1024*1024-j.output.Len())])
	}
	if j.kind == "devices" {
		return n, nil
	}
	j.partial += string(data)
	for {
		index := strings.IndexAny(j.partial, "\n\r")
		if index < 0 {
			break
		}
		line := j.partial[:index]
		j.partial = j.partial[index+1:]
		if strings.TrimSpace(line) != "" {
			emit(Event{kind: j.kind, text: line})
		}
	}
	if len(j.partial) > 16000 {
		emit(Event{kind: j.kind, text: j.partial})
		j.partial = ""
	}
	return n, nil
}
func attachJob(cmd *exec.Cmd) uintptr {
	group, _, _ := kernel32.NewProc("CreateJobObjectW").Call(0, 0)
	if group == 0 {
		return 0
	}
	type Basic struct {
		UserTime, JobTime  int64
		Flags              uint32
		Min, Max           uintptr
		Active             uint32
		Affinity           uintptr
		Priority, Schedule uint32
	}
	limits := struct {
		Basic                              Basic
		IO                                 [6]uint64
		ProcMem, JobMem, PeakProc, PeakJob uintptr
	}{}
	limits.Basic.Flags = 0x2000
	ok, _, _ := kernel32.NewProc("SetInformationJobObject").Call(group, 9, uintptr(unsafe.Pointer(&limits)), unsafe.Sizeof(limits))
	if ok == 0 {
		syscall.CloseHandle(syscall.Handle(group))
		return 0
	}
	process, _, _ := kernel32.NewProc("OpenProcess").Call(0x101, 0, uintptr(cmd.Process.Pid))
	if process == 0 {
		syscall.CloseHandle(syscall.Handle(group))
		return 0
	}
	ok, _, _ = kernel32.NewProc("AssignProcessToJobObject").Call(group, process)
	syscall.CloseHandle(syscall.Handle(process))
	if ok == 0 {
		syscall.CloseHandle(syscall.Handle(group))
		return 0
	}
	return group
}
func (j *Job) terminate() {
	j.mu.Lock()
	group, cmd := j.group, j.cmd
	j.mu.Unlock()
	if group != 0 {
		kernel32.NewProc("TerminateJobObject").Call(group, 130)
		return
	}
	if cmd != nil && cmd.Process != nil {
		killer := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "taskkill.exe"), "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
		killer.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
		_ = killer.Run()
		_ = cmd.Process.Kill()
	}
}
func startJob(kind, executable string, args ...string) {
	if isRunning(kind) || app.closing {
		return
	}
	job := &Job{kind: kind}
	app.jobs[kind] = job
	go func() {
		cmd := exec.Command(executable, args...)
		cmd.Dir = app.root
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
		cmd.Stdout, cmd.Stderr = job, job
		cmd.Env = append(os.Environ(), "PYTHONUTF8=1", "PYTHONIOENCODING=utf-8", "PYTHONNOUSERSITE=1", "PYGAME_HIDE_SUPPORT_PROMPT=1")
		job.mu.Lock()
		job.cmd = cmd
		err := cmd.Start()
		if err == nil {
			job.group = attachJob(cmd)
		}
		job.mu.Unlock()
		if err == nil {
			err = cmd.Wait()
		}
		job.mu.Lock()
		if job.partial != "" {
			emit(Event{kind: kind, text: job.partial})
		}
		if job.group != 0 {
			syscall.CloseHandle(syscall.Handle(job.group))
			job.group = 0
		}
		job.mu.Unlock()
		emit(Event{kind: kind, job: job, err: err, done: true})
	}()
	refreshButtons()
}
func pythonPath() string {
	if p := app.deps["python_exe"]; fileExists(p) {
		return p
	}
	for _, r := range []string{`.joyvox\python\python.exe`, `.joyvox\venv\Scripts\python.exe`, `.venv\Scripts\python.exe`} {
		p := filepath.Join(app.root, r)
		if fileExists(p) {
			return p
		}
	}
	return ""
}
func startStatus(install bool) {
	if app.closing || isRunning("status") || isRunning("install") || isRunning("backend") {
		return
	}
	action, kind := "Status", "status"
	if install {
		if !saveMain(false) {
			return
		}
		warning := estimates() + "\r\n\r\nThese are approximate fresh-download sizes; installed components and cached models may reduce them. Downloads require an Internet connection. Recognition stays local.\r\n\r\nCompatible installed Python will be reused through an isolated JoyVox environment. Your system Python packages will not be changed.\r\n\r\nThe configured exec line will also be added to autoexec.cfg; existing content is preserved and backed up. Continue?"
		if messageBox(app.window, warning, "Install / repair dependencies", 0x24) != 6 {
			return
		}
		folder := resolve(app.root, app.config.Values["STT_OUTPUT_PATH"])
		changed, err := ensureAutoexec(folder, app.config.Values["STT_EXEC_FILE"])
		if err != nil {
			appendLog("ERROR: autoexec.cfg: " + err.Error() + ". Dependency setup will continue.")
		} else if changed {
			appendLog("Added exec " + app.config.Values["STT_EXEC_FILE"] + " to autoexec.cfg. Existing content was preserved.")
		} else {
			appendLog("autoexec.cfg already has the configured exec line; no duplicate added.")
		}
		action, kind = "Install", "install"
		setRun("Installing dependencies… progress appears below.", false)
	} else {
		setText(app.controls[idDeps], "Checking installed Python and dependencies…")
		app.depError = false
		app.depsChecked = false
		app.actualModel = ""
	}
	ps := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	args := []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", filepath.Join(app.root, ".joyvox", "tools", "runtime.ps1"), "-Action", action, "-Root", app.root}
	if install && send(app.controls[idGPU], 0xF0, 0, 0) == 1 {
		args = append(args, "-WithGpu")
	}
	startJob(kind, ps, args...)
}
func queryMics() {
	if isRunning("devices") || isRunning("backend") || app.closing {
		return
	}
	python := pythonPath()
	if python == "" {
		return
	}
	startJob("devices", python, "-X", "utf8", "-u", filepath.Join(app.root, ".joyvox", "app", "joyvox.py"), "--config", filepath.Join(app.root, "joyvox_config.cfg"), "--list-devices-json")
}
func readStatus() {
	app.deps = loadConfig(filepath.Join(app.root, ".joyvox", "dependency-status.ini"), false).Values
	app.depsChecked = true
	app.actualModel = ""
	app.depError = !coreReady()
	switch {
	case coreReady():
		text := "Dependencies ready · CPU available"
		if app.deps["gpu"] == "ready" {
			text = "Dependencies ready · NVIDIA ready"
		}
		setText(app.controls[idDeps], text)
	case app.deps["python"] == "ready":
		setText(app.controls[idDeps], "Python found · recognition packages need setup")
	default:
		setText(app.controls[idDeps], "Setup needed · compatible Python not found")
	}
	if app.deps["summary"] != "" {
		appendLog(app.deps["summary"])
	}
	if !isRunning("backend") {
		if !coreReady() {
			setRun("Install / repair dependencies to enable speech recognition.", true)
		} else if !fileExists(resolve(app.root, getText(app.controls[idExe]))) {
			setRun("Choose an existing game executable.", true)
		} else if !dirExists(resolve(app.root, getText(app.controls[idOutput]))) {
			setRun("Choose an existing game / mod output folder.", true)
		} else {
			setRun("Stopped · ready to start JoyVox", false)
		}
	}
	refreshButtons()
}
func startBackend() {
	if !coreReady() || isRunning("backend") || !saveMain(true) {
		return
	}
	python := pythonPath()
	if python == "" {
		setRun("ERROR: compatible Python is unavailable. Use Install / repair.", true)
		return
	}
	stop := filepath.Join(app.root, ".joyvox", "stop.signal")
	_ = os.Remove(stop)
	app.stopping = false
	if app.autoError {
		appendLog("ERROR: autoexec.cfg needs attention; starting JoyVox is still allowed. Run exec " + app.config.Values["STT_EXEC_FILE"] + " manually in the game until repaired.")
	}
	appendLog("Starting speech recognition. " + modelEstimate(app.config.Values["STT_WHISPER_MODEL"], app.config.Values["STT_CPU_MODEL"], app.config.Values["STT_WHISPER_DEVICE"], app.deps["gpu"] == "ready"))
	setRun("Loading speech recognition… wait for READY before speaking.", false)
	startJob("backend", python, "-X", "utf8", "-u", filepath.Join(app.root, ".joyvox", "app", "joyvox.py"), "--config", filepath.Join(app.root, "joyvox_config.cfg"), "--stop-file", stop)
}
func stopBackend() {
	if !isRunning("backend") || app.stopping {
		return
	}
	app.stopping = true
	app.stopDeadline = time.Now().Add(5 * time.Second)
	_ = atomicWrite(filepath.Join(app.root, ".joyvox", "stop.signal"), []byte("stop\n"))
	setRun("Stopping JoyVox…", false)
	refreshButtons()
}
func launchGame() {
	exe := resolve(app.root, getText(app.controls[idExe]))
	if !fileExists(exe) {
		alert("Choose an existing game executable.")
		return
	}
	if !isRunning("backend") && !saveMain(false) {
		return
	}
	if app.autoError {
		appendLog("ERROR: autoexec.cfg entry is missing or unreadable; launching the game is still allowed.")
	}
	verb, path, dir := up("open"), up(exe), up(filepath.Dir(exe))
	r, _, _ := shell32.NewProc("ShellExecuteW").Call(app.window, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(path)), 0, uintptr(unsafe.Pointer(dir)), 1)
	runtime.KeepAlive(verb)
	runtime.KeepAlive(path)
	runtime.KeepAlive(dir)
	if r <= 32 {
		setRun("ERROR: Windows could not launch the selected game.", true)
	}
}
func processEvents() {
	for {
		select {
		case e := <-events:
			if !e.done {
				appendLog(e.text)
				if e.kind == "backend" && strings.Contains(e.text, "] READY") && !app.stopping {
					if model := readyModel(e.text); model != "" {
						app.actualModel = model
						refreshModelDisplay()
						setText(app.controls[idEstimate], estimates())
					}
					setRun("Ready · hold "+app.config.Values["STT_LISTEN"]+" to talk. Keep JoyVox open while playing.", false)
				}
				if e.kind == "backend" && strings.Contains(e.text, "ERROR:") {
					setRun(e.text, true)
				}
				continue
			}
			delete(app.jobs, e.kind)
			switch e.kind {
			case "status":
				if e.err == nil {
					readStatus()
					if !app.closing {
						queryMics()
					}
				} else {
					app.deps = map[string]string{}
					app.depsChecked = true
					app.depError = true
					setText(app.controls[idDeps], "Dependency check failed · see activity")
					setRun("ERROR: dependency check failed. Review the activity log or repair dependencies.", true)
				}
				if app.pendingBindings && !app.closing {
					app.pendingBindings = false
					if python := pythonPath(); python != "" && dirExists(resolve(app.root, app.config.Values["STT_OUTPUT_PATH"])) {
						startJob("bindings", python, "-X", "utf8", "-u", filepath.Join(app.root, ".joyvox", "app", "joyvox.py"), "--config", filepath.Join(app.root, "joyvox_config.cfg"), "--initialize-game")
					}
				}
			case "install":
				if e.err == nil {
					appendLog("Dependency setup finished. Verifying installed components…")
					app.pendingBindings = true
				} else {
					appendLog("ERROR: dependency installation failed: " + e.err.Error() + ". Details are above.")
				}
				if !app.closing {
					startStatus(false)
				}
			case "bindings":
				if e.err != nil {
					appendLog("ERROR: could not initialize game bindings. Correct the configuration and start JoyVox to try again.")
				}
			case "devices":
				var devices []struct {
					Index int    `json:"index"`
					Name  string `json:"name"`
					Host  string `json:"host_api"`
				}
				if e.err == nil && json.Unmarshal(e.job.output.Bytes(), &devices) == nil {
					wanted := selectedMic()
					options := make([]MicOption, 0, len(devices))
					for _, d := range devices {
						name := d.Name
						if d.Host != "" {
							name += " · " + d.Host
						}
						name += fmt.Sprintf(" [%d]", d.Index)
						options = append(options, MicOption{strconv.Itoa(d.Index), name})
					}
					fillMics(options, wanted)
					appendLog(fmt.Sprintf("Microphones refreshed: %d input device entries. Windows default: %s", len(options), app.defaultMic))
				} else {
					appendLog("Microphone names from Windows remain available. PyAudio device verification will work after dependency setup.")
				}
			case "backend":
				app.actualModel = ""
				requested := app.stopping
				app.stopping = false
				if e.err != nil && !requested {
					setRun("ERROR: JoyVox stopped. Review the activity below.", true)
					appendLog("ERROR: recognizer stopped: " + e.err.Error())
				} else {
					setRun("Stopped · ready to start again", false)
					appendLog("JoyVox stopped.")
				}
			}
			refreshButtons()
			if app.closing && len(app.jobs) == 0 {
				user32.NewProc("DestroyWindow").Call(app.window)
			}
		default:
			return
		}
	}
}
func createMainControls() {
	add(idLogo, "STATIC", "", 3, 0)
	if app.titleIcon != 0 {
		send(app.controls[idLogo], 0x0172, 1, app.titleIcon)
	}
	add(idByline, "STATIC", "by Joyeuse", 0, 0)
	label("title", "JoyVox  "+version)
	setFont(app.labels["title"], app.titleFont)
	label("tagline", "Speech to in-game chat for Quake 2 engine games.")
	label("binds", "Quick binds")
	for i, text := range []string{"Hold to talk", "Autochat target: public", "Autochat target: team", "Send message: public", "Send message: team"} {
		h := control(app.window, "STATIC", text, 1, 0, 0) // centered, borderless
		app.labels["bind-"+strconv.Itoa(i)] = h
		setFont(h, app.smallFont)
	}
	add(idDeps, "STATIC", "Checking installed Python and dependencies…", 0, 0)
	add(idInstall, "BUTTON", "Install / repair dependencies", tabstop|0x0000000B, 0)
	add(idGPU, "BUTTON", "Install NVIDIA acceleration libraries (optional; CPU recognition also works)", tabstop|3, 0)
	if fileExists(filepath.Join(os.Getenv("SystemRoot"), "System32", "nvcuda.dll")) {
		send(app.controls[idGPU], 0xF1, 1, 0)
	}
	add(idEstimate, "STATIC", "", 0, 0)
	setFont(app.controls[idEstimate], app.smallFont)
	for _, field := range []struct {
		ID, Browse int
		Title      string
	}{
		{idExe, idExeBrowse, "Game executable"},
		{idOutput, idOutputBrowse, "Game / mod output folder"},
	} {
		label(strconv.Itoa(field.ID), field.Title)
		add(field.ID, "EDIT", "", tabstop|0x80, 0x200)
		send(app.controls[field.ID], 0xC5, 32767, 0)
		add(field.Browse, "BUTTON", "Browse…", tabstop, 0)
	}
	cue := up("Enter the path to your Game executable")
	send(app.controls[idExe], 0x1501, 1, uintptr(unsafe.Pointer(cue)))
	runtime.KeepAlive(cue)
	label("mic", "Microphone")
	label("model", "Voice recognition model")
	add(idMic, "COMBOBOX", "", tabstop|3|0x200000, 0)
	add(idMicRefresh, "BUTTON", "Refresh", tabstop, 0)
	add(idModel, "COMBOBOX", "", tabstop|2|0x200000, 0)
	for _, model := range []string{"auto", "large-v3-turbo", "small.en", "base.en", "medium.en", "tiny.en", "large-v3"} {
		addString(app.controls[idModel], model)
	}
	send(app.controls[idModel], 0x160, uintptr(sc(620)), 0)
	add(idModelInfo, "STATIC", "", 0, 0)
	setFont(app.controls[idModelInfo], app.smallItalicFont)
	setFont(app.controls[idByline], app.smallItalicFont)
	add(idBindTalk, "BUTTON", "", tabstop, 0)
	add(idBindPublicAuto, "BUTTON", "", tabstop, 0)
	add(idBindTeamAuto, "BUTTON", "", tabstop, 0)
	add(idBindPublicSend, "BUTTON", "", tabstop, 0)
	add(idBindTeamSend, "BUTTON", "", tabstop, 0)
	add(idAutoexec, "STATIC", "", 0, 0)
	setFont(app.controls[idAutoexec], app.smallFont)
	add(idConfig, "BUTTON", "Advanced config", tabstop, 0)
	add(idLaunch, "BUTTON", "Launch game", tabstop, 0)
	add(idStart, "BUTTON", "Start JoyVox", tabstop|0x0000000B|1, 0)
	add(idStop, "BUTTON", "Stop", tabstop|0x0000000B, 0)
	add(idRun, "STATIC", "Preparing launcher…", 0, 0)
	label("activity", "Activity")
	add(idClear, "BUTTON", "Clear", tabstop, 0)
	add(idLog, "RICHEDIT50W", "", tabstop|0x200000|4|0x40|0x800, 0x200)
	setFont(app.controls[idLog], app.logFont)
	send(app.controls[idLog], 0x443, 0, color(19, 28, 39))
	send(app.controls[idLog], 0x435, 0, 250000)
	app.config = parseConfig(nil, filepath.Join(app.root, "joyvox_config.cfg"), true)
	fillMics(nil, "auto")
	setSelectedModel("auto")
	refreshButtons()
	layout()
}
func initialize() {
	if err := extractPayload(); err != nil {
		app.depError = true
		setText(app.controls[idDeps], "Setup files could not be prepared")
		setRun("ERROR: use a writable folder. "+err.Error(), true)
		appendLog("ERROR: " + err.Error())
		return
	}
	app.config = loadConfig(filepath.Join(app.root, "joyvox_config.cfg"), true)
	if migrateContext(&app.config) {
		backup := app.config.Path + ".before-4.2.1.bak"
		if !fileExists(backup) {
			if data, err := os.ReadFile(app.config.Path); err == nil {
				_ = atomicWrite(backup, data)
			}
		}
		if err := app.config.Save(); err != nil {
			appendLog("ERROR: updated recognition context could not be saved: " + err.Error())
		} else {
			appendLog("Updated the old default recognition context; custom prompts and all other settings were preserved.")
		}
	}
	app.ready = true
	app.initialized = true
	refreshForm(true)
	nativeMics()
	appendLog("JoyVox " + version + " · speech recognition runs locally. Select the game and output folder, then install dependencies if needed.")
	startStatus(false)
}
func browseMain(id int) {
	target := 0
	folder := false
	switch id {
	case idExeBrowse:
		target = idExe
	case idOutputBrowse:
		target = idOutput
		folder = true
	}
	if target == 0 {
		return
	}
	initial := resolve(app.root, getText(app.controls[target]))
	chosen := ""
	if folder {
		chosen = browseFolder(app.window, initial)
	} else {
		chosen = browseFile(app.window, initial, target == idExe)
	}
	if chosen != "" {
		setText(app.controls[target], chosen)
		if target == idExe {
			gameChanged()
		}
		refreshButtons()
	}
}

type DrawItemStruct struct {
	CtlType, CtlID, ItemID, ItemAction, ItemState uint32
	HwndItem, HDC                                 uintptr
	RcItem                                        Rect
	ItemData                                      uintptr
}

func modifiersPrefix() string {
	parts := []string{}
	for _, item := range []struct {
		VK   uintptr
		Name string
	}{{0x11, "ctrl"}, {0x10, "shift"}, {0x12, "alt"}} {
		state, _, _ := user32.NewProc("GetKeyState").Call(item.VK)
		if state&0x8000 != 0 {
			parts = append(parts, item.Name)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "+") + "+"
}
func keyToken(vk uintptr) string {
	v := int(vk)
	switch {
	case v >= 0x41 && v <= 0x5A:
		return modifiersPrefix() + strings.ToLower(string(rune(v)))
	case v >= 0x30 && v <= 0x39:
		return modifiersPrefix() + string(rune(v))
	case v >= 0x70 && v <= 0x87:
		return modifiersPrefix() + fmt.Sprintf("f%d", v-0x6F)
	}
	mapping := map[int]string{0x09: "tab", 0x0D: "enter", 0x20: "space", 0x25: "left", 0x26: "up", 0x27: "right", 0x28: "down", 0x21: "pgup", 0x22: "pgdn", 0x24: "home", 0x23: "end", 0x2D: "ins", 0x2E: "del", 0x08: "backspace"}
	if name, ok := mapping[v]; ok {
		return modifiersPrefix() + name
	}
	return ""
}
func mouseToken(msg uint32, wparam uintptr) string {
	base := ""
	switch msg {
	case 0x201:
		base = "mouse1"
	case 0x204:
		base = "mouse2"
	case 0x207:
		base = "mouse3"
	case 0x20B:
		if (wparam>>16)&0xffff == 1 {
			base = "mouse4"
		} else {
			base = "mouse5"
		}
	}
	if base == "" {
		return ""
	}
	return modifiersPrefix() + base
}
func beginBindCapture(id int) {
	if !fieldsUnlocked() {
		return
	}
	if app.capturingBind == 0 {
		app.bindPreviousStatus = getText(app.controls[idRun])
		app.bindPreviousError = app.runError
	}
	app.capturingBind = id
	refreshBindButtons()
	setRun("Press a key or combo for "+captureBindLabel(id)+". Esc cancels.", false)
	user32.NewProc("SetFocus").Call(app.window)
}
func cancelBindCapture() {
	if app.capturingBind == 0 {
		return
	}
	app.capturingBind = 0
	refreshBindButtons()
	setRun(app.bindPreviousStatus, app.bindPreviousError)
}
func completeBindCapture(token string) {
	key := bindConfigKey(app.capturingBind)
	if key == "" || token == "" {
		return
	}
	previous := app.config.Values[key]
	app.config.Values[key] = token
	app.capturingBind = 0
	if err := app.config.Save(); err != nil {
		app.config.Values[key] = previous
		refreshBindButtons()
		appendLog("ERROR: could not save bind change: " + err.Error())
		setRun("ERROR: could not save the new binding.", true)
		return
	}
	refreshButtons()
	setRun(app.bindPreviousStatus, app.bindPreviousError)
}

// Capture before IsDialogMessage can treat Enter/Tab as dialog navigation.
func interceptBindInput(msg *WinMsg) bool {
	if app.capturingBind == 0 || app.editor != 0 {
		return false
	}
	fgWindow, _, _ := user32.NewProc("GetForegroundWindow").Call()
	if fgWindow != app.window {
		cancelBindCapture()
		return false
	}
	switch msg.Message {
	case 0x100, 0x104:
		if msg.WParam == 0x1B {
			cancelBindCapture()
			return true
		}
		if token := keyToken(msg.WParam); token != "" {
			completeBindCapture(token)
		}
		return true
	case 0x101, 0x105, 0x102, 0x106: // key up / generated character while waiting
		return true
	case 0x201, 0x204, 0x207, 0x20B:
		if token := mouseToken(msg.Message, msg.WParam); token != "" {
			completeBindCapture(token)
		}
		return true
	}
	return false
}
func requestClose() {
	if app.closing {
		return
	}
	if app.editor != 0 {
		user32.NewProc("SetForegroundWindow").Call(app.editor)
		return
	}
	if isRunning("install") && messageBox(app.window, "Dependency installation is still running. Close JoyVox and stop this installation? You can use Install / repair again afterwards.", "Stop installation?", 0x24) != 6 {
		return
	}
	if app.ready && !isRunning("backend") {
		if !saveMain(false) {
			return
		}
	}
	app.closing = true
	app.closeDeadline = time.Now().Add(5 * time.Second)
	if isRunning("backend") {
		stopBackend()
	}
	for kind, j := range app.jobs {
		if kind != "backend" {
			go j.terminate()
		}
	}
	if len(app.jobs) == 0 {
		user32.NewProc("DestroyWindow").Call(app.window)
	} else {
		setRun("Stopping child processes before closing…", false)
		refreshButtons()
	}
}
func mainProc(hwnd uintptr, msg uint32, wparam, lparam uintptr) uintptr {
	switch msg {
	case 0x1:
		app.window = hwnd
		createMainControls()
		user32.NewProc("SetTimer").Call(hwnd, 1, 250, 0)
		post(hwnd, wmInitialize, 0, 0)
		return 0
	case wmInitialize:
		initialize()
		return 0
	case wmEvent:
		processEvents()
		return 0
	case 0x0F: // WM_PAINT
		paintMainFrame(hwnd)
		return 0
	case 0x2C: // WM_MEASUREITEM
		if measureComboItem(lparam) {
			return 1
		}
		return 0
	case 0x5:
		layout()
		return 0
	case 0x24:
		m := (*MinMax)(unsafe.Pointer(lparam))
		m.MinTrack = Point{int32(sc(830)), int32(sc(800))}
		return 0
	case 0x2E0: // WM_DPICHANGED
		app.dpi = int(wparam & 0xffff)
		setFonts()
		r := (*Rect)(unsafe.Pointer(lparam))
		user32.NewProc("SetWindowPos").Call(hwnd, 0, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), 4|0x10)
		layout()
		return 0
	case 0x113:
		if app.stopping && time.Now().After(app.stopDeadline) {
			if j := app.jobs["backend"]; j != nil {
				go j.terminate()
			}
			app.stopDeadline = time.Now().Add(60 * time.Second)
		}
		if app.closing && time.Now().After(app.closeDeadline) {
			for _, j := range app.jobs {
				go j.terminate()
			}
			app.closeDeadline = time.Now().Add(60 * time.Second)
		}
		if app.initialized && time.Since(app.lastAutoCheck) > 2*time.Second {
			app.lastAutoCheck = time.Now()
			checkAutoexec()
		}
		return 0
	case 0x111:
		id, code := int(wparam&0xffff), int((wparam>>16)&0xffff)
		if code == 0x300 { // EN_CHANGE (programmatic SetWindowText also sends this)
			if app.initialized && (id == idExe || id == idOutput) {
				if id == idExe {
					gameChanged()
				} else {
					refreshButtons()
				}
			}
			return 0
		}
		if id == idModel && (code == 1 || code == 5) {
			if app.modelUpdating {
				return 0
			}
			app.actualModel = ""
			post(hwnd, 0x8003, 0, 0)
			return 0
		}
		if code != 0 {
			return 0
		}
		switch id {
		case idInstall:
			startStatus(true)
		case idGPU:
			refreshButtons()
		case idExeBrowse, idOutputBrowse:
			browseMain(id)
		case idMicRefresh:
			nativeMics()
			queryMics()
		case idConfig:
			openEditor()
		case idStart:
			startBackend()
		case idStop:
			stopBackend()
		case idLaunch:
			launchGame()
		case idClear:
			setText(app.controls[idLog], "")
		case idBindTalk, idBindPublicAuto, idBindTeamAuto, idBindPublicSend, idBindTeamSend:
			beginBindCapture(id)
		}
		return 0
	case 0x100, 0x104:
		if app.capturingBind != 0 {
			if wparam == 0x1B {
				cancelBindCapture()
				return 0
			}
			if token := keyToken(wparam); token != "" {
				completeBindCapture(token)
				return 0
			}
		}
	case 0x201, 0x204, 0x207, 0x20B:
		if app.capturingBind != 0 {
			if token := mouseToken(msg, wparam); token != "" {
				completeBindCapture(token)
				return 0
			}
		}
	case 0x8003:
		refreshButtons()
		return 0
	case 0x138:
		if lparam == app.controls[idExe] || lparam == app.controls[idOutput] {
			ink := fg
			if !isEnabled(lparam) {
				ink = muted
			}
			gdi32.NewProc("SetTextColor").Call(wparam, ink)
			gdi32.NewProc("SetBkColor").Call(wparam, cerulean)
			return app.inputBackground
		}
		ink := fg
		switch lparam {
		case app.labels["tagline"], app.controls[idEstimate], app.labels["activity"], app.controls[idModelInfo], app.controls[idByline]:
			ink = muted
		case app.controls[idDeps]:
			ink = blue
			if app.depError {
				ink = red
			} else if coreReady() {
				ink = green
			}
		case app.controls[idRun]:
			ink = blue
			if app.runError {
				ink = red
			}
		case app.controls[idAutoexec]:
			ink = green
			if app.autoError {
				ink = red
			}
		}
		gdi32.NewProc("SetBkMode").Call(wparam, 1)
		gdi32.NewProc("SetTextColor").Call(wparam, ink)
		return app.background
	case 0x135:
		gdi32.NewProc("SetBkMode").Call(wparam, 1)
		gdi32.NewProc("SetTextColor").Call(wparam, fg)
		return app.background
	case 0x133:
		gdi32.NewProc("SetBkMode").Call(wparam, 1)
		gdi32.NewProc("SetTextColor").Call(wparam, fg)
		gdi32.NewProc("SetBkColor").Call(wparam, cerulean)
		return app.inputBackground
	case 0x134:
		gdi32.NewProc("SetTextColor").Call(wparam, fg)
		gdi32.NewProc("SetBkColor").Call(wparam, cerulean)
		return app.inputBackground
	case 0x2B:
		if drawOwnerButton(lparam) || drawComboItem(lparam) {
			return 1
		}
		return 0
	case 0x10:
		requestClose()
		return 0
	case 0x2:
		user32.NewProc("PostQuitMessage").Call(0)
		return 0
	}
	r, _, _ := user32.NewProc("DefWindowProcW").Call(hwnd, uintptr(msg), wparam, lparam)
	return r
}
func registerClass(name string, proc uintptr) error {
	cursor, _, _ := user32.NewProc("LoadCursorW").Call(0, 32512)
	p := up(name)
	icon := loadAppIcon(app.instance, 32)
	if icon == 0 {
		icon = chooseStockIcon()
	}
	small := loadAppIcon(app.instance, 16)
	if small == 0 {
		small = icon
	}
	cls := WndClass{Style: 3, Proc: proc, Instance: app.instance, Icon: icon, Cursor: cursor, Background: app.background, Name: p, IconSmall: small}
	cls.Size = uint32(unsafe.Sizeof(cls))
	value, _, err := user32.NewProc("RegisterClassExW").Call(uintptr(unsafe.Pointer(&cls)))
	runtime.KeepAlive(p)
	if value == 0 {
		return err
	}
	return nil
}
func main() {
	runtime.LockOSThread()
	exe, err := os.Executable()
	if err != nil {
		messageBox(0, "Could not locate joyvox.exe: "+err.Error(), "JoyVox", 0x10)
		return
	}
	app = AppState{root: filepath.Dir(exe), controls: map[int]uintptr{}, labels: map[string]uintptr{}, deps: map[string]string{}, jobs: map[string]*Job{}, dpi: 96}
	// One launcher per folder, not one globally: separate installations are fine.
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(strings.ToLower(app.root)))
	mutexName := up(fmt.Sprintf(`Local\JoyVox-%x`, hash.Sum64()))
	mutex, _, mutexError := kernel32.NewProc("CreateMutexW").Call(0, 0, uintptr(unsafe.Pointer(mutexName)))
	runtime.KeepAlive(mutexName)
	if mutex != 0 {
		defer syscall.CloseHandle(syscall.Handle(mutex))
		if mutexError == syscall.Errno(183) {
			messageBox(0, "JoyVox is already open from this folder. Use the existing window.", "JoyVox", 0x40)
			return
		}
	}
	hr, _, _ := ole32.NewProc("CoInitializeEx").Call(0, 2)
	if hrOK(hr) {
		defer ole32.NewProc("CoUninitialize").Call()
	}
	icc := struct{ Size, Classes uint32 }{8, 0x000040FF}
	comctl32.NewProc("InitCommonControlsEx").Call(uintptr(unsafe.Pointer(&icc)))
	if _, err := syscall.LoadDLL("Msftedit.dll"); err != nil {
		messageBox(0, "Windows Rich Edit could not load: "+err.Error(), "JoyVox", 0x10)
		return
	}
	// Manifest declares PerMonitorV2; the process DPI query scales initial UI.
	if p := user32.NewProc("GetDpiForSystem"); p.Find() == nil {
		d, _, _ := p.Call()
		if d > 0 {
			app.dpi = int(d)
		}
	}
	app.instance, _, _ = kernel32.NewProc("GetModuleHandleW").Call(0)
	app.titleIcon = loadAppIcon(app.instance, sc(48))
	app.background, _, _ = gdi32.NewProc("CreateSolidBrush").Call(panel)
	app.inputBackground, _, _ = gdi32.NewProc("CreateSolidBrush").Call(cerulean)
	app.panelBackground, _, _ = gdi32.NewProc("CreateSolidBrush").Call(panel2)
	defer gdi32.NewProc("DeleteObject").Call(app.background)
	defer gdi32.NewProc("DeleteObject").Call(app.inputBackground)
	defer gdi32.NewProc("DeleteObject").Call(app.panelBackground)
	setFonts()
	if err := registerClass("JoyVoxMain421", syscall.NewCallback(mainProc)); err != nil {
		messageBox(0, err.Error(), "JoyVox", 0x10)
		return
	}
	if err := registerClass("JoyVoxConfig421", editorCallback); err != nil {
		messageBox(0, err.Error(), "JoyVox", 0x10)
		return
	}
	class, title := up("JoyVoxMain421"), up("JoyVox "+version)
	var work Rect
	user32.NewProc("SystemParametersInfoW").Call(0x30, 0, uintptr(unsafe.Pointer(&work)), 0)
	width, height := sc(940), sc(910)
	if int(work.Right-work.Left) > 0 {
		width = min(width, int(work.Right-work.Left)-sc(20))
		height = min(height, int(work.Bottom-work.Top)-sc(20))
	}
	x, y := int(work.Left)+(int(work.Right-work.Left)-width)/2, int(work.Top)+(int(work.Bottom-work.Top)-height)/2
	h, _, err := user32.NewProc("CreateWindowExW").Call(0x10000, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(title)), 0x02CF0000, uintptr(x), uintptr(y), uintptr(width), uintptr(height), 0, 0, app.instance, 0)
	runtime.KeepAlive(class)
	runtime.KeepAlive(title)
	if h == 0 {
		messageBox(0, "Could not create the JoyVox window: "+err.Error(), "JoyVox", 0x10)
		return
	}
	app.window = h
	showWindow(h, true)
	user32.NewProc("UpdateWindow").Call(h)
	var msg WinMsg
	for {
		r, _, _ := user32.NewProc("GetMessageW").Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		if interceptBindInput(&msg) {
			continue
		}
		target := app.window
		if app.editor != 0 {
			target = app.editor
		}
		handled, _, _ := user32.NewProc("IsDialogMessageW").Call(target, uintptr(unsafe.Pointer(&msg)))
		if handled != 0 {
			continue
		}
		user32.NewProc("TranslateMessage").Call(uintptr(unsafe.Pointer(&msg)))
		user32.NewProc("DispatchMessageW").Call(uintptr(unsafe.Pointer(&msg)))
	}
	for _, j := range app.jobs {
		j.terminate()
	}
	for _, f := range []uintptr{app.font, app.titleFont, app.logFont, app.smallFont, app.smallItalicFont} {
		if f != 0 {
			gdi32.NewProc("DeleteObject").Call(f)
		}
	}
}
