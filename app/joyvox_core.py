"""Configuration, text processing and Kingpin files (standard library only)."""
from __future__ import annotations

import os
import re
import tempfile
import threading
import unicodedata
from pathlib import Path

VERSION = "4.2.5"

DEFAULT_CONFIG = {
    "STT_LISTEN": "mouse4", "STT_KEY": "j", "STT_TEAMKEY": "k",
    "STT_KEY_AUTO": "f5", "STT_TEAMKEY_AUTO": "f6",
    "STT_KINGPIN_EXE": "", "STT_OUTPUT_PATH": "",
    "STT_OUTPUT_FILE": "joyvoxinput.cfg", "STT_EXEC_FILE": "joyvox.cfg",
    "STT_DICTIONARY_FILE": "joyvox_dictionary.cfg",
    "STT_AUDIO_INPUT_INDEX": "auto", "STT_SENTENCECASE": "no",
    "STT_CAPITALIZE": "no", "STT_ENDWITHPERIOD": "no",
    "STT_ALLOWEXCLAMATION": "no", "STT_ASCII_ONLY": "yes",
    "SFX_ENABLED": "yes", "SFX_MIC_ONOFF": "yes",
    "SFX_TRANSCRIBE_SUCCESS": "yes", "SFX_TRANSCRIBE_FAIL": "yes",
    "STT_AUTO_KEY": "yes", "STT_CLEAR_AFTER_SEND": "yes",
    "STT_CLEAR_DELAY": "1.0", "STT_REQUIRE_GAME_FOCUS": "yes",
    "STT_GAME_EXES": "kingpin.exe,kingpinx.exe,kingpin2.exe,kingpin_x64.exe",
    "STT_WHISPER_MODEL": "auto", "STT_CPU_MODEL": "small.en",
    "STT_WHISPER_DEVICE": "auto", "STT_WHISPER_COMPUTE_TYPE": "auto",
    "STT_LANGUAGE": "en", "STT_MIN_AUDIO_LENGTH": "0.15",
    "STT_PROMPT": "Push forward and cover me. Watch the stairs. I need health, armor, and ammo. Nice shot, good game. Quake II, Kingpin, deathmatch, capture the flag, Bagman, cash bag, safe, rocket launcher, railgun.",
    "STT_HOTWORDS": "Kingpin, Bagman, cash bag, safe, team",
    "STT_BEAM_SIZE": "5", "STT_VAD_FILTER": "yes",
    "STT_PRE_ROLL_MS": "150", "STT_TAIL_MS": "120",
    "STT_MAX_RECORD_SECONDS": "30", "STT_QUEUE_SIZE": "3",
    "STT_MODEL_CACHE": "", "STT_CUDA_DLL_PATH": "",
    "STT_MODEL_WARMUP": "yes", "STT_CPU_THREADS": "4",
    "STT_SAVE_RECORDINGS": "no", "STT_RECORDINGS_PATH": "recordings",
}


def as_bool(value):
    return str(value).strip().lower() in {"yes", "y", "true", "1", "on"}


def _read_pairs(path):
    # Only whitespace-prefixed // is an inline comment. URLs and UNC paths survive.
    with Path(path).open(encoding="utf-8-sig") as handle:
        for line in handle:
            line = line.strip()
            if not line or line.startswith(("#", "//")):
                continue
            line = re.split(r"\s+//", line, maxsplit=1)[0].rstrip()
            if "=" in line:
                key, value = line.split("=", 1)
                yield key.strip(), value.strip()


def load_config(path):
    path = Path(path).resolve()
    values = dict(DEFAULT_CONFIG)
    values.update(_read_pairs(path))
    validate_config(values)
    for key in ("STT_OUTPUT_PATH", "STT_DICTIONARY_FILE", "STT_MODEL_CACHE",
                "STT_RECORDINGS_PATH"):
        value = values[key]
        if value:
            expanded = Path(os.path.expandvars(value)).expanduser()
            if not expanded.is_absolute():
                expanded = path.parent / expanded
            values[key] = str(expanded)
    return values


