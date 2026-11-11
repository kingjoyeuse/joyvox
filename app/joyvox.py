#!/usr/bin/env python
"""JoyVox: local push-to-talk speech recognition for Quake 2 engine games."""
from __future__ import annotations

# No microphone, GPU, audio player, or model imports before the banner/help.
import argparse
import importlib.metadata
import os
from pathlib import Path
import queue
import struct
import sys
import threading
import time

from joyvox_core import (VERSION, DictionaryCorrector, GameFiles, as_bool,
                         game_is_focused, load_config, load_dictionary,
                         normalize_key, post_process_text, sanitize_game_text,
                         split_message)

SCRIPT_DIR = Path(__file__).resolve().parent
START_TIME = time.perf_counter()


def log(message):
    print(f"[+{time.perf_counter() - START_TIME:5.1f}s] {message}", flush=True)


def format_transcript(raw, config, corrector):
    return sanitize_game_text(post_process_text(corrector.apply(raw), config),
                              ascii_only=as_bool(config.get("STT_ASCII_ONLY", "yes")))


def deliver_text(text, mode, config, files, transmit, focus_check=game_is_focused):
    """Never press a key unless the new output file was committed successfully."""
    chunks = split_message(text)
    if not chunks:
        return None, False
    generation = files.write(chunks)
    if as_bool(config["STT_AUTO_KEY"]) and focus_check(config):
        transmit(mode)
        return generation, True
    return generation, False


class SoundEffects:
    def __init__(self, config):
        self.sounds, self.mixer = {}, None
        if not as_bool(config["SFX_ENABLED"]):
            return
        os.environ.setdefault("PYGAME_HIDE_SUPPORT_PROMPT", "1")
        try:
            import pygame.mixer
            self.mixer = pygame.mixer
            self.mixer.init()
            for name, flag in (("mic_on", "SFX_MIC_ONOFF"), ("mic_off", "SFX_MIC_ONOFF"),
                               ("trans_success", "SFX_TRANSCRIBE_SUCCESS"),
                               ("trans_fail", "SFX_TRANSCRIBE_FAIL")):
                if as_bool(config[flag]):
                    try:
                        self.sounds[name] = self.mixer.Sound(str(SCRIPT_DIR / (name + ".ogg")))
                    except Exception as exc:
                        log(f"Sound {name} unavailable: {exc}")
        except Exception as exc:
            log(f"Sound effects unavailable; recognition can continue: {exc}")

    def play(self, name):
        if name in self.sounds:
            try:
                self.sounds[name].play()
            except Exception as exc:
                log(f"Sound playback failed: {exc}")

    def close(self):
        if self.mixer:
            self.mixer.quit()


class InputHooks:
    """OS hooks only enqueue events; no model work or disk I/O on hook threads."""
    def __init__(self, config, events):
        from pynput import keyboard, mouse
        self.keyboard, self.mouse, self.events = keyboard, mouse, events
        self.bindings = {config[k] for k in ("STT_LISTEN", "STT_KEY", "STT_TEAMKEY",
                         "STT_KEY_AUTO", "STT_TEAMKEY_AUTO") if config[k]}
        self.down, self.ignore_until, self.lock = set(), {}, threading.Lock()
        self.mouse_names = {mouse.Button.left: "mouse1", mouse.Button.right: "mouse2",
                            mouse.Button.middle: "mouse3", mouse.Button.x1: "mouse4",
                            mouse.Button.x2: "mouse5"}
        self.mouse_buttons = {name: button for button, name in self.mouse_names.items()}
        self.kb_controller, self.ms_controller = keyboard.Controller(), mouse.Controller()
        self.listeners = [keyboard.Listener(on_press=lambda k: self._key(k, True),
                                            on_release=lambda k: self._key(k, False)),
                          mouse.Listener(on_click=self._mouse)]

    def _key(self, key, pressed):
        char = getattr(key, "char", None)
        name = normalize_key(char if char else str(key))
        if name not in self.bindings and name.endswith(("_l", "_r")):
            name = name[:-2]
        self._event(name, pressed)

    def _mouse(self, _x, _y, button, pressed):
        self._event(self.mouse_names.get(button, ""), pressed)

    def _event(self, name, pressed):
        if name not in self.bindings:
            return
        with self.lock:
            if time.monotonic() <= self.ignore_until.get(name, 0):
                if not pressed:
                    self.down.discard(name)
                return
            if pressed and name in self.down:
                return
            if pressed:
                self.down.add(name)
            else:
                self.down.discard(name)
        self.events.put(("input", name, pressed))

    def start(self):
        for listener in self.listeners:
            listener.start()
        for listener in self.listeners:
            listener.wait()

    def transmit(self, binding):
        with self.lock:
            self.ignore_until[binding] = time.monotonic() + 0.25
        if binding in self.mouse_buttons:
            controller, key = self.ms_controller, self.mouse_buttons[binding]
        else:
            controller = self.kb_controller
            key = binding if len(binding) == 1 else getattr(self.keyboard.Key, binding)
        controller.press(key)
        try:
            # Give the older game's input polling a chance to see the key.
            time.sleep(0.035)
        finally:
            controller.release(key)

    def check(self):
        for listener in self.listeners:
            if not listener.is_alive():
                listener.join(timeout=0)
                raise RuntimeError("An input listener stopped. Restart JoyVox.")

    def close(self):
        for listener in self.listeners:
            listener.stop()
        for listener in self.listeners:
            if listener.is_alive():
                listener.join(timeout=1)


