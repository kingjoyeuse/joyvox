// Shared launcher state and file operations. No Windows or audio dependencies.
package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const version = "4.2.4"

//go:embed settings.json
var settingJSON string

type Setting struct{ Key, Label, Default, Help string }

var settings = func() []Setting {
	var rows [][4]string
	if err := json.Unmarshal([]byte(settingJSON), &rows); err != nil {
		panic(err)
	}
	out := make([]Setting, 0, len(rows))
	for _, r := range rows {
		out = append(out, Setting{r[0], r[1], r[2], r[3]})
	}
	return out
}()

func settingFor(key string) Setting {
	for _, s := range settings {
		if s.Key == key {
			return s
		}
	}
	return Setting{Key: key, Label: key, Help: "Additional user-defined setting; preserved when saving."}
}
func truth(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "yes", "true", "1", "on", "y":
		return true
	}
	return false
}
func isBoolean(key string) bool {
	switch key {
	case "STT_VAD_FILTER", "STT_MODEL_WARMUP", "STT_SENTENCECASE", "STT_CAPITALIZE", "STT_ENDWITHPERIOD", "STT_ALLOWEXCLAMATION", "STT_ASCII_ONLY", "STT_AUTO_KEY", "STT_CLEAR_AFTER_SEND", "STT_REQUIRE_GAME_FOCUS", "SFX_ENABLED", "SFX_MIC_ONOFF", "SFX_TRANSCRIBE_SUCCESS", "SFX_TRANSCRIBE_FAIL", "STT_SAVE_RECORDINGS":
		return true
	}
	return false
}
func isFolder(key string) bool {
	switch key {
	case "STT_OUTPUT_PATH", "STT_MODEL_CACHE", "STT_RECORDINGS_PATH", "STT_CUDA_DLL_PATH":
		return true
	}
	return false
}
func isFile(key string) bool { return key == "STT_KINGPIN_EXE" || key == "STT_DICTIONARY_FILE" }

type Config struct {
	Values map[string]string
	Lines  []string
	Path   string
}

func parseConfig(data []byte, path string, defaults bool) Config {
	c := Config{Values: make(map[string]string), Path: path}
	if defaults {
		for _, s := range settings {
			c.Values[s.Key] = s.Default
		}
	}
	text := strings.TrimPrefix(string(data), "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	c.Lines = strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	for _, line := range c.Lines {
		clean := strings.TrimSpace(line)
		if clean == "" || strings.HasPrefix(clean, "#") || strings.HasPrefix(clean, "//") {
			continue
		}
		k, v, ok := strings.Cut(clean, "=")
		if !ok {
			continue
		}
		c.Values[strings.TrimSpace(k)] = strings.TrimSpace(inlineComment.Split(v, 2)[0])
	}
	return c
}

var inlineComment = regexp.MustCompile(`\s+//`)

func loadConfig(path string, defaults bool) Config {
	data, _ := os.ReadFile(path)
	return parseConfig(data, path, defaults)
}
func (c Config) Copy() Config {
	out := Config{Values: make(map[string]string), Lines: append([]string(nil), c.Lines...), Path: c.Path}
	for k, v := range c.Values {
		out.Values[k] = v
	}
	return out
}
func (c *Config) Save() error {
	var lines []string
	written := make(map[string]bool)
	for _, line := range c.Lines {
		clean := strings.TrimSpace(line)
		k, v, ok := strings.Cut(line, "=")
		key := strings.TrimSpace(k)
		if ok && !strings.HasPrefix(clean, "#") && !strings.HasPrefix(clean, "//") {
			if value, exists := c.Values[key]; exists {
				if strings.ContainsAny(value, "\r\n\x00") {
					return fmt.Errorf("%s must be one line", key)
				}
				suffix := ""
				if i := inlineComment.FindStringIndex(v); i != nil {
					suffix = v[i[0]:]
				}
				lines = append(lines, k+"="+value+suffix)
				written[key] = true
				continue
			}
		}
		lines = append(lines, line)
	}
	keys := make([]string, 0, len(c.Values))
	for k := range c.Values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !written[k] {
			if strings.ContainsAny(c.Values[k], "\r\n\x00") {
				return fmt.Errorf("%s must be one line", k)
			}
			lines = append(lines, k+"="+c.Values[k])
		}
	}
	data := []byte(strings.Join(lines, "\r\n") + "\r\n")
	if err := atomicWrite(c.Path, data); err != nil {
		return err
	}
	*c = parseConfig(data, c.Path, true)
	return nil
}
func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".joyvox-write-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp, path)
}
func fileExists(path string) bool { st, e := os.Stat(path); return e == nil && !st.IsDir() }
func dirExists(path string) bool  { st, e := os.Stat(path); return e == nil && st.IsDir() }

