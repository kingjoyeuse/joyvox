"""Read-only runtime inspection for JoyVox's native Windows launcher.

No microphone stream, keyboard hook, speech model, or network request is started.
Dependency failures are reported as data; a completed check exits successfully.
"""
from __future__ import annotations

import argparse
from contextlib import redirect_stderr, redirect_stdout
import importlib
import importlib.metadata
import inspect
import io
import json
import os
from pathlib import Path
import re
import struct
import sys
import wave


def clean_message(value):
    text = " ".join(str(value).split())
    text = re.sub(r"(https?://)[^/@\s]+:[^/@\s]+@", r"\1[credentials]@", text)
    return text[:1200]


def python_status():
    supported = (
        sys.platform == "win32" and sys.implementation.name == "cpython"
        and (3, 10) <= sys.version_info[:2] <= (3, 12)
        and struct.calcsize("P") == 8
    )
    return {
        "python": "ready" if supported else "error",
        "core": "missing", "gpu": "missing",
        "python_exe": str(Path(sys.executable).resolve()),
        "python_version": ".".join(map(str, sys.version_info[:3])),
        "python_prefix": str(Path(sys.prefix).resolve()),
        "python_base_prefix": str(Path(sys.base_prefix).resolve()),
        "summary": "Python is ready." if supported else "JoyVox needs 64-bit CPython 3.10-3.12 on Windows.",
        "diagnostics": [], "versions": {}, "gpu_libraries": "missing",
        "cuda_devices": 0,
    }


def application_directory(root):
    for path in (root / ".joyvox" / "app", root / "app"):
        if (path / "joyvox_engine.py").is_file():
            return path
    raise FileNotFoundError("JoyVox backend files are missing. Restart JoyVox.exe to restore its private support files.")


def cuda_config(root):
    # Dependency checks must still work when unrelated game settings are wrong.
    values = {"STT_CUDA_DLL_PATH": ""}
    path = root / "joyvox_config.cfg"
    if path.is_file():
        for line in path.read_text(encoding="utf-8-sig").splitlines():
            line = line.strip()
            if line.startswith(("#", "//")) or "=" not in line:
                continue
            key, value = line.split("=", 1)
            if key.strip() == "STT_CUDA_DLL_PATH":
                values[key.strip()] = re.split(r"\s+//", value, maxsplit=1)[0].strip()
    return values


