"""Local speech recognition for JoyVox.

Importing this module only imports Python's standard library. Heavy dependencies
and model weights are loaded explicitly by Engine.load(), normally in a worker.
No method in this module writes game commands or transmits chat.
"""
from __future__ import annotations

import gc
import importlib
import math
import os
from pathlib import Path
import re
import sys
import threading
import time


def _enabled(value: str) -> bool:
    return str(value).strip().lower() in ("1", "yes", "true", "on", "y")


class Engine:
    """One reusable faster-whisper model and a serialized recognition path."""

    def __init__(self, config: dict[str, str], dictionary: dict[str, str], log=print):
        self.config = dict(config)
        self.dictionary = dict(dictionary)
        self.log = log
        self.ready = False
        self.device = ""
        self.model_name = ""
        self.compute_type = ""
        self.load_seconds = 0.0
        self._model = None
        self._np = None
        self._ct2 = None
        self._whisper_class = None
        self._download_model = None
        self._cache_miss_errors = ()
        self._resolved_models: dict[str, str] = {}
        self._dll_handles = []
        self._cuda_library_handles = []
        self._cuda_libraries_checked = False
        self._dll_prepared = False
        self._load_lock = threading.Lock()
        self._inference_lock = threading.Lock()
        self._runtime_fallback_used = False

    def _prepare_windows_dlls(self):
        if self._dll_prepared or os.name != "nt":
            return
        self._dll_prepared = True
        configured = self.config.get("STT_CUDA_DLL_PATH", "")
        candidates = [Path(os.path.expandvars(p.strip())).expanduser()
                      for p in configured.split(";") if p.strip()]
        for folder in sys.path:
            if folder:
                for package in ("cublas", "cudnn"):
                    for leaf in ("bin", "lib"):
                        candidates.append(Path(folder) / "nvidia" / package / leaf)
        cuda_path = os.environ.get("CUDA_PATH")
        if cuda_path:
            candidates.append(Path(cuda_path) / "bin")
        # Python 3.8+ uses a safer DLL search than native LoadLibrary and may
        # ignore PATH unless those directories are explicitly registered.
        # Preserve existing CUDA/cuDNN installations that already use PATH.
        for folder in os.environ.get("PATH", "").split(os.pathsep):
            if folder and any((Path(folder) / dll).is_file() for dll in (
                "cublas64_12.dll", "cudnn64_9.dll", "cudnn_ops64_9.dll",
            )):
                candidates.append(Path(folder))
        paths = []
        seen = set()
        for candidate in candidates:
            if not candidate.is_dir():
                continue
            path = str(candidate.resolve())
            if path.casefold() in seen:
                continue
            seen.add(path.casefold())
            paths.append(path)
            if hasattr(os, "add_dll_directory"):
                self._dll_handles.append(os.add_dll_directory(path))
        if paths:
            # CT2 can load CUDA DLLs from native code after Python imports have
            # finished. Both mechanisms are needed across Python/CT2 versions.
            os.environ["PATH"] = os.pathsep.join(paths + [os.environ.get("PATH", "")])
            self.log(f"Registered {len(paths)} CUDA library folder(s) for this process.")

    def _import_dependencies(self):
        if self._whisper_class is not None:
            return
        self._prepare_windows_dlls()
        self.log("Loading speech-recognition libraries...")
        try:
            self._np = importlib.import_module("numpy")
            self._ct2 = importlib.import_module("ctranslate2")
            faster_whisper = importlib.import_module("faster_whisper")
            utils = importlib.import_module("faster_whisper.utils")
            self._whisper_class = faster_whisper.WhisperModel
            self._download_model = utils.download_model
            try:
                errors = importlib.import_module("huggingface_hub.errors")
            except ImportError:
                errors = importlib.import_module("huggingface_hub.utils")
            self._cache_miss_errors = (errors.LocalEntryNotFoundError,)
        except (ImportError, OSError, AttributeError) as exc:
            raise RuntimeError(
                "Speech-recognition dependencies could not load. Open joyvox.exe "
                "and click Install dependencies. "
                f"Details: {exc}"
            ) from exc

    @staticmethod
    def _validate_model_directory(folder: str):
        path = Path(folder)
        missing = [name for name in ("model.bin", "config.json", "tokenizer.json")
                   if not (path / name).is_file() or (path / name).stat().st_size == 0]
        if missing:
            raise FileNotFoundError(
                f"Incomplete model at {path}: missing or empty {', '.join(missing)}."
            )
        return str(path)

    def _resolve_model(self, name: str):
        if name in self._resolved_models:
            return self._resolved_models[name]
        local = Path(os.path.expandvars(name)).expanduser()
        if local.is_dir():
            resolved = self._validate_model_directory(str(local))
            self._resolved_models[name] = resolved
            return resolved
        if local.is_absolute() or name.startswith(("./", "../", ".\\", "..\\")):
            raise FileNotFoundError(f"Configured model folder does not exist: {local}")

        custom_cache = self.config.get("STT_MODEL_CACHE", "").strip() or None
        # Honor the cache folder selected in the GUI, keeping portable model files local.
        # A blank setting still reuses the standard Hugging Face cache.
        caches = [custom_cache]
        repair_cache = None
        needs_repair = False
        for cache in caches:
            try:
                path = self._download_model(name, cache_dir=cache, local_files_only=True)
            except self._cache_miss_errors:
                continue
            try:
                resolved = self._validate_model_directory(path)
            except FileNotFoundError as exc:
                self.log(str(exc))
                if not needs_repair:
                    repair_cache, needs_repair = cache, True
                continue
            self.log(f"Using cached speech model: {name}")
            self._resolved_models[name] = resolved
            return resolved

        self.log(
            f"Downloading missing speech-model files for {name}. "
            "The first download can take several minutes; later launches use the local cache."
        )
        target_cache = repair_cache if needs_repair else custom_cache
        path = self._download_model(name, cache_dir=target_cache, local_files_only=False)
        resolved = self._validate_model_directory(path)
        self._resolved_models[name] = resolved
        return resolved

    @staticmethod
    def _is_cuda_failure(exc: Exception):
        # Never turn a bad model ID, invalid option, missing cache, or connection
        # problem into a misleading "CPU fallback".
        message = str(exc).lower()
        return isinstance(exc, (RuntimeError, OSError)) and any(
            token in message for token in (
                "cuda", "cudnn", "cublas", "nvidia", "out of memory",
                "insufficient driver", "driver version", "gpu",
            )
        )

    @staticmethod
    def _cuda_help(exc):
        return (
            f"CUDA initialization failed: {exc}. "
            "faster-whisper requires CUDA 12 cuBLAS and cuDNN 9 libraries. "
            "In joyvox.exe, install dependencies with NVIDIA support, or set STT_CUDA_DLL_PATH to their DLL "
            "folders. Set STT_WHISPER_DEVICE=cpu to use the CPU explicitly."
        )

    def _model_for_device(self, device: str):
        requested = self.config.get("STT_WHISPER_MODEL", "auto").strip()
        if not requested:
            requested = "auto"
        if requested.lower() != "auto":
            return requested
        if device == "cuda":
            return "large-v3-turbo"
        return self.config.get("STT_CPU_MODEL", "small.en").strip() or "small.en"

    def _check_windows_cuda_libraries(self):
        if os.name != "nt" or self._cuda_libraries_checked:
            return
        # Some cuDNN builds abort inside native code when a companion DLL is
        # missing. Load the core/convolution libraries through Python first so
        # the usual missing-library case raises a catchable OSError instead.
        ctypes = importlib.import_module("ctypes")
        for name in (
            "cublasLt64_12.dll", "cublas64_12.dll", "cudnn64_9.dll",
            "cudnn_ops64_9.dll", "cudnn_cnn64_9.dll",
        ):
            try:
                self._cuda_library_handles.append(ctypes.WinDLL(name))
            except OSError as exc:
                raise RuntimeError(f"CUDA library {name} could not load: {exc}") from exc
        self._cuda_libraries_checked = True

    def _choose_compute_type(self, device: str, force_auto=False):
        supported = self._ct2.get_supported_compute_types(device)
        requested = self.config.get("STT_WHISPER_COMPUTE_TYPE", "auto").strip().lower()
        if force_auto:
            requested = "auto"
        if requested and requested != "auto":
            if requested not in supported:
                raise ValueError(
                    f"Compute type {requested!r} is not supported on {device}. "
                    f"Choose STT_WHISPER_COMPUTE_TYPE=auto or one of: {', '.join(sorted(supported))}."
                )
            return requested
        preferred = ("float16", "int8_float16", "float32") if device == "cuda" else ("int8", "int8_float32", "float32")
        for compute_type in preferred:
            if compute_type in supported:
                return compute_type
        raise RuntimeError(f"No suitable compute type is available on {device}: {sorted(supported)}")

    def _load_selected(self, device: str, force_auto_compute=False):
        model_name = self._model_for_device(device)
        if device == "cuda":
            self._check_windows_cuda_libraries()
        compute_type = self._choose_compute_type(device, force_auto=force_auto_compute)
        folder = self._resolve_model(model_name)
        try:
            threads = int(self.config.get("STT_CPU_THREADS", "4"))
        except ValueError as exc:
            raise ValueError("STT_CPU_THREADS must be a positive integer.") from exc
        if threads < 1:
            raise ValueError("STT_CPU_THREADS must be a positive integer.")
        self.log(f"Loading {model_name} on {device.upper()} ({compute_type})...")
        self._model = self._whisper_class(
            folder, device=device, compute_type=compute_type,
            cpu_threads=threads, num_workers=1, local_files_only=True,
        )
        if device == "cuda" or _enabled(self.config.get("STT_MODEL_WARMUP", "yes")):
            self.log("Checking CUDA inference..." if device == "cuda" else "Warming up speech recognition...")
            # CT2 loads cuDNN lazily. Consume the generator so this really tests
            # inference before reporting success. CUDA always gets this check.
            segments, _ = self._model.transcribe(
                self._np.zeros(16000, dtype=self._np.float32),
                language="en", beam_size=1, best_of=1, temperature=0.0,
                vad_filter=False, condition_on_previous_text=False,
                without_timestamps=True, max_new_tokens=1,
            )
            for _ in segments:
                pass
        self.device, self.model_name, self.compute_type = device, model_name, compute_type

    def load(self):
        """Load once, checking cached files before allowing a missing-model download.

        Raises an actionable exception if recognition cannot become ready. With
        DEVICE=auto, CUDA runtime failures fall back to CPU; an explicit CUDA
        request fails visibly. Explicit model choices are preserved on fallback.
        """
        with self._load_lock:
            if self.ready:
                return
            start = time.perf_counter()
            self._import_dependencies()
            requested = self.config.get("STT_WHISPER_DEVICE", "auto").strip().lower()
            if requested not in ("auto", "cpu", "cuda"):
                raise ValueError("STT_WHISPER_DEVICE must be auto, cpu, or cuda.")
            chosen = requested
            if requested in ("auto", "cuda"):
                try:
                    has_cuda = self._ct2.get_cuda_device_count() > 0
                except (RuntimeError, OSError) as exc:
                    if requested == "cuda":
                        raise RuntimeError(self._cuda_help(exc)) from exc
                    self.log(f"CUDA discovery failed ({exc}); using CPU.")
                    has_cuda = False
                if not has_cuda and requested == "cuda":
                    raise RuntimeError(self._cuda_help("No NVIDIA CUDA device is visible"))
                chosen = "cuda" if has_cuda else "cpu"
                if not has_cuda:
                    self.log("No usable CUDA device detected; using CPU recognition.")
            try:
                self._load_selected(chosen)
            except Exception as exc:
                self._model = None
                gc.collect()
                if chosen != "cuda" or not self._is_cuda_failure(exc):
                    raise
                if requested != "auto":
                    raise RuntimeError(self._cuda_help(exc)) from exc
                self.log(f"CUDA unavailable: {exc}")
                self.log("Falling back to CPU recognition (INT8 when supported).")
                self._load_selected("cpu", force_auto_compute=True)
            self.load_seconds = time.perf_counter() - start
            self.ready = True
            self.log(f"Speech model ready: {self.model_name}, {self.device.upper()}, {self.compute_type} ({self.load_seconds:.2f}s).")

    def _bounded_hint(self, text: str, token_limit=96):
        text = " ".join(text.split())[:600]
        tokenizer = getattr(self._model, "hf_tokenizer", None)
        if tokenizer is not None:
            encoded = tokenizer.encode(text)
            ids = getattr(encoded, "ids", encoded)
            if len(ids) > token_limit:
                text = tokenizer.decode(ids[:token_limit])
        else:
            text = text[:384]
        return text.strip(" ,;") or None

    def _hints(self):
        custom_prompt = self.config.get("STT_PROMPT", "").strip()
        terms = re.split(r"[,;\n]", self.config.get("STT_HOTWORDS", ""))
        terms.extend(self.dictionary.values())
        unique = []
        seen = set()
        for term in terms:
            term = " ".join(str(term).strip().strip('"').split())
            if term and term.casefold() not in seen:
                unique.append(term)
                seen.add(term.casefold())
            if len(unique) >= 32:
                break
        return self._bounded_hint(custom_prompt), self._bounded_hint(", ".join(unique))

    def _decode(self, audio):
        prompt, hotwords = self._hints()
        try:
            beam_size = int(self.config.get("STT_BEAM_SIZE", "5"))
        except ValueError as exc:
            raise ValueError("STT_BEAM_SIZE must be an integer between 1 and 20.") from exc
        if not 1 <= beam_size <= 20:
            raise ValueError("STT_BEAM_SIZE must be between 1 and 20.")
        language = self.config.get("STT_LANGUAGE", "en").strip().lower()
        if language in ("", "auto"):
            language = None
        segments, _ = self._model.transcribe(
            audio, language=language, beam_size=beam_size, best_of=1,
            temperature=0.0, condition_on_previous_text=False,
            initial_prompt=prompt, hotwords=hotwords,
            vad_filter=_enabled(self.config.get("STT_VAD_FILTER", "yes")),
            vad_parameters={
                "threshold": 0.35, "min_speech_duration_ms": 100,
                "min_silence_duration_ms": 500, "speech_pad_ms": 250,
            },
            word_timestamps=False,
        )
        return " ".join(segment.text.strip() for segment in segments if segment.text.strip()).strip()

    def _transcribe_array(self, audio):
        if audio.size == 0:
            return ""
        if not self._np.isfinite(audio).all():
            raise ValueError("Audio contains invalid (NaN/infinite) samples.")
        peak = float(self._np.max(self._np.abs(audio)))
        if peak == 0.0:
            self.log("No audio signal (digital silence); nothing to transcribe.")
            return ""
        rms = float(self._np.sqrt(self._np.mean(audio.astype(self._np.float64) ** 2)))
        if rms and 20.0 * math.log10(rms) < -45.0:
            self.log("Microphone signal is very quiet; check the selected input and microphone level.")
        if float(self._np.mean(self._np.abs(audio) >= 0.999)) > 0.001:
            self.log("Microphone audio is clipping; lower the microphone gain for clearer recognition.")
        # No amplitude normalization/noise gate: quiet speech must not be lost,
        # and background noise must not be amplified to speech level.
        try:
            return self._decode(audio)
        except Exception as exc:
            if not (
                self.config.get("STT_WHISPER_DEVICE", "auto").strip().lower() == "auto"
                and self.device == "cuda" and not self._runtime_fallback_used
                and self._is_cuda_failure(exc)
            ):
                raise
            self._runtime_fallback_used = True
            self.ready = False
            self.log(f"CUDA failed during recognition: {exc}. Retrying this utterance once on CPU.")
            self._model = None
            gc.collect()
            with self._load_lock:
                self._load_selected("cpu", force_auto_compute=True)
                self.ready = True
            return self._decode(audio)

    def transcribe(self, pcm: bytes, sample_rate: int = 16000) -> str:
        """Transcribe mono little-endian signed 16-bit PCM; never sends text."""
        if not self.ready:
            raise RuntimeError("Speech recognition is not ready; call Engine.load() first.")
        if len(pcm) % 2:
            raise ValueError("16-bit PCM audio must contain an even number of bytes.")
        if not isinstance(sample_rate, int) or isinstance(sample_rate, bool) or sample_rate <= 0:
            raise ValueError("Audio sample rate must be a positive integer.")
        with self._inference_lock:
            audio = self._np.frombuffer(pcm, dtype="<i2").astype(self._np.float32) / 32768.0
            if sample_rate != 16000 and audio.size:
                try:
                    resample_poly = importlib.import_module("scipy.signal").resample_poly
                except ImportError as exc:
                    raise RuntimeError("Resampling needs scipy. Click Install dependencies in joyvox.exe.") from exc
                divisor = math.gcd(sample_rate, 16000)
                audio = resample_poly(audio, 16000 // divisor, sample_rate // divisor).astype(self._np.float32)
            return self._transcribe_array(self._np.ascontiguousarray(audio))

    def transcribe_file(self, path) -> str:
        """Transcribe a local recording for testing, without microphone/game I/O."""
        if not self.ready:
            raise RuntimeError("Speech recognition is not ready; call Engine.load() first.")
        if not Path(path).is_file():
            raise FileNotFoundError(f"Audio file does not exist: {path}")
        with self._inference_lock:
            decode_audio = importlib.import_module("faster_whisper.audio").decode_audio
            audio = decode_audio(str(path), sampling_rate=16000)
            return self._transcribe_array(self._np.ascontiguousarray(audio, dtype=self._np.float32))