var percentEnv = regexp.MustCompile(`%([^%]+)%`)

func resolve(root, value string) string {
	value = strings.Trim(strings.TrimSpace(value), `"`)
	if value == "" {
		return ""
	}
	value = percentEnv.ReplaceAllStringFunc(value, func(s string) string {
		v := os.Getenv(s[1 : len(s)-1])
		if v == "" {
			return s
		}
		return v
	})
	if !filepath.IsAbs(value) {
		value = filepath.Join(root, value)
	}
	return filepath.Clean(value)
}
func defaultOutput(exe string) string {
	if exe == "" {
		return ""
	}
	folder := "baseq2"
	if strings.EqualFold(filepath.Base(exe), "kingpin.exe") {
		folder = "main"
	}
	return filepath.Join(filepath.Dir(exe), folder)
}
func gameName(exe string) string {
	if strings.TrimSpace(exe) == "" {
		return "game"
	}
	return strings.TrimSuffix(filepath.Base(exe), filepath.Ext(exe))
}
func addGameAllowList(c *Config, exe string) {
	name := filepath.Base(exe)
	if name == "." || name == "" {
		return
	}
	values := strings.Split(c.Values["STT_GAME_EXES"], ",")
	for _, s := range values {
		if strings.EqualFold(strings.TrimSpace(s), name) {
			return
		}
	}
	c.Values["STT_GAME_EXES"] = strings.Trim(c.Values["STT_GAME_EXES"], " ,")
	if c.Values["STT_GAME_EXES"] != "" {
		c.Values["STT_GAME_EXES"] += ","
	}
	c.Values["STT_GAME_EXES"] += name
}
func migrateContext(c *Config) bool {
	switch c.Values["STT_PROMPT"] {
	case "Gaming voice chat in Kingpin.", "Kingpin multiplayer voice chat.":
		c.Values["STT_PROMPT"] = settingFor("STT_PROMPT").Default
		return true
	}
	return false
}