def load_dictionary(path):
    if not Path(path).exists():
        return {}
    entries = {}
    for wrong, correct in _read_pairs(path):
        wrong = " ".join(wrong.lower().split())
        if len(correct) >= 2 and correct[0] == correct[-1] and correct[0] in "\"'":
            correct = correct[1:-1]
        if wrong:
            entries[wrong] = correct
    return entries


SPECIAL_KEYS = {
    "space", "tab", "enter", "esc", "shift", "shift_l", "shift_r",
    "ctrl", "ctrl_l", "ctrl_r", "alt", "alt_l", "alt_r", "alt_gr",
    "backspace", "delete", "insert", "home", "end", "page_up", "page_down",
    "up", "down", "left", "right", "caps_lock", "num_lock", "scroll_lock",
    "pause", "print_screen",
} | {f"f{i}" for i in range(1, 25)}
KEY_ALIASES = {"escape": "esc", "return": "enter", "control": "ctrl",
               "pgup": "page_up", "pgdn": "page_down", "del": "delete",
               "ins": "insert", "capslock": "caps_lock",
               "numlock": "num_lock", "scrolllock": "scroll_lock"}
GAME_KEYS = {"page_up": "pgup", "page_down": "pgdn", "delete": "del",
             "insert": "ins", "caps_lock": "capslock", "num_lock": "numlock",
             "scroll_lock": "scrolllock", "print_screen": "printscreen"}


def normalize_key(value):
    value = value.strip().lower()
    if value.startswith("key."):
        value = value[4:]
    return KEY_ALIASES.get(value, value)


def valid_binding(value):
    value = normalize_key(value)
    return (not value or re.fullmatch(r"[a-z0-9]", value) is not None
            or value in SPECIAL_KEYS or value in {f"mouse{i}" for i in range(1, 6)})


def validate_config(values):
    for key in ("STT_LISTEN", "STT_KEY", "STT_TEAMKEY", "STT_KEY_AUTO", "STT_TEAMKEY_AUTO"):
        if not valid_binding(values[key]):
            raise ValueError(f"{key}: unsupported key {values[key]!r}; use a letter, named key or mouse1–mouse5")
        values[key] = normalize_key(values[key])
    required = [values[x] for x in ("STT_LISTEN", "STT_KEY", "STT_TEAMKEY")]
    if any(not x for x in required):
        raise ValueError("STT_LISTEN, STT_KEY and STT_TEAMKEY must each have a binding")
    keys = required + [values[x] for x in ("STT_KEY_AUTO", "STT_TEAMKEY_AUTO") if values[x]]
    # Left/right modifiers collapse to one key in the Quake/Kingpin bind table.
    game_keys = [re.sub(r"_(l|r)$", "", key) for key in keys]
    if len(game_keys) != len(set(game_keys)):
        raise ValueError("Listen, transmit and target-selection keys must be different")
    for key in ("STT_OUTPUT_FILE", "STT_EXEC_FILE"):
        if not re.fullmatch(r"[A-Za-z0-9_-][A-Za-z0-9_.-]*\.cfg", values[key]):
            raise ValueError(f"{key} must be a plain .cfg filename without spaces or folders")
    if values["STT_OUTPUT_FILE"].lower() == values["STT_EXEC_FILE"].lower():
        raise ValueError("STT_OUTPUT_FILE and STT_EXEC_FILE must differ")
    ranges = {
        "STT_MIN_AUDIO_LENGTH": (0, 3), "STT_PRE_ROLL_MS": (0, 1000),
        "STT_TAIL_MS": (0, 1000), "STT_MAX_RECORD_SECONDS": (1, 120),
        "STT_QUEUE_SIZE": (1, 10), "STT_CLEAR_DELAY": (0.2, 10),
        "STT_BEAM_SIZE": (1, 10), "STT_CPU_THREADS": (1, 64),
    }
    integer_keys = {"STT_PRE_ROLL_MS", "STT_TAIL_MS", "STT_QUEUE_SIZE",
                    "STT_BEAM_SIZE", "STT_CPU_THREADS"}
    for key, (low, high) in ranges.items():
        try:
            number = float(values[key])
        except ValueError as exc:
            raise ValueError(f"{key} must be numeric") from exc
        if not low <= number <= high or (key in integer_keys and not number.is_integer()):
            raise ValueError(f"{key} must be {'an integer' if key in integer_keys else 'a number'} from {low} to {high}")
    if values["STT_WHISPER_DEVICE"].lower() not in {"auto", "cuda", "cpu"}:
        raise ValueError("STT_WHISPER_DEVICE must be auto, cuda or cpu")


