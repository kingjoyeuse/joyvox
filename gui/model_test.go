package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGameDefaults(t *testing.T) {
	root := t.TempDir()
	for _, test := range []struct{ exe, folder, name string }{{"kingpin.exe", "main", "kingpin"}, {"KINGPIN.EXE", "main", "KINGPIN"}, {"quake2.exe", "baseq2", "quake2"}, {"custom-engine.exe", "baseq2", "custom-engine"}} {
		t.Run(test.exe, func(t *testing.T) {
			p := filepath.Join(root, test.exe)
			if got := defaultOutput(p); got != filepath.Join(root, test.folder) {
				t.Fatalf("folder %q", got)
			}
			if got := gameName(p); got != test.name {
				t.Fatalf("name %q", got)
			}
		})
	}
	if defaultOutput("") != "" || gameName("") != "game" {
		t.Fatal("empty executable must remain unconfigured")
	}
}
func TestConfigurationPreservesUserData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "joyvox_config.cfg")
	c := parseConfig([]byte("# keep this comment\nSTT_PROMPT=My own vocabulary.\nMY_CUSTOM_KEY=keep-me\nSTT_AUTO_KEY=no // keep this explanation\n"), path, true)
	c.Values["STT_AUTO_KEY"] = "yes"
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	result := loadConfig(path, true)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if result.Values["STT_PROMPT"] != "My own vocabulary." || result.Values["MY_CUSTOM_KEY"] != "keep-me" {
		t.Fatal("user values changed")
	}
	if !bytes.Contains(data, []byte("# keep this comment")) || !bytes.Contains(data, []byte("yes // keep this explanation")) {
		t.Fatal("comments lost")
	}
	result.Values["STT_PROMPT"] = "one\ntwo"
	if err := result.Save(); err == nil {
		t.Fatal("newline injection accepted")
	}
}
func TestContextMigrationOnlyReplacesOldDefault(t *testing.T) {
	for _, prompt := range []string{"Gaming voice chat in Kingpin.", "Kingpin multiplayer voice chat.", "Keep my exact words.", ""} {
		c := parseConfig([]byte("STT_PROMPT="+prompt), "", true)
		changed := migrateContext(&c)
		shouldChange := strings.HasPrefix(prompt, "Gaming voice") || strings.HasPrefix(prompt, "Kingpin multiplayer")
		if changed != shouldChange {
			t.Fatalf("migration %q: %v", prompt, changed)
		}
		if !shouldChange && c.Values["STT_PROMPT"] != prompt {
			t.Fatal("custom/empty prompt overwritten")
		}
	}
}
func TestGameAllowList(t *testing.T) {
	c := parseConfig(nil, "", true)
	c.Values["STT_GAME_EXES"] = "kingpin.exe, MyGame.EXE"
	addGameAllowList(&c, filepath.Join(t.TempDir(), "mygame.exe"))
	if strings.Count(strings.ToLower(c.Values["STT_GAME_EXES"]), "mygame.exe") != 1 {
		t.Fatal("duplicate allowed game")
	}
	addGameAllowList(&c, filepath.Join(t.TempDir(), "quake2.exe"))
	if !strings.HasSuffix(c.Values["STT_GAME_EXES"], ",quake2.exe") {
		t.Fatal("selected engine not allowed")
	}
}
func TestExecParsing(t *testing.T) {
	for _, test := range []struct {
		text string
		want bool
	}{
		{"exec joyvox.cfg\n", true}, {"EXEC \"joyvox.cfg\" // note\r\n", true}, {"set x 1; exec joyvox.cfg", true},
		{"// exec joyvox.cfg\n", false}, {"alias hi \"exec joyvox.cfg\"", false}, {"echo \"exec joyvox.cfg\"", false},
		{"exec joyvox", false}, {"exec joyvox.cfg.old", false}, {"exec other.cfg", false}, {"exec joyvox.cfg//comment", false}, {"\ufeffexec joyvox.cfg", false}, {"exec joyvox.cfg extra", false},
	} {
		if got := hasExec([]byte(test.text), "joyvox.cfg"); got != test.want {
			t.Errorf("%q: %v", test.text, got)
		}
	}
}
func TestAutoexecAppendPreservesContentsAndIsIdempotent(t *testing.T) {
	folder := t.TempDir()
	path := filepath.Join(folder, "autoexec.cfg")
	original := []byte("// custom player name: \xe9\nset name Joy\nbind x \"use Rocket Launcher\"")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := ensureAutoexec(folder, "custom-bindings.cfg")
	if err != nil || !changed {
		t.Fatalf("append %v %v", changed, err)
	}
	actual, _ := os.ReadFile(path)
	expected := append(append([]byte{}, original...), []byte("\nexec custom-bindings.cfg\n")...)
	if !bytes.Equal(actual, expected) {
		t.Fatalf("existing bytes changed: %q", actual)
	}
	backup, _ := os.ReadFile(path + ".joyvox.bak")
	if !bytes.Equal(backup, original) {
		t.Fatal("backup does not match original")
	}
	changed, err = ensureAutoexec(folder, "custom-bindings.cfg")
	if err != nil || changed {
		t.Fatalf("second repair %v %v", changed, err)
	}
	if ok, _ := autoexecStatus(folder, "custom-bindings.cfg"); !ok {
		t.Fatal("existing command not detected")
	}
	if _, err = ensureAutoexec(folder, "second.cfg"); err != nil {
		t.Fatal(err)
	}
	backup, _ = os.ReadFile(path + ".joyvox.bak")
	if !bytes.Equal(backup, original) {
		t.Fatal("first backup overwritten")
	}
}
func TestAutoexecMissingAndInvalid(t *testing.T) {
	folder := t.TempDir()
	if ok, _ := autoexecStatus(folder, "joyvox.cfg"); ok {
		t.Fatal("missing autoexec reported ready")
	}
	if changed, err := ensureAutoexec(folder, "joyvox.cfg"); err != nil || !changed {
		t.Fatalf("new file %v %v", changed, err)
	}
	data, _ := os.ReadFile(filepath.Join(folder, "autoexec.cfg"))
	if string(data) != "exec joyvox.cfg\r\n" {
		t.Fatal("unexpected new contents")
	}
	for _, name := range []string{"../bad.cfg", "joyvox;quit.cfg", "with space.cfg", "x.txt", ""} {
		if _, err := ensureAutoexec(folder, name); err == nil {
			t.Errorf("invalid name accepted: %q", name)
		}
	}
	if _, err := ensureAutoexec(filepath.Join(folder, "absent"), "joyvox.cfg"); err == nil {
		t.Fatal("created game directory silently")
	}
	os.WriteFile(filepath.Join(folder, "autoexec.cfg"), []byte{0xff, 0xfe, 'x', 0}, 0600)
	if _, err := ensureAutoexec(folder, "joyvox.cfg"); err == nil {
		t.Fatal("UTF16 was modified")
	}
}
func TestDescriptionColumnUsesRemainingClientWidth(t *testing.T) {
	for _, dpi := range []int{96, 120, 144, 192} {
		for _, width := range []int{300, 780, 940, 1200, 1800} {
			c := fitColumns(width, dpi)
			if c[0]+c[1]+c[2] > width || c[2] < 0 || c[1] > 145*dpi/96 {
				t.Fatalf("columns overflow: dpi=%d width=%d: %v", dpi, width, c)
			}
		}
	}
}
func TestModelEstimatesAndLabels(t *testing.T) {
	if !strings.Contains(modelEstimate("auto", "small.en", "auto", true), "1.6 GB") {
		t.Fatal("GPU model estimate missing")
	}
	if !strings.Contains(modelEstimate("auto", "small.en", "cpu", true), "500 MB") {
		t.Fatal("CPU override estimate wrong")
	}
	for key, want := range map[string]string{"STT_KINGPIN_EXE": "Game executable", "STT_DICTIONARY_FILE": "Correction/custom dictionary", "STT_MODEL_CACHE": "Downloaded voice recognition model folder", "STT_AUTO_KEY": "Automatically send text"} {
		if got := settingFor(key).Label; got != want {
			t.Errorf("%s label: %q", key, got)
		}
	}
}

