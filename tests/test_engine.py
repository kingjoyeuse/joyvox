"""Behavioral engine regressions; no model downloads, microphone, or GPU needed."""
import importlib
from pathlib import Path
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import numpy as np

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "app"))
from joyvox_engine import Engine


class CacheMiss(Exception):
    pass


class FakeModel:
    def __init__(self, device, calls, fail_warmup=False, fail_audio=False):
        self.device = device
        self.calls = calls
        self.fail_warmup = fail_warmup
        self.fail_audio = fail_audio

    def transcribe(self, audio, **options):
        self.calls.append((self.device, np.array(audio, copy=True), options))
        def segments():
            # Deliberately lazy like faster-whisper: creating this generator
            # alone does not discover a CUDA runtime problem.
            if self.fail_warmup and options.get("max_new_tokens") == 1:
                raise RuntimeError("Cannot load cudnn_ops64_9.dll")
            if self.fail_audio and "max_new_tokens" not in options:
                raise RuntimeError("CUDA out of memory")
            yield SimpleNamespace(text=" Hold the safe. ")
            yield SimpleNamespace(text=" Elfor is here. ")
        return segments(), SimpleNamespace()


class EngineTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.model_dir = Path(self.temp.name) / "model"
        self.model_dir.mkdir()
        for name in ("config.json", "model.bin", "tokenizer.json"):
            (self.model_dir / name).write_bytes(b"test fixture")
        self.downloads = []
        self.loads = []
        self.calls = []
        self.messages = []

    def engine(self, config=None, cuda=True, supported=None, fail_warmup=False, fail_audio=False):
        engine = Engine({"STT_MODEL_WARMUP": "no", **(config or {})}, {}, self.messages.append)
        engine._np = np
        engine._ct2 = SimpleNamespace(
            get_cuda_device_count=lambda: 1 if cuda else 0,
            get_supported_compute_types=lambda device: supported or (
                {"int8_float16", "float16", "float32"} if device == "cuda" else {"int8", "float32"}
            ),
        )
        engine._cache_miss_errors = (CacheMiss,)
        def download(name, **options):
            self.downloads.append((name, options))
            return str(self.model_dir)
        def factory(folder, **options):
            self.loads.append((folder, options))
            return FakeModel(
                options["device"], self.calls,
                fail_warmup=fail_warmup and options["device"] == "cuda",
                fail_audio=fail_audio and options["device"] == "cuda",
            )
        engine._download_model = download
        engine._whisper_class = factory
        return engine

    def test_import_does_not_load_heavy_dependencies(self):
        # -S excludes host sitecustomize hooks; this tests imports by JoyVox.
        result = subprocess.run(
            [sys.executable, "-S", "-c", "import sys; import joyvox_engine; "
             "assert not set(('numpy','faster_whisper','ctranslate2','torch','scipy')) & set(sys.modules)"],
            cwd=Path(__file__).resolve().parents[1] / "app", capture_output=True, text=True,
        )
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_configured_cache_is_respected_and_model_is_loaded_once(self):
        engine = self.engine({"STT_MODEL_CACHE": "/another/cache"}, cuda=False)
        engine.load()
        self.assertEqual(self.downloads, [("small.en", {"cache_dir": "/another/cache", "local_files_only": True})])
        self.assertTrue(engine.ready)
        self.assertEqual(engine.device, "cpu")
        self.assertEqual(engine.compute_type, "int8")
        engine.load()
        self.assertEqual(len(self.loads), 1)

    def test_cache_misses_download_once_into_configured_cache(self):
        engine = self.engine({"STT_MODEL_CACHE": "/new/cache"}, cuda=False)
        def download(name, **options):
            self.downloads.append(options)
            if options["local_files_only"]:
                raise CacheMiss()
            return str(self.model_dir)
        engine._download_model = download
        engine.load()
        self.assertEqual(self.downloads, [
            {"cache_dir": "/new/cache", "local_files_only": True},
            {"cache_dir": "/new/cache", "local_files_only": False},
        ])

    def test_incomplete_cache_repairs_in_place_instead_of_loading_missing_tokenizer(self):
        (self.model_dir / "tokenizer.json").unlink()
        engine = self.engine(cuda=False)
        def download(name, **options):
            self.downloads.append(options)
            if not options["local_files_only"]:
                (self.model_dir / "tokenizer.json").write_bytes(b"repaired")
            return str(self.model_dir)
        engine._download_model = download
        engine.load()
        self.assertEqual(self.downloads[-1], {"cache_dir": None, "local_files_only": False})
        self.assertEqual(len(self.loads), 1)

    def test_explicit_local_model_never_calls_download(self):
        engine = self.engine({"STT_WHISPER_MODEL": str(self.model_dir)}, cuda=False)
        engine.load()
        self.assertEqual(self.downloads, [])
        self.assertEqual(engine.model_name, str(self.model_dir))

    def test_bad_explicit_local_model_is_not_treated_as_hub_id(self):
        engine = self.engine({"STT_WHISPER_MODEL": str(Path(self.temp.name) / "missing")}, cuda=False)
        with self.assertRaises(FileNotFoundError):
            engine.load()
        self.assertFalse(engine.ready)
        self.assertEqual(self.downloads, [])

    def test_auto_cuda_selects_turbo_and_runs_lazy_inference_before_ready(self):
        engine = self.engine()
        engine.load()
        self.assertEqual(engine.model_name, "large-v3-turbo")
        self.assertEqual(engine.compute_type, "float16")
        self.assertTrue(engine.ready)
        self.assertEqual(len(self.calls), 1)
        self.assertEqual(self.calls[0][2]["max_new_tokens"], 1)
        self.assertFalse(self.calls[0][2]["vad_filter"])

    def test_int8_float16_is_supported_gpu_alternative(self):
        engine = self.engine(supported={"int8_float16", "float32"})
        engine.load()
        self.assertEqual(engine.compute_type, "int8_float16")

    def test_lazy_cuda_failure_falls_back_to_small_cpu_when_auto(self):
        engine = self.engine(fail_warmup=True)
        engine.load()
        self.assertTrue(engine.ready)
        self.assertEqual((engine.device, engine.model_name), ("cpu", "small.en"))
        self.assertEqual([x[1]["device"] for x in self.loads], ["cuda", "cpu"])
        self.assertTrue(any("cudnn_ops64_9.dll" in m for m in self.messages))

    def test_explicit_model_is_preserved_on_cpu_fallback_and_resolved_once(self):
        engine = self.engine({"STT_WHISPER_MODEL": "base.en"}, fail_warmup=True)
        engine.load()
        self.assertEqual((engine.device, engine.model_name), ("cpu", "base.en"))
        self.assertEqual(len(self.downloads), 1)

    def test_explicit_cuda_does_not_silently_fall_back(self):
        engine = self.engine({"STT_WHISPER_DEVICE": "cuda"}, fail_warmup=True)
        with self.assertRaisesRegex(RuntimeError, "joyvox.exe"):
            engine.load()
        self.assertFalse(engine.ready)
        self.assertEqual([x[1]["device"] for x in self.loads], ["cuda"])

    def test_download_error_does_not_trigger_cpu_fallback(self):
        engine = self.engine()
        def download(name, **options):
            if options["local_files_only"]:
                raise CacheMiss()
            raise RuntimeError("HTTP connection failed")
        engine._download_model = download
        with self.assertRaisesRegex(RuntimeError, "HTTP connection failed"):
            engine.load()
        self.assertFalse(engine.ready)
        self.assertEqual(self.loads, [])

    def test_invalid_compute_option_is_visible(self):
        engine = self.engine({"STT_WHISPER_COMPUTE_TYPE": "invalid"})
        with self.assertRaisesRegex(ValueError, "Compute type"):
            engine.load()
        self.assertFalse(engine.ready)
        self.assertEqual(self.loads, [])

    def test_windows_cuda_preflight_keeps_handles_and_checks_only_once(self):
        engine = self.engine()
        checked = []
        def win_dll(name):
            checked.append(name)
            return object()
        with patch("joyvox_engine.os.name", "nt"), patch(
            "joyvox_engine.importlib.import_module", return_value=SimpleNamespace(WinDLL=win_dll)
        ):
            engine._check_windows_cuda_libraries()
            engine._check_windows_cuda_libraries()
        self.assertEqual(len(checked), 5)
        self.assertEqual(len(engine._cuda_library_handles), 5)
        self.assertIn("cudnn_ops64_9.dll", checked)

    def test_windows_missing_cuda_library_is_a_catchable_error(self):
        engine = self.engine()
        def win_dll(name):
            raise OSError("module not found")
        with patch("joyvox_engine.os.name", "nt"), patch(
            "joyvox_engine.importlib.import_module", return_value=SimpleNamespace(WinDLL=win_dll)
        ):
            with self.assertRaisesRegex(RuntimeError, "CUDA library") as failure:
                engine._check_windows_cuda_libraries()
        self.assertTrue(engine._is_cuda_failure(failure.exception))
        self.assertFalse(engine._cuda_libraries_checked)

    def test_pcm_is_float32_mono_and_decoder_options_are_set(self):
        engine = self.engine(cuda=False)
        engine.load()
        samples = np.array([0, 8192, -8192, 1024], dtype="<i2")
        text = engine.transcribe(samples.tobytes())
        self.assertEqual(text, "Hold the safe. Elfor is here.")
        _, audio, options = self.calls[-1]
        np.testing.assert_array_equal(audio, samples.astype(np.float32) / 32768.0)
        self.assertEqual(audio.dtype, np.float32)
        self.assertEqual(options["language"], "en")
        self.assertEqual(options["beam_size"], 5)
        self.assertEqual(options["temperature"], 0.0)
        self.assertFalse(options["condition_on_previous_text"])
        self.assertEqual(options["vad_parameters"]["speech_pad_ms"], 250)

    def test_digital_silence_does_not_hallucinate(self):
        engine = self.engine(cuda=False)
        engine.load()
        self.assertEqual(engine.transcribe(bytes(32000)), "")
        self.assertEqual(self.calls, [])

    def test_quiet_speech_is_not_gated_or_amplified(self):
        engine = self.engine(cuda=False)
        engine.load()
        samples = np.array([1, -1] * 8000, dtype="<i2")
        self.assertTrue(engine.transcribe(samples.tobytes()))
        np.testing.assert_array_equal(self.calls[-1][1], samples.astype(np.float32) / 32768.0)
        self.assertTrue(any("very quiet" in m for m in self.messages))

    def test_clipping_is_reported(self):
        engine = self.engine(cuda=False)
        engine.load()
        engine.transcribe(np.array([32767, -32768] * 100, dtype="<i2").tobytes())
        self.assertTrue(any("clipping" in m for m in self.messages))

    def test_pcm_at_48khz_is_resampled_not_reinterpreted(self):
        engine = self.engine(cuda=False)
        engine.load()
        t = np.arange(48000) / 48000.0
        samples = (np.sin(2 * np.pi * 1000 * t) * 10000).astype("<i2")
        engine.transcribe(samples.tobytes(), sample_rate=48000)
        audio = self.calls[-1][1]
        self.assertEqual(len(audio), 16000)
        self.assertAlmostEqual(float(np.max(audio[100:-100])), 10000 / 32768.0, places=2)
        # The physical 1 kHz tone remains 1 kHz after resampling.
        frequency = np.argmax(np.abs(np.fft.rfft(audio)))
        self.assertEqual(frequency, 1000)

    def test_pcm_validation(self):
        engine = self.engine(cuda=False)
        engine.load()
        with self.assertRaises(ValueError):
            engine.transcribe(b"x")
        with self.assertRaises(ValueError):
            engine.transcribe(b"\0\0", 0)
        with self.assertRaises(ValueError):
            engine.transcribe(b"\0\0", True)

    def test_recognition_must_be_ready(self):
        engine = self.engine()
        with self.assertRaisesRegex(RuntimeError, "not ready"):
            engine.transcribe(b"\0\0")

    def test_hints_are_deduplicated_ordered_and_token_bounded(self):
        engine = self.engine({
            "STT_PROMPT": "Context " * 200,
            "STT_HOTWORDS": "Kingpin, Elfor, Kingpin; boiler room",
        }, cuda=False)
        engine.dictionary = {"l4": "Elfor", "king pen": "Kingpin", "x": '"boiler room"'}
        engine.load()
        engine._model.hf_tokenizer = SimpleNamespace(
            encode=lambda s: SimpleNamespace(ids=[ord(c) for c in s]),
            decode=lambda ids: "".join(chr(c) for c in ids),
        )
        engine.transcribe(np.array([123, -123], dtype="<i2").tobytes())
        options = self.calls[-1][2]
        self.assertLessEqual(len(options["initial_prompt"]), 96)
        self.assertEqual(options["hotwords"], "Kingpin, Elfor, boiler room")

    def test_runtime_cuda_error_retries_original_audio_once_on_cpu(self):
        engine = self.engine(fail_audio=True)
        engine.load()
        pcm = np.array([1000, -1000] * 1000, dtype="<i2").tobytes()
        self.assertTrue(engine.transcribe(pcm))
        self.assertEqual(engine.device, "cpu")
        self.assertTrue(engine.ready)
        self.assertEqual([x[0] for x in self.calls], ["cuda", "cuda", "cpu"])
        np.testing.assert_array_equal(self.calls[-2][1], self.calls[-1][1])
        engine.transcribe(pcm)
        self.assertEqual(len(self.loads), 2)

    def test_explicit_cuda_runtime_failure_does_not_change_devices(self):
        engine = self.engine({"STT_WHISPER_DEVICE": "cuda"}, fail_audio=True)
        engine.load()
        with self.assertRaisesRegex(RuntimeError, "CUDA out of memory"):
            engine.transcribe(np.array([1000, -1000], dtype="<i2").tobytes())
        self.assertEqual(len(self.loads), 1)

    def test_file_transcription_uses_decoder_and_does_not_write_files(self):
        engine = self.engine(cuda=False)
        engine.load()
        recording = Path(self.temp.name) / "sample.wav"
        recording.write_bytes(b"fake audio fixture")
        before = {p: p.read_bytes() for p in Path(self.temp.name).rglob("*") if p.is_file()}
        real_import = importlib.import_module
        def import_module(name):
            if name == "faster_whisper.audio":
                return SimpleNamespace(decode_audio=lambda path, sampling_rate: np.array([0.1, -0.1], dtype=np.float32))
            return real_import(name)
        with patch("joyvox_engine.importlib.import_module", side_effect=import_module):
            self.assertTrue(engine.transcribe_file(recording))
        after = {p: p.read_bytes() for p in Path(self.temp.name).rglob("*") if p.is_file()}
        self.assertEqual(before, after)


if __name__ == "__main__":
    unittest.main()