class JoyVox:
    def __init__(self, config, dictionary, stop_file=None):
        self.config, self.dictionary = config, dictionary
        self.corrector = DictionaryCorrector(dictionary)
        self.events = queue.Queue()
        self.jobs = queue.Queue(maxsize=int(config["STT_QUEUE_SIZE"]))
        self.stopping = threading.Event()
        self.mode = "say"
        self.engine = self.capture = self.hooks = self.sounds = self.worker = None
        self.files = GameFiles(config)
        self._ready_announced = False
        self._loading_notice_at = 0.0
        self.timers = []
        self.stop_file = Path(stop_file) if stop_file else None

    def _model_worker(self):
        try:
            from joyvox_engine import Engine
            self.engine = Engine(self.config, self.dictionary, log=log)
            self.engine.load()
            self.events.put(("model_ready",))
        except Exception as exc:
            self.events.put(("fatal", f"Recognition could not start: {exc}"))
            return
        while not self.stopping.is_set():
            if self.stop_file is not None and self.stop_file.exists():
                log("Stopping JoyVox at the launcher's request.")
                self.stopping.set()
                break
            try:
                clip = self.jobs.get(timeout=0.2)
            except queue.Empty:
                continue
            try:
                started = time.perf_counter()
                self._save_clip(clip)
                raw = self.engine.transcribe(clip.pcm, sample_rate=clip.sample_rate)
                text = format_transcript(raw, self.config, self.corrector)
                self._save_result(raw, text, clip)
                if not self.stopping.is_set():
                    self.events.put(("result", clip, text, time.perf_counter() - started, raw))
            except Exception as exc:
                self._save_result("", "", clip, error=str(exc))
                kind = "error" if self.engine.ready else "fatal"
                self.events.put((kind, f"Transcription failed: {exc}"))
            finally:
                self.jobs.task_done()

    def _save_clip(self, clip):
        if not as_bool(self.config["STT_SAVE_RECORDINGS"]):
            return
        import wave
        directory = Path(self.config["STT_RECORDINGS_PATH"])
        try:
            directory.mkdir(parents=True, exist_ok=True)
            # A failed transcription must not pair new audio with an old result.
            (directory / "joyvox_last_result.json").unlink(missing_ok=True)
            with wave.open(str(directory / "joyvox_last_clip.wav"), "wb") as output:
                output.setnchannels(1)
                output.setsampwidth(2)
                output.setframerate(clip.sample_rate)
                output.writeframes(clip.pcm)
        except OSError as exc:
            log(f"Could not save optional sample: {exc}")

    def _save_result(self, raw, text, clip, error=None):
        if not as_bool(self.config["STT_SAVE_RECORDINGS"]):
            return
        import json
        try:
            path = Path(self.config["STT_RECORDINGS_PATH"]) / "joyvox_last_result.json"
            path.write_text(json.dumps({"raw": raw, "formatted": text,
                "model": self.engine.model_name, "device": self.engine.device,
                "held_seconds": clip.held_seconds, "sample_rate": clip.sample_rate,
                "mode": clip.mode, "sequence": clip.sequence, "error": error},
                indent=2, ensure_ascii=False), encoding="utf-8")
        except OSError as exc:
            log(f"Could not save optional sample result: {exc}")

    def _clear_later(self, generation):
        if not as_bool(self.config["STT_CLEAR_AFTER_SEND"]):
            return
        timer = threading.Timer(float(self.config["STT_CLEAR_DELAY"]),
                                self.events.put, args=(("clear", generation),))
        timer.daemon = True
        self.timers = [t for t in self.timers if t.is_alive()]
        self.timers.append(timer)
        timer.start()

    def _transmit(self, mode):
        self.hooks.transmit(self.config["STT_TEAMKEY" if mode == "say_team" else "STT_KEY"])

    def _input(self, binding, pressed):
        if binding == self.config["STT_LISTEN"]:
            if pressed:
                if not self._ready_announced:
                    now = time.monotonic()
                    if now - self._loading_notice_at > 2:
                        log("Recognition is still loading. Wait for READY before speaking.")
                        self._loading_notice_at = now
                    return
                if self.jobs.full():
                    log("Recognition queue is full. Wait for a result before speaking again.")
                    return
                if self.capture.start(mode=self.mode):
                    self.sounds.play("mic_on")
                    log(f"Listening ({'TEAM' if self.mode == 'say_team' else 'PUBLIC'})...")
            else:
                self.capture.stop()
        elif pressed:
            if binding in (self.config["STT_KEY_AUTO"], self.config["STT_KEY"]):
                self.mode = "say"
            elif binding in (self.config["STT_TEAMKEY_AUTO"], self.config["STT_TEAMKEY"]):
                self.mode = "say_team"
            if binding in (self.config["STT_KEY_AUTO"], self.config["STT_TEAMKEY_AUTO"]):
                log(f"Auto mode: {'TEAM' if self.mode == 'say_team' else 'PUBLIC'} chat")
            if binding in (self.config["STT_KEY"], self.config["STT_TEAMKEY"]) and game_is_focused(self.config):
                self._clear_later(self.files.generation)

    def _handle(self, event):
        kind, *data = event
        if kind == "input":
            self._input(*data)
        elif kind == "clip":
            clip = data[0]
            if not self.capture.is_capturing:
                self.sounds.play("mic_off")
            if clip.held_seconds < float(self.config["STT_MIN_AUDIO_LENGTH"]):
                log(f"Ignored short press ({clip.held_seconds:.2f}s).")
                return
            try:
                self.jobs.put_nowait(clip)
                log(f"Queued {len(clip.pcm) / (2 * clip.sample_rate):.2f}s of audio.")
            except queue.Full:
                log("Recognition queue is full; this clip was not sent. Repeat after the next result.")
        elif kind == "result":
            clip, text, elapsed, _raw = data
            if not text:
                log(f"No speech recognized ({elapsed:.2f}s).")
                if not self.capture.is_capturing:
                    self.sounds.play("trans_fail")
                return
            log(f"Recognized in {elapsed:.2f}s: {text}")
            try:
                generation, sent = deliver_text(text, clip.mode, self.config, self.files, self._transmit)
            except Exception as exc:
                log(f"Delivery failed; no success reported: {exc}")
                return
            if sent:
                log(f"Sent to {'TEAM' if clip.mode == 'say_team' else 'PUBLIC'} chat.")
                self._clear_later(generation)
            elif as_bool(self.config["STT_AUTO_KEY"]):
                log("The selected game is not focused. Text is ready; switch to the game and press your transmit key.")
            else:
                log("Text is ready. Press your transmit key in the game.")
            if not self.capture.is_capturing:
                self.sounds.play("trans_success")
        elif kind == "clear":
            try:
                self.files.clear(expected_generation=data[0])
            except OSError as exc:
                log(f"Could not clear queued text: {exc}")
        elif kind == "error":
            log(data[0])
            if self.sounds and not self.capture.is_capturing:
                self.sounds.play("trans_fail")
        elif kind == "fatal":
            raise RuntimeError(data[0])

    def run(self):
        self.files.initialize()
        log(f"Game files ready: {self.files.output_dir}")
        self.worker = threading.Thread(target=self._model_worker, name="JoyVox-recognition", daemon=True)
        self.worker.start()
        from joyvox_audio import AudioCapture
        self.capture = AudioCapture(
            device=self.config["STT_AUDIO_INPUT_INDEX"],
            pre_roll_ms=int(self.config["STT_PRE_ROLL_MS"]), tail_ms=int(self.config["STT_TAIL_MS"]),
            max_seconds=float(self.config["STT_MAX_RECORD_SECONDS"]),
            on_clip=lambda clip: self.events.put(("clip", clip)),
            on_error=lambda message: self.events.put(("error", message)), log=log,
        )
        if not self.capture.open():
            raise RuntimeError("Could not open the microphone. Refresh the microphone selector in joyvox.exe and choose an input device.")
        self.sounds = SoundEffects(self.config)
        self.hooks = InputHooks(self.config, self.events)
        self.hooks.start()
        log("Microphone and controls ready. Loading recognition in parallel...")
        while not self.stopping.is_set():
            if self.stop_file is not None and self.stop_file.exists():
                log("Stopping JoyVox at the launcher's request.")
                self.stopping.set()
                break
            try:
                self._handle(self.events.get(timeout=0.1))
            except queue.Empty:
                pass
            if self.engine and self.engine.ready and not self._ready_announced:
                self._ready_announced = True
                log(f"READY — {self.engine.model_name} / {self.engine.device} / {self.engine.compute_type}")
                print(f"  Hold {self.config['STT_LISTEN']} to speak. {self.config['STT_KEY_AUTO']} public, "
                      f"{self.config['STT_TEAMKEY_AUTO']} team.\n"
                      f"  Manual send: {self.config['STT_KEY']} public, {self.config['STT_TEAMKEY']} team.\n"
                      f"  In your game, run: exec {self.config['STT_EXEC_FILE']}\n"
                      "  Keep this window running; Ctrl+C stops JoyVox.\n", flush=True)
            self.hooks.check()
            if not self.capture.ready:
                raise RuntimeError("Microphone stream stopped. Check the selected device and restart JoyVox.")

    def close(self):
        self.stopping.set()
        for timer in self.timers:
            timer.cancel()
        if self.hooks:
            self.hooks.close()
        if self.capture:
            self.capture.close()
        if self.sounds:
            self.sounds.close()
        if self.worker and self.worker.is_alive():
            self.worker.join(timeout=0.3)


