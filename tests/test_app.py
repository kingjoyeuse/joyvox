"""JoyVox workflow regressions with real queues/threads and fake OS hardware."""
from contextlib import redirect_stdout
import io
import json
import os
from pathlib import Path
import queue
import subprocess
import sys
import tempfile
import threading
from types import ModuleType, SimpleNamespace
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1] / "app"
sys.path.insert(0, str(ROOT))
import joyvox
from joyvox_audio import CapturedAudio
from joyvox_core import DEFAULT_CONFIG, EMPTY_ALIASES, GameFiles


class FakeSounds:
    def __init__(self):
        self.played = []
        self.closed = False

    def play(self, name):
        self.played.append(name)

    def close(self):
        self.closed = True


class FakeCapture:
    def __init__(self):
        self.is_recording = False
        self.ready = True
        self.started_modes = []
        self.closed = False

    @property
    def is_capturing(self):
        return self.is_recording

    def start(self, mode):
        if self.is_recording:
            return False
        self.started_modes.append(mode)
        self.is_recording = True
        return True

    def stop(self):
        self.is_recording = False

    def close(self):
        self.closed = True
        self.is_recording = False
        self.ready = False


class FakeHooks:
    def __init__(self):
        self.transmissions = []
        self.closed = False

    def transmit(self, binding):
        self.transmissions.append(binding)

    def close(self):
        self.closed = True


class FakeListener:
    def __init__(self, **callbacks):
        self.callbacks = callbacks
        self.alive = False

    def start(self):
        self.alive = True

    def wait(self):
        pass

    def is_alive(self):
        return self.alive

    def stop(self):
        self.alive = False

    def join(self, timeout=None):
        pass


class FakeController:
    def __init__(self):
        self.actions = []

    def press(self, key):
        self.actions.append(("press", key))

    def release(self, key):
        self.actions.append(("release", key))


class SpecialKey:
    char = None

    def __init__(self, name):
        self.name = name

    def __str__(self):
        return "Key." + self.name


def fake_pynput():
    module = ModuleType("pynput")
    module.keyboard = SimpleNamespace(
        Key=SimpleNamespace(**{name: SpecialKey(name) for name in (
            "f5", "f6", "f7", "enter", "ctrl", "ctrl_l", "ctrl_r", "space",
        )}), Controller=FakeController, Listener=FakeListener,
    )
    module.mouse = SimpleNamespace(
        Button=SimpleNamespace(**{name: object() for name in ("left", "right", "middle", "x1", "x2")}),
        Controller=FakeController, Listener=FakeListener,
    )
    return module


class AppTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.folder = Path(self.temp.name)
        self.config = dict(DEFAULT_CONFIG,
            STT_OUTPUT_PATH=str(self.folder), SFX_ENABLED="no",
            STT_SAVE_RECORDINGS="no", STT_REQUIRE_GAME_FOCUS="no",
            STT_CLEAR_AFTER_SEND="no",
        )
        self.logged = []
        self.log_patch = patch("joyvox.log", self.logged.append)
        self.log_patch.start()
        self.addCleanup(self.log_patch.stop)

    def app(self):
        app = joyvox.JoyVox(dict(self.config), {})
        app.capture = FakeCapture()
        app.sounds = FakeSounds()
        app.hooks = FakeHooks()
        self.addCleanup(app.close)
        return app

    @staticmethod
    def clip(sequence, mode="say"):
        return CapturedAudio(
            pcm=sequence.to_bytes(2, "little") * 1600, sample_rate=16000,
            mode=mode, held_seconds=0.5, sequence=sequence,
        )

    def get_event(self, app, kind, timeout=2):
        # Worker queue operations are bounded by events, not timing sleeps.
        while True:
            event = app.events.get(timeout=timeout)
            if event[0] == kind:
                return event
            if event[0] in ("fatal", "error"):
                self.fail(str(event))

    def test_worker_is_fifo_serial_and_stops_without_leaking_thread(self):
        app = self.app()
        gates = [threading.Event() for _ in range(3)]
        entered = [threading.Event() for _ in range(3)]
        starts, active, max_active = [], 0, 0
        lock = threading.Lock()
        class Engine:
            ready = False
            model_name, device, compute_type = "fake", "cpu", "int8"
            def __init__(self, *args, **kwargs):
                pass
            def load(self):
                self.ready = True
            def transcribe(self, pcm, sample_rate):
                nonlocal active, max_active
                number = int.from_bytes(pcm[:2], "little")
                with lock:
                    starts.append(number)
                    active += 1
                    max_active = max(active, max_active)
                entered[number - 1].set()
                if not gates[number - 1].wait(2):
                    raise RuntimeError("Test gate timed out")
                with lock:
                    active -= 1
                return f"Message {number}."
        module = ModuleType("joyvox_engine")
        module.Engine = Engine
        with patch.dict(sys.modules, {"joyvox_engine": module}):
            app.worker = threading.Thread(target=app._model_worker)
            app.worker.start()
            try:
                self.get_event(app, "model_ready")
                for number in (1, 2, 3):
                    app.jobs.put(self.clip(number))
                self.assertTrue(entered[0].wait(2))
                self.assertEqual(starts, [1])
                self.assertFalse(entered[1].is_set())
                self.assertEqual(app.jobs.qsize(), 2)
                results = []
                for number in (1, 2, 3):
                    self.assertTrue(entered[number - 1].wait(2))
                    gates[number - 1].set()
                    result = self.get_event(app, "result")
                    results.append((result[1].sequence, result[2]))
                self.assertEqual(results, [(1, "message 1"), (2, "message 2"), (3, "message 3")])
                self.assertEqual(starts, [1, 2, 3])
                self.assertEqual(max_active, 1)
                app.close()
                app.worker.join(2)
                self.assertFalse(app.worker.is_alive())
                self.assertTrue(app.capture.closed)
                self.assertTrue(app.sounds.closed)
                self.assertTrue(app.hooks.closed)
            finally:
                app.stopping.set()
                for gate in gates:
                    gate.set()
                app.worker.join(2)

    def test_shutdown_during_inference_does_not_publish_or_send_late_result(self):
        app = self.app()
        entered, release = threading.Event(), threading.Event()
        class Engine:
            ready = False
            def __init__(self, *args, **kwargs):
                pass
            def load(self):
                self.ready = True
            def transcribe(self, *args, **kwargs):
                entered.set()
                if not release.wait(2):
                    raise RuntimeError("Test gate timed out")
                return "Do not send after shutdown."
        module = ModuleType("joyvox_engine")
        module.Engine = Engine
        with patch.dict(sys.modules, {"joyvox_engine": module}):
            app.worker = threading.Thread(target=app._model_worker)
            app.worker.start()
            try:
                self.get_event(app, "model_ready")
                app.jobs.put(self.clip(1))
                self.assertTrue(entered.wait(2))
                app.close()
                release.set()
                app.worker.join(2)
                self.assertFalse(app.worker.is_alive())
                self.assertTrue(app.events.empty())
                self.assertEqual(app.hooks.transmissions, [])
                self.assertFalse(app.files.output_file.exists())
            finally:
                release.set()
                app.stopping.set()
                app.worker.join(2)

    def test_result_uses_captured_chat_target_after_mode_changes(self):
        app = self.app()
        app.mode = "say"
        app._handle(("result", self.clip(1, mode="say_team"), "at their safe", 0.2, "At their safe."))
        self.assertEqual(app.hooks.transmissions, [self.config["STT_TEAMKEY"]])
        self.assertIn("say_team at their safe", app.files.output_file.read_text())

    def test_start_freezes_current_mode_into_capture_request(self):
        app = self.app()
        app._ready_announced = True
        app.mode = "say_team"
        app._input(app.config["STT_LISTEN"], True)
        app._input(app.config["STT_KEY_AUTO"], True)
        self.assertEqual(app.mode, "say")
        self.assertEqual(app.capture.started_modes, ["say_team"])

    def test_failed_atomic_write_never_presses_transmit_key_or_reports_success(self):
        app = self.app()
        app.files.write = lambda chunks: (_ for _ in ()).throw(PermissionError("read-only game folder"))
        app._handle(("result", self.clip(1), "attack the safe", 0.2, "Attack the safe."))
        self.assertEqual(app.hooks.transmissions, [])
        self.assertNotIn("trans_success", app.sounds.played)
        self.assertTrue(any("Delivery failed" in line for line in self.logged))
        self.assertFalse(app.files.output_file.exists())

    def test_focus_suppression_leaves_text_available_for_manual_send(self):
        app = self.app()
        app.config["STT_REQUIRE_GAME_FOCUS"] = "yes"
        app.config["STT_CLEAR_AFTER_SEND"] = "yes"
        with patch("joyvox_core.foreground_executable", return_value="firefox.exe"):
            app._handle(("result", self.clip(1), "defend the cash", 0.2, "Defend the cash."))
        self.assertEqual(app.hooks.transmissions, [])
        self.assertEqual(app.timers, [])
        self.assertIn("defend the cash", app.files.output_file.read_text())

    def test_empty_result_never_reuses_previous_transmission(self):
        app = self.app()
        app.files.write(["previous line"])
        app._handle(("result", self.clip(1), "", 0.2, ""))
        self.assertEqual(app.hooks.transmissions, [])
        self.assertNotIn("trans_success", app.sounds.played)
        self.assertIn("trans_fail", app.sounds.played)

    def test_full_queue_does_not_begin_another_recording(self):
        app = self.app()
        app._ready_announced = True
        for number in range(app.jobs.maxsize):
            app.jobs.put(self.clip(number + 1))
        app._input(app.config["STT_LISTEN"], True)
        self.assertEqual(app.capture.started_modes, [])

    def test_ptt_before_readiness_does_not_capture(self):
        app = self.app()
        app._input(app.config["STT_LISTEN"], True)
        self.assertEqual(app.capture.started_modes, [])

    def test_special_keyboard_and_mouse_bindings_use_os_key_objects(self):
        config = dict(self.config, STT_KEY="f7", STT_TEAMKEY="mouse5")
        fake = fake_pynput()
        events = queue.Queue()
        with patch.dict(sys.modules, {"pynput": fake}):
            hooks = joyvox.InputHooks(config, events)
        with patch("joyvox.time.sleep"):
            hooks.transmit("f7")
            hooks.transmit("mouse5")
        self.assertEqual(hooks.kb_controller.actions, [
            ("press", fake.keyboard.Key.f7), ("release", fake.keyboard.Key.f7),
        ])
        self.assertEqual(hooks.ms_controller.actions, [
            ("press", fake.mouse.Button.x2), ("release", fake.mouse.Button.x2),
        ])
        hooks._key(fake.keyboard.Key.f5, True)
        hooks._key(fake.keyboard.Key.f5, True)  # keyboard autorepeat
        hooks._key(fake.keyboard.Key.f5, False)
        hooks._mouse(0, 0, fake.mouse.Button.x1, True)
        self.assertEqual([events.get_nowait() for _ in range(events.qsize())], [
            ("input", "f5", True), ("input", "f5", False), ("input", "mouse4", True),
        ])

    def test_generic_modifier_binding_accepts_left_and_right_os_keys(self):
        fake = fake_pynput()
        events = queue.Queue()
        with patch.dict(sys.modules, {"pynput": fake}):
            hooks = joyvox.InputHooks(dict(self.config, STT_LISTEN="ctrl"), events)
        hooks._key(fake.keyboard.Key.ctrl_l, True)
        hooks._key(fake.keyboard.Key.ctrl_l, False)
        hooks._key(fake.keyboard.Key.ctrl_r, True)
        self.assertEqual([events.get_nowait() for _ in range(events.qsize())], [
            ("input", "ctrl", True), ("input", "ctrl", False), ("input", "ctrl", True),
        ])

    def test_ignored_release_does_not_leave_key_permanently_debounced(self):
        fake = fake_pynput()
        events = queue.Queue()
        with patch.dict(sys.modules, {"pynput": fake}):
            hooks = joyvox.InputHooks(self.config, events)
        hooks._event("j", True)
        with patch("joyvox.time.sleep"):
            hooks.transmit("j")
        hooks._event("j", False)  # physical release during suppression
        hooks.ignore_until["j"] = 0.0
        hooks._event("j", True)
        self.assertEqual([events.get_nowait() for _ in range(events.qsize())], [
            ("input", "j", True), ("input", "j", True),
        ])

    def test_import_help_version_and_diagnose_avoid_backend_and_game_side_effects(self):
        # Subprocesses use -S so host sitecustomize imports are not blamed on the app.
        config_path = self.folder / "settings.cfg"
        config_path.write_text(f"STT_OUTPUT_PATH={self.folder}\nSTT_SAVE_RECORDINGS=yes\n", encoding="utf-8")
        before = {p.name: p.read_bytes() for p in self.folder.iterdir() if p.is_file()}
        code = """\
import json, sys
sys.path.insert(0, sys.argv[1])
import joyvox
try:
    result = joyvox.main(json.loads(sys.argv[2])) if sys.argv[2] != 'null' else 0
except SystemExit as exc:
    result = exc.code
for name in ('numpy','torch','ctranslate2','faster_whisper','pyaudio','pynput','pygame','scipy'):
    assert name not in sys.modules, name + ' loaded unexpectedly'
raise SystemExit(result)
"""
        for arguments in (None, ["--help"], ["--version"], ["--config", str(config_path), "--diagnose"]):
            with self.subTest(arguments=arguments):
                result = subprocess.run(
                    [sys.executable, "-S", "-c", code, str(ROOT), json.dumps(arguments)],
                    cwd=self.folder, env=dict(os.environ, PYTHONDONTWRITEBYTECODE="1"),
                    capture_output=True, text=True, timeout=10,
                )
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        after = {p.name: p.read_bytes() for p in self.folder.iterdir() if p.is_file()}
        self.assertEqual(before, after)
        self.assertEqual({p.name for p in self.folder.iterdir()}, {"settings.cfg"})


if __name__ == "__main__":
    unittest.main()