def inspect_dependencies(root):
    result = python_status()
    if result["python"] != "ready":
        return result
    messages = result["diagnostics"]
    try:
        app_dir = application_directory(root)
        sys.path.insert(0, str(app_dir))
        engine_class = importlib.import_module("joyvox_engine").Engine
        engine = engine_class(cuda_config(root), {}, log=lambda text: messages.append(clean_message(text)))
        engine._prepare_windows_dlls()
    except Exception as exc:
        result.update(core="error", summary=clean_message(exc))
        messages.append(clean_message(exc))
        return result

    packages = (
        ("numpy", "numpy"), ("scipy", "scipy.signal"),
        ("ctranslate2", "ctranslate2"), ("av", "av"),
        ("faster-whisper", "faster_whisper"), ("onnxruntime", "onnxruntime"),
        ("PyAudio", "pyaudio"), ("pynput", "pynput"),
        ("pygame", "pygame.mixer"),
    )
    imported = {}
    failures = []
    for package, module in packages:
        try:
            imported[module] = importlib.import_module(module)
            version = importlib.metadata.version(package)
            result["versions"][package] = version
            messages.append(f"OK: {package} {version} imports successfully.")
        except ModuleNotFoundError as exc:
            failures.append("missing")
            messages.append(f"MISSING: {package}: {clean_message(exc)}")
        except Exception as exc:
            failures.append("error")
            messages.append(f"ERROR: {package}: {clean_message(exc)}")

    if all(name in imported for name in ("numpy", "av", "faster_whisper", "scipy.signal")):
        try:
            # Exercise the same PyAV interface as file transcription. A package
            # can import successfully yet expose an incompatible API (PyAV 19).
            audio_file = io.BytesIO()
            with wave.open(audio_file, "wb") as wav:
                wav.setnchannels(1)
                wav.setsampwidth(2)
                wav.setframerate(16000)
                wav.writeframes(bytes(3200))
            audio_file.seek(0)
            decoder = importlib.import_module("faster_whisper.audio").decode_audio
            audio = decoder(audio_file, sampling_rate=16000)
            if audio.shape != (1600,):
                raise RuntimeError("Audio decoding returned an unexpected waveform.")
            imported["scipy.signal"].resample_poly(audio, 1, 3)
            arguments = inspect.signature(imported["faster_whisper"].WhisperModel.transcribe).parameters
            if not {"hotwords", "vad_parameters", "condition_on_previous_text"} <= set(arguments):
                raise RuntimeError("The installed faster-whisper API is incompatible with this JoyVox release.")
            vad_options = importlib.import_module("faster_whisper.vad").VadOptions
            vad_options(threshold=0.35, min_speech_duration_ms=100,
                        min_silence_duration_ms=500, speech_pad_ms=250)
            messages.append("OK: audio decoding, resampling, and recognition options are compatible. No speech model loaded.")
        except Exception as exc:
            failures.append("error")
            messages.append(f"ERROR: audio/recognition API check: {clean_message(exc)}")

    result["core"] = "error" if "error" in failures else ("missing" if failures else "ready")
    if "ctranslate2" in imported:
        try:
            engine._check_windows_cuda_libraries()
            result["gpu_libraries"] = "ready"
            messages.append("OK: CUDA 12 cuBLAS and cuDNN 9 DLLs load successfully.")
            count = imported["ctranslate2"].get_cuda_device_count()
            result["cuda_devices"] = count
            if count > 0:
                compute_types = sorted(imported["ctranslate2"].get_supported_compute_types("cuda"))
                if not compute_types:
                    raise RuntimeError("The NVIDIA device reports no usable compute types.")
                result["gpu"] = "ready"
                messages.append(f"OK: {count} CUDA device(s) detected; supported types: {', '.join(compute_types)}.")
                messages.append("GPU inference is checked when recognition starts; dependency status does not load a speech model.")
            else:
                result["gpu"] = "missing"
                messages.append("OPTIONAL: NVIDIA libraries load, but no CUDA device is visible. CPU recognition remains available.")
        except Exception as exc:
            cause = exc
            while getattr(cause, "__cause__", None) is not None:
                cause = cause.__cause__
            state = "missing" if getattr(cause, "winerror", None) == 126 else "error"
            # Missing DLLs can also be reported without a Windows error code.
            if any(term in str(exc).lower() for term in ("could not load", "not found", "cannot find")):
                state = "missing" if getattr(cause, "winerror", None) in (None, 126) else state
            result["gpu"] = state
            if result["gpu_libraries"] != "ready":
                result["gpu_libraries"] = state
            messages.append(f"OPTIONAL NVIDIA support: {clean_message(exc)}. CPU recognition does not require these libraries.")

    if result["core"] == "ready":
        if result["gpu"] == "ready":
            result["summary"] = "Dependencies ready. CPU and NVIDIA libraries checked; the speech model loads when you click Start."
        else:
            result["summary"] = "Dependencies ready for CPU recognition. Optional NVIDIA support is unavailable; see the details below."
    else:
        result["summary"] = "Some recognition dependencies are missing or could not load. Click Install dependencies to create or repair JoyVox's isolated runtime."
    return result


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", required=True, type=Path)
    parser.add_argument("--json", action="store_true")
    parser.add_argument("--python-only", action="store_true", help=argparse.SUPPRESS)
    args = parser.parse_args(argv)
    if not args.root.is_absolute() or not args.root.is_dir():
        parser.error("--root must be an existing absolute folder")
    sys.dont_write_bytecode = True
    os.environ.setdefault("PYGAME_HIDE_SUPPORT_PROMPT", "1")
    captured = io.StringIO()
    with redirect_stdout(captured), redirect_stderr(captured):
        result = python_status() if args.python_only else inspect_dependencies(args.root.resolve())
    extra = clean_message(captured.getvalue())
    if extra:
        result["diagnostics"].append(extra)
    if args.json:
        print(json.dumps(result, ensure_ascii=True))
    else:
        for key in ("python", "core", "gpu", "python_exe", "summary"):
            print(f"{key}={clean_message(result[key])}")
        for message in result["diagnostics"]:
            print(f"detail={clean_message(message)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