class DictionaryCorrector:
    """Single-pass literal phrase substitution; replacements never cascade."""

    def __init__(self, entries):
        self.entries = {" ".join(k.lower().split()): v for k, v in entries.items() if k.strip()}
        keys = sorted(self.entries, key=lambda key: (-len(key), key))
        alternatives = [r"\s+".join(re.escape(p) for p in k.split()) for k in keys]
        self.pattern = re.compile(r"(?<!\w)(?:" + "|".join(alternatives) + r")(?!\w)", re.I) if keys else None

    def apply(self, text):
        if self.pattern is None:
            return text
        return self.pattern.sub(lambda m: self.entries[" ".join(m.group().lower().split())], text)


def post_process_text(text, config):
    text = " ".join(text.split())
    if not text:
        return ""
    if not as_bool(config.get("STT_ALLOWEXCLAMATION", "yes")):
        text = re.sub(r"[.!]*![.!]*", ".", text)
    if not as_bool(config.get("STT_CAPITALIZE", "yes")):
        words = []
        for word in text.split():
            letters = re.sub(r"[^a-zA-Z]", "", word)
            words.append(word if letters and letters.isupper() else word.lower())
        text = " ".join(words)
    if as_bool(config.get("STT_SENTENCECASE", "yes")):
        text = text[0].upper() + text[1:]
    else:
        letters = re.sub(r"[^a-zA-Z]", "", text.split()[0])
        if not (letters and letters.isupper()):
            text = text[0].lower() + text[1:]
    if as_bool(config.get("STT_ENDWITHPERIOD", "no")):
        if text[-1] not in ".!?":
            text += "."
    elif text.endswith(".") and not text.endswith("..."):
        text = text[:-1]
    return text.strip()


def sanitize_game_text(text, ascii_only=True):
    # Normalize before removing console syntax: fullwidth punctuation can
    # otherwise turn back into a semicolon/quote/dollar after sanitization.
    text = unicodedata.normalize("NFKC", text)
    text = text.translate(str.maketrans({"“": "'", "”": "'", "‘": "'", "’": "'",
                                       "—": "-", "–": "-", "…": "..."}))
    if ascii_only:
        text = unicodedata.normalize("NFKD", text).encode("ascii", "ignore").decode("ascii")
    text = text.replace('"', "'").replace(";", ",").replace("$", " dollars ")
    text = re.sub(r"/+", "/", text.replace("\\", "/"))
    text = "".join(" " if unicodedata.category(c).startswith("C") else c for c in text)
    if not ascii_only:
        text = text.encode("cp1252", "replace").decode("cp1252")
    return " ".join(text.split())


def split_message(text, max_length=130, max_messages=3):
    if max_length < 4 or max_messages < 1:
        raise ValueError("Invalid message limits")
    remaining = " ".join(text.split())
    chunks = []
    while remaining and len(chunks) < max_messages:
        if len(remaining) <= max_length:
            chunks.append(remaining)
            remaining = ""
            break
        split_at = remaining.rfind(" ", 0, max_length + 1)
        if split_at <= 0:
            split_at = max_length
        chunks.append(remaining[:split_at])
        remaining = remaining[split_at:].lstrip()
    if remaining:
        chunks[-1] = chunks[-1][:max_length - 3].rstrip() + "..."
    return chunks


EMPTY_ALIASES = 'alias sttoutput ""\nalias sttoutputteam ""\n'


def build_output(transmissions):
    # Defense in depth for all callers, including dictionaries and offline tests.
    chunks = [sanitize_game_text(t, ascii_only=False) for t in transmissions]
    public = "; ".join("say " + t for t in chunks if t)
    team = "; ".join("say_team " + t for t in chunks if t)
    return f'alias sttoutput "{public}"\nalias sttoutputteam "{team}"\n'