var cfgFilename = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]*\.cfg$`)

// Split Quake command lines without treating comments or quoted aliases as exec.
func quakeCommands(text string) [][]string {
	var commands [][]string
	var words []string
	var token strings.Builder
	quote, comment, started := false, false, false
	flushWord := func() {
		if started {
			words = append(words, token.String())
			token.Reset()
			started = false
		}
	}
	flushCommand := func() {
		flushWord()
		if len(words) > 0 {
			commands = append(commands, words)
		}
		words = nil
	}
	for i := 0; i < len(text); i++ {
		ch := text[i]
		if comment {
			if ch == '\n' {
				comment = false
				flushCommand()
			}
			continue
		}
		if !quote && !started && ch == '/' && i+1 < len(text) && text[i+1] == '/' {
			comment = true
			i++
			continue
		}
		if ch == '"' {
			quote = !quote
			started = true
			continue
		}
		if !quote && (ch == ';' || ch == '\n' || ch == '\r') {
			flushCommand()
			continue
		}
		if !quote && (ch == ' ' || ch == '\t') {
			flushWord()
			continue
		}
		started = true
		token.WriteByte(ch)
	}
	flushCommand()
	return commands
}
func hasExec(data []byte, filename string) bool {
	for _, cmd := range quakeCommands(string(data)) {
		if len(cmd) == 2 && strings.EqualFold(cmd[0], "exec") && strings.EqualFold(cmd[1], filename) {
			return true
		}
	}
	return false
}
func cfgLoads(root, current, filename string, seen map[string]bool) bool {
	path := filepath.Clean(current)
	if seen[path] {
		return false
	}
	seen[path] = true
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, cmd := range quakeCommands(string(data)) {
		if len(cmd) != 2 || !strings.EqualFold(cmd[0], "exec") {
			continue
		}
		candidate := strings.TrimSpace(strings.Trim(cmd[1], `"`))
		if strings.EqualFold(candidate, filename) {
			return true
		}
		if !cfgFilename.MatchString(candidate) {
			continue
		}
		next := filepath.Join(root, filepath.FromSlash(candidate))
		if cfgLoads(root, next, filename, seen) {
			return true
		}
	}
	return false
}
func autoexecLoads(folder, filename string) bool {
	return cfgLoads(folder, filepath.Join(folder, "autoexec.cfg"), filename, map[string]bool{})
}
func autoexecStatus(folder, filename string) (bool, string) {
	if !dirExists(folder) {
		return false, "Choose an existing game / mod output folder."
	}
	if !cfgFilename.MatchString(filename) {
		return false, "Game bindings filename must be a plain .cfg filename."
	}
	data, err := os.ReadFile(filepath.Join(folder, "autoexec.cfg"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, "Cannot read autoexec.cfg: " + err.Error()
	}
	if autoexecLoads(folder, filename) {
		if hasExec(data, filename) {
			return true, "autoexec.cfg contains exec " + filename
		}
		return true, "autoexec.cfg reaches exec " + filename + " through another cfg"
	}
	return false, "Missing JoyVox exec chain: autoexec.cfg does not reach exec " + filename + ". Use Install / repair. Launch is still allowed."
}
func ensureAutoexec(folder, filename string) (bool, error) {
	if !dirExists(folder) {
		return false, errors.New("choose an existing game / mod output folder before repairing autoexec.cfg")
	}
	if !cfgFilename.MatchString(filename) {
		return false, errors.New("game bindings filename must be a plain .cfg filename")
	}
	path := filepath.Join(folder, "autoexec.cfg")
	data, err := os.ReadFile(path)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if len(data) >= 2 && ((data[0] == 0xff && data[1] == 0xfe) || (data[0] == 0xfe && data[1] == 0xff)) {
		return false, errors.New("autoexec.cfg is UTF-16; convert it to the game's plain-text encoding before editing")
	}
	if autoexecLoads(folder, filename) {
		return false, nil
	}
	if exists {
		backup := path + ".joyvox.bak"
		if !fileExists(backup) {
			if err = atomicWrite(backup, data); err != nil {
				return false, fmt.Errorf("cannot back up autoexec.cfg: %w", err)
			}
		}
	}
	newline := "\r\n"
	if strings.Contains(string(data), "\n") && !strings.Contains(string(data), "\r\n") {
		newline = "\n"
	}
	out := append([]byte(nil), data...)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, []byte(newline)...)
	}
	out = append(out, []byte("exec "+filename+newline)...)
	if err = atomicWrite(path, out); err != nil {
		return false, err
	}
	return true, nil
}