def diagnose(config, config_path, load_model=False):
    import platform
    import shutil
    import subprocess
    print(f"JoyVox {VERSION}\nPython: {sys.version}\nInterpreter: {sys.executable}\n"
          f"Platform: {platform.platform()}\nArchitecture: {struct.calcsize('P') * 8}-bit\n"
          f"Config: {config_path}\nGame folder: {config['STT_OUTPUT_PATH']}\n"
          f"Game folder exists: {Path(config['STT_OUTPUT_PATH']).is_dir()}\n"
          f"Model: {config['STT_WHISPER_MODEL']}\nDevice: {config['STT_WHISPER_DEVICE']}\n"
          f"Compute type: {config['STT_WHISPER_COMPUTE_TYPE']}\n"
          f"Model cache: {config['STT_MODEL_CACHE'] or '(existing Hugging Face cache)'}\n"
          f"Input device: {config['STT_AUDIO_INPUT_INDEX']}")
    for package in ("faster-whisper", "ctranslate2", "numpy", "scipy", "PyAudio", "pynput", "pygame",
                    "nvidia-cublas-cu12", "nvidia-cudnn-cu12"):
        try:
            version = importlib.metadata.version(package)
        except importlib.metadata.PackageNotFoundError:
            version = "not installed in this interpreter"
        print(f"{package}: {version}")
    if shutil.which("nvidia-smi"):
        try:
            result = subprocess.run(["nvidia-smi", "--query-gpu=name,driver_version,memory.total,memory.used",
                                     "--format=csv,noheader"], capture_output=True, text=True, timeout=5)
            print("NVIDIA GPU:", result.stdout.strip() or result.stderr.strip())
        except (OSError, subprocess.TimeoutExpired) as exc:
            print("NVIDIA query:", exc)
    if load_model:
        from joyvox_engine import Engine
        engine = Engine(config, load_dictionary(config["STT_DICTIONARY_FILE"]), log=log)
        engine.load()
        print(f"Verified recognition ready: {engine.model_name}, {engine.device}, "
              f"{engine.compute_type}; model load {engine.load_seconds:.2f}s")
    else:
        print("Model not loaded. Use --diagnose --load-model to measure and check the backend.")