def atomic_write(path, content):
    path = Path(path)
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(mode="w", dir=path.parent, prefix=f".{path.name}.",
                                         suffix=".tmp", encoding="cp1252", newline="\n", delete=False) as handle:
            temporary = Path(handle.name)
            handle.write(content)
            handle.flush()
        os.replace(temporary, path)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


class GameFiles:
    def __init__(self, config):
        self.config = config
        self.output_dir = Path(config["STT_OUTPUT_PATH"])
        self.output_file = self.output_dir / config["STT_OUTPUT_FILE"]
        self.lock = threading.RLock()
        self.generation = 0

    def initialize(self):
        if not self.config["STT_OUTPUT_PATH"].strip():
            raise ValueError("Choose your game executable and game/mod folder in joyvox.exe before starting.")
        if not self.output_dir.is_dir():
            raise FileNotFoundError(f"Game folder does not exist: {self.output_dir}. Set STT_OUTPUT_PATH in joyvox_config.cfg.")
        # Reset first, so an interrupted setup cannot leave old speech queued.
        self.clear()
        def game_key(value):
            value = re.sub(r"_(l|r)$", "", value)
            return GAME_KEYS.get(value, value)
        content = (
            f'// Generated by JoyVox. In your game: exec {self.config["STT_EXEC_FILE"]}\n'
            'alias +stt "echo Listening..."\n'
            'alias -stt "echo Transcribing..."\n'
            f'alias sttsay "exec {self.config["STT_OUTPUT_FILE"]}; sttoutput"\n'
            f'alias sttsayteam "exec {self.config["STT_OUTPUT_FILE"]}; sttoutputteam"\n'
            f'bind {game_key(self.config["STT_LISTEN"])} +stt\n'
            f'bind {game_key(self.config["STT_KEY"])} sttsay\n'
            f'bind {game_key(self.config["STT_TEAMKEY"])} sttsayteam\n'
        )
        atomic_write(self.output_dir / self.config["STT_EXEC_FILE"], content)

    def write(self, transmissions):
        with self.lock:
            atomic_write(self.output_file, build_output(transmissions))
            self.generation += 1
            return self.generation

    def clear(self, expected_generation=None):
        with self.lock:
            if expected_generation is not None and expected_generation != self.generation:
                return False
            atomic_write(self.output_file, EMPTY_ALIASES)
            self.generation += 1
            return True


def foreground_executable():
    """Windows foreground process name; no title-based false matches."""
    if os.name != "nt":
        return ""
    import ctypes
    from ctypes import wintypes
    user32 = ctypes.WinDLL("user32", use_last_error=True)
    kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
    user32.GetForegroundWindow.restype = wintypes.HWND
    user32.GetWindowThreadProcessId.argtypes = (wintypes.HWND, ctypes.POINTER(wintypes.DWORD))
    kernel32.OpenProcess.argtypes = (wintypes.DWORD, wintypes.BOOL, wintypes.DWORD)
    kernel32.OpenProcess.restype = wintypes.HANDLE
    kernel32.QueryFullProcessImageNameW.argtypes = (wintypes.HANDLE, wintypes.DWORD, wintypes.LPWSTR, ctypes.POINTER(wintypes.DWORD))
    kernel32.QueryFullProcessImageNameW.restype = wintypes.BOOL
    kernel32.CloseHandle.argtypes = (wintypes.HANDLE,)
    pid = wintypes.DWORD()
    user32.GetWindowThreadProcessId(user32.GetForegroundWindow(), ctypes.byref(pid))
    process = kernel32.OpenProcess(0x1000, False, pid.value)
    if not process:
        return ""
    try:
        buffer = ctypes.create_unicode_buffer(32768)
        size = wintypes.DWORD(len(buffer))
        if kernel32.QueryFullProcessImageNameW(process, 0, buffer, ctypes.byref(size)):
            return Path(buffer.value).name.lower()
        return ""
    finally:
        kernel32.CloseHandle(process)


def game_is_focused(config):
    if not as_bool(config.get("STT_REQUIRE_GAME_FOCUS", "yes")):
        return True
    allowed = {x.strip().lower() for x in config["STT_GAME_EXES"].split(",") if x.strip()}
    return foreground_executable() in allowed