// Display labels are deliberately separate from the value saved for Whisper.
// Never send "auto (large-v3-turbo)" to faster-whisper as a model identifier.
func canonicalModel(value string) string {
	value = strings.TrimSpace(value)
	lower := strings.ToLower(value)
	if lower == "" || lower == "auto" || (strings.HasPrefix(lower, "auto (") && strings.HasSuffix(lower, ")")) {
		return "auto"
	}
	return value
}
func resolvedAutoModel(cpuModel, device string, gpuReady bool) string {
	if !strings.EqualFold(strings.TrimSpace(device), "cpu") && gpuReady {
		return "large-v3-turbo"
	}
	if strings.TrimSpace(cpuModel) == "" {
		return "small.en"
	}
	return strings.TrimSpace(cpuModel)
}
func autoDisplayLabel(cpuModel, device string, gpuReady, checked bool) string {
	if !checked {
		return "auto (checking…)"
	}
	return "auto (" + resolvedAutoModel(cpuModel, device, gpuReady) + ")"
}
func modelDescription(model string) string {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "large-v3-turbo", "turbo":
		return "Fast, high-quality multilingual recognition."
	case "small", "small.en":
		return "Balanced accuracy and speed; practical for CPU use."
	case "base", "base.en":
		return "Lightweight and fast, with lower recognition accuracy."
	case "medium", "medium.en":
		return "Larger and more accurate than small; slower on CPU."
	case "tiny", "tiny.en":
		return "Smallest and fastest; lowest recognition accuracy."
	case "large-v3", "large-v2", "large":
		return "Full-size model; more demanding than Turbo."
	default:
		return "Custom voice recognition model or local model folder."
	}
}
func modelSummary(model, cpuModel, device string, gpuReady bool) string {
	model = canonicalModel(model)
	if model == "auto" {
		model = resolvedAutoModel(cpuModel, device, gpuReady)
	}
	return modelDescription(model)
}
func modelEstimate(model, cpuModel, device string, gpuReady bool) string {
	model = canonicalModel(model)
	if model == "auto" {
		model = resolvedAutoModel(cpuModel, device, gpuReady)
	}
	switch strings.ToLower(model) {
	case "tiny", "tiny.en":
		return "Voice model on first Start: about 80 MB."
	case "base", "base.en":
		return "Voice model on first Start: about 150 MB."
	case "small", "small.en":
		return "Voice model on first Start: about 500 MB."
	case "medium", "medium.en":
		return "Voice model on first Start: about 1.5 GB."
	case "large-v3-turbo", "turbo":
		return "Voice model on first Start: about 1.6 GB."
	case "large-v3", "large-v2", "large":
		return "Voice model on first Start: about 3.1 GB."
	}
	return "Voice-model download size depends on the selected custom model."
}

// An optional repair stays available after setup, without advertising that
// anything needs installing. Explicit CUDA selection also counts as requested.
func dependencyRepairNeeded(checked, coreReady, gpuReady, wantGPU bool) bool {
	return checked && (!coreReady || (wantGPU && !gpuReady))
}

// Five equal columns, with rounding distributed across the available space.
// Each column contains a small caption above a key-only button.
type BindColumn struct{ X, Width int }

func quickBindColumns(left, width, gap int) [5]BindColumn {
	var out [5]BindColumn
	available := max(0, width-4*gap)
	for i := range out {
		start, end := available*i/5, available*(i+1)/5
		out[i] = BindColumn{left + start + i*gap, end - start}
	}
	return out
}

// Actual list-view client width, not its outer width. Values stay compact and
// descriptions receive the remainder after space for borders/rounding.
func fitColumns(clientWidth, dpi int) [3]int {
	if dpi <= 0 {
		dpi = 96
	}
	available := max(0, clientWidth-4)
	first := min(available*30/100, 230*dpi/96)
	value := min(available*20/100, 145*dpi/96)
	return [3]int{first, value, max(0, available-first-value)}
}

// Parse the model actually reported by the recognizer, not an installation guess.
func readyModel(line string) string {
	_, tail, ok := strings.Cut(line, "] READY")
	if !ok {
		return ""
	}
	tail = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(tail), "—-"))
	fields := strings.Split(tail, " / ")
	if len(fields) < 3 {
		return ""
	}
	return strings.TrimSpace(fields[0])
}