func TestAutoLabelTracksStatusWithoutChangingSavedValue(t *testing.T) {
	for _, tt := range []struct {
		name, cpu, device string
		gpu, checked      bool
		want              string
	}{
		{"startup", "small.en", "auto", false, false, "auto (checking…)"},
		{"cpu-only", "small.en", "auto", false, true, "auto (small.en)"},
		{"after-cuda-install", "small.en", "auto", true, true, "auto (large-v3-turbo)"},
		{"forced-cpu", "small.en", "cpu", true, true, "auto (small.en)"},
		{"custom-cpu-fallback", "base.en", "auto", false, true, "auto (base.en)"},
		{"blank-fallback", "", "auto", false, true, "auto (small.en)"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			label := autoDisplayLabel(tt.cpu, tt.device, tt.gpu, tt.checked)
			if label != tt.want {
				t.Fatalf("got %q; want %q", label, tt.want)
			}
			if canonicalModel(label) != "auto" {
				t.Fatalf("display label leaked to config: %s", label)
			}
		})
	}
	for _, custom := range []string{"large-v3-turbo", "some-account/my-model", `C:\models\my-converted-model`} {
		if canonicalModel(custom) != custom {
			t.Errorf("custom model changed: %q", custom)
		}
	}
	info := modelSummary("auto (large-v3-turbo)", "small.en", "auto", true)
	if strings.Contains(strings.ToLower(info), "auto") || strings.Contains(strings.ToLower(info), "otherwise") {
		t.Fatalf("redundant Auto rules returned: %q", info)
	}
	if readyModel("[+ 2.0s] READY — large-v3-turbo / cuda / float16") != "large-v3-turbo" ||
		readyModel("[+ 4.0s] READY — small.en / cpu / int8") != "small.en" {
		t.Fatal("actual running model was not parsed")
	}
}
func TestDependencyButtonOnlyHighlightsRequiredSetup(t *testing.T) {
	for _, tt := range []struct {
		checked, core, gpu, requested, want bool
	}{
		{false, false, false, true, false},
		{true, true, true, true, false},
		{true, true, false, false, false},
		{true, true, false, true, true},
		{true, false, false, false, true},
		{true, false, true, true, true},
	} {
		if got := dependencyRepairNeeded(tt.checked, tt.core, tt.gpu, tt.requested); got != tt.want {
			t.Fatalf("unexpected repair state for %+v: %v", tt, got)
		}
	}
}
func TestFiveBindColumnsFitMinimumAndWideWindows(t *testing.T) {
	for _, width := range []int{766, 876, 1080, 1600} {
		cols := quickBindColumns(24, width, 12)
		if len(cols) != 5 || cols[0].X != 24 || cols[4].X+cols[4].Width != 24+width {
			t.Fatalf("incorrect grid extent: %+v", cols)
		}
		for i, c := range cols {
			if c.Width < 140 || c.Width-cols[0].Width > 1 {
				t.Fatalf("unequal/too narrow: %+v", cols)
			}
			if i > 0 && c.X-cols[i-1].X-cols[i-1].Width != 12 {
				t.Fatal("wrong gap")
			}
		}
	}
}