def main(argv=None):
    parser = argparse.ArgumentParser(description="Local push-to-talk speech recognition for Quake 2 engine games.")
    parser.add_argument("--version", action="version", version=f"JoyVox {VERSION}")
    config_root = SCRIPT_DIR.parent.parent if SCRIPT_DIR.parent.name == ".joyvox" else SCRIPT_DIR.parent
    parser.add_argument("--config", default=str(config_root / "joyvox_config.cfg"))
    parser.add_argument("--stop-file", help="Launcher-owned stop signal file")
    modes = parser.add_mutually_exclusive_group()
    modes.add_argument("--diagnose", action="store_true", help="Print setup/version information without game writes")
    modes.add_argument("--list-devices", action="store_true", help="List microphone device indices")
    modes.add_argument("--list-devices-json", action="store_true", help="List input devices as JSON for the launcher")
    modes.add_argument("--initialize-game", action="store_true", help="Write game bindings without loading recognition or opening the microphone")
    modes.add_argument("--transcribe", metavar="FILE", help="Transcribe an audio file; never sends to the game")
    parser.add_argument("--load-model", action="store_true", help="Also load and check the model during --diagnose")
    args = parser.parse_args(argv)
    if args.load_model and not args.diagnose:
        parser.error("--load-model requires --diagnose")
    if args.list_devices_json:
        import json
        try:
            from joyvox_audio import list_devices
            devices = [{"index": int(d["index"]), "name": str(d.get("name", "Microphone")),
                        "host_api": str(d.get("host_api_name", ""))}
                       for d in list_devices()]
            print(json.dumps(devices, ensure_ascii=False), flush=True)
            return 0
        except Exception as exc:
            print(str(exc), file=sys.stderr, flush=True)
            return 1
    print(f"JoyVox {VERSION} — starting...", flush=True)
    config_path = Path(args.config).expanduser().resolve()
    application = None
    try:
        config = load_config(config_path)
        if args.initialize_game:
            GameFiles(config).initialize()
            log(f"Game bindings ready: {config['STT_EXEC_FILE']}")
        elif args.diagnose:
            diagnose(config, config_path, args.load_model)
        elif args.list_devices:
            from joyvox_audio import list_devices
            print("Available microphone devices:")
            for device in list_devices():
                print(device)
        elif args.transcribe:
            from joyvox_engine import Engine
            path = Path(args.transcribe).expanduser().resolve()
            if not path.is_file():
                raise FileNotFoundError(f"Audio file not found: {path}")
            dictionary = load_dictionary(config["STT_DICTIONARY_FILE"])
            engine = Engine(config, dictionary, log=log)
            engine.load()
            raw = engine.transcribe_file(str(path))
            print("Raw transcript:", raw)
            print("Formatted transcript:", format_transcript(raw, config, DictionaryCorrector(dictionary)))
        else:
            dictionary = load_dictionary(config["STT_DICTIONARY_FILE"])
            log(f"Configuration loaded; {len(dictionary)} dictionary corrections.")
            application = JoyVox(config, dictionary, stop_file=args.stop_file)
            application.run()
    except KeyboardInterrupt:
        log("Stopping JoyVox.")
    except Exception as exc:
        log(f"ERROR: {exc}")
        return 1
    finally:
        if application:
            application.close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
