package main

import (
	"runtime"
	"strings"
	"unsafe"
)

func selectedModel() string {
	return canonicalModel(getText(app.controls[idModel]))
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
	if value == "auto" {
		// refreshModelDisplay will set the resolved display label.
		send(h, 0x14E, 0, 0)
		setText(h, "auto")
		return
	}
	send(h, 0x14E, ^uintptr(0), 0)
	setText(h, value)
}
func refreshModelDisplay() {
	h := app.controls[idModel]
	if h == 0 || app.modelUpdating {
		return
	}
	previousText := getText(h)
	model := canonicalModel(previousText)
	label := currentAutoLabel()
	app.modelUpdating = true
	if app.autoModelLabel != label {
		index := int32(send(h, 0x147, 0, 0))
		send(h, 0x144, 0, 0) // CB_DELETESTRING, first row is always Auto
		p := up(label)
		send(h, 0x14A, 0, uintptr(unsafe.Pointer(p))) // CB_INSERTSTRING
		runtime.KeepAlive(p)
		app.autoModelLabel = label
		if model == "auto" && strings.TrimSpace(previousText) != "" {
			send(h, 0x14E, 0, 0)
			setText(h, label)
		} else if index > 0 {
			send(h, 0x14E, uintptr(index), 0)
		} else {
			send(h, 0x14E, ^uintptr(0), 0)
			setText(h, previousText)
		}
	} else if model == "auto" && strings.TrimSpace(previousText) != "" && previousText != label {
		send(h, 0x14E, 0, 0)
		setText(h, label)
	}
	app.modelUpdating = false
	info := ""
	if model == "auto" && !app.depsChecked && app.actualModel == "" {
		info = "Checking available acceleration…"
	} else {
		info = modelSummary(modelForDisplay(), app.config.Values["STT_CPU_MODEL"], app.config.Values["STT_WHISPER_DEVICE"], app.deps["gpu"] == "ready")
	}
	setText(app.controls[idModelInfo], info)
	invalidate(h)
}
func needsDependencyInstall() bool {
	wantGPU := send(app.controls[idGPU], 0xF0, 0, 0) == 1 || strings.EqualFold(app.config.Values["STT_WHISPER_DEVICE"], "cuda")
	return dependencyRepairNeeded(app.depsChecked, coreReady(), app.deps["gpu"] == "ready", wantGPU)
}
