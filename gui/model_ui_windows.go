package main

import (
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

var modelChoices = []string{"auto", "large-v3-turbo", "small.en", "base.en", "medium.en", "tiny.en", "large-v3"}

func modelListString(h uintptr, index int) string {
	if index < 0 {
		return ""
	}
	n := int32(send(h, 0x18A, uintptr(index), 0)) // LB_GETTEXTLEN
	if n < 0 || n > 32767 {
		return ""
	}
	b := make([]uint16, int(n)+1)
	send(h, 0x189, uintptr(index), uintptr(unsafe.Pointer(&b[0]))) // LB_GETTEXT
	return syscall.UTF16ToString(b)
}

func selectedModel() string {
	h := app.controls[idModel]
	index := int(int32(send(h, 0x188, 0, 0))) // LB_GETCURSEL
	return canonicalModel(modelListString(h, index))
}
func modelForDisplay() string {
	model := selectedModel()
	if model == "auto" && app.actualModel != "" {
		return app.actualModel
	}
	return model
}
func currentAutoLabel() string {
	if app.actualModel != "" {
		return "auto (" + app.actualModel + ")"
	}
	return autoDisplayLabel(app.config.Values["STT_CPU_MODEL"], app.config.Values["STT_WHISPER_DEVICE"], app.deps["gpu"] == "ready", app.depsChecked)
}

func setSelectedModel(value string) {
	h := app.controls[idModel]
	value = canonicalModel(value)
	app.modelUpdating = true
	defer func() { app.modelUpdating = false }()
	send(h, 0x184, 0, 0) // LB_RESETCONTENT
	selected := -1
	label := currentAutoLabel()
	for i, choice := range modelChoices {
		display := choice
		if i == 0 {
			display = label
		}
		p := up(display)
		send(h, 0x180, 0, uintptr(unsafe.Pointer(p))) // LB_ADDSTRING
		runtime.KeepAlive(p)
		if value == choice {
			selected = i
		}
	}
	if selected < 0 {
		// Existing custom model names and paths remain selectable after switching
		// away; custom values can still be edited in Advanced settings.
		p := up(value)
		selected = int(int32(send(h, 0x180, 0, uintptr(unsafe.Pointer(p)))))
		runtime.KeepAlive(p)
	}
	app.autoModelLabel = label
	if selected >= 0 {
		send(h, 0x186, uintptr(selected), 0) // LB_SETCURSEL
	}
}
func refreshModelDisplay() {
	h := app.controls[idModel]
	if h == 0 || app.modelUpdating {
		return
	}
	label := currentAutoLabel()
	if app.autoModelLabel == label {
		return
	}
	app.modelUpdating = true
	defer func() { app.modelUpdating = false }()
	selected := int32(send(h, 0x188, 0, 0)) // LB_GETCURSEL
	top := int32(send(h, 0x18E, 0, 0))      // LB_GETTOPINDEX
	send(h, 0x182, 0, 0)                    // LB_DELETESTRING
	p := up(label)
	send(h, 0x181, 0, uintptr(unsafe.Pointer(p))) // LB_INSERTSTRING
	runtime.KeepAlive(p)
	if selected >= 0 {
		send(h, 0x186, uintptr(selected), 0)
	}
	if top >= 0 {
		send(h, 0x197, uintptr(top), 0) // LB_SETTOPINDEX
	}
	app.autoModelLabel = label
	invalidate(h)
}
func needsDependencyInstall() bool {
	wantGPU := send(app.controls[idGPU], 0xF0, 0, 0) == 1 || strings.EqualFold(app.config.Values["STT_WHISPER_DEVICE"], "cuda")
	return dependencyRepairNeeded(app.depsChecked, coreReady(), app.deps["gpu"] == "ready", wantGPU)
}
