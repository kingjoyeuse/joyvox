"""Persistent, bounded microphone capture for JoyVox.

Only the Python standard library is imported until ``open`` or ``list_devices``.
Idle microphone audio stays in a small RAM pre-roll; it is never written to disk.
``CaptureBuffer`` is the hardware-independent, thread-safe PCM16 capture core.
"""
from __future__ import annotations

from array import array
from collections import deque
from dataclasses import dataclass
import importlib
import math
import queue
import sys
import threading
import time
from typing import Callable, Optional


@dataclass(frozen=True)
class CapturedAudio:
    """An immutable mono, little-endian signed PCM16 utterance."""

    pcm: bytes
    sample_rate: int
    mode: str
    held_seconds: float
    sequence: int


class CaptureBuffer:
    """Capture mono PCM16 without audio hardware or third-party dependencies.

    ``start`` and ``stop`` return ``(accepted, completed_clips)``; ``feed``
    returns completed clips. Callbacks must run after these methods release
    their lock. The sample clock bounds memory and the monotonic clock measures
    how long PTT was actually held, excluding pre-roll and release padding.
    """

    def __init__(self, sample_rate: int, pre_roll_ms: float = 150,
                 tail_ms: float = 120, max_seconds: float = 30,
                 clock: Callable[[], float] = time.monotonic):
        if not isinstance(sample_rate, int) or not 1 <= sample_rate <= 384000:
            raise ValueError("sample_rate must be an integer from 1 to 384000")
        for name, value, low, high in (
            ("pre_roll_ms", pre_roll_ms, 0, 2000),
            ("tail_ms", tail_ms, 0, 2000),
            ("max_seconds", max_seconds, 0.000001, 300),
        ):
            if not math.isfinite(float(value)) or not low <= float(value) <= high:
                raise ValueError(f"{name} must be between {low} and {high}")
        self.sample_rate = sample_rate
        self.max_seconds = float(max_seconds)
        self._pre_bytes = round(sample_rate * float(pre_roll_ms) / 1000) * 2
        self._tail_bytes = round(sample_rate * float(tail_ms) / 1000) * 2
        self._max_bytes = max(2, round(sample_rate * self.max_seconds) * 2)
        self._clock = clock
        self._lock = threading.Lock()
        self._ring = deque()
        self._ring_bytes = 0
        self._parts = []
        self._state = "idle"
        self._waiting_release = False
        self._sequence = 0
        self._held_bytes = 0
        self._remaining_tail = 0
        self._started_at = 0.0
        self._held_seconds = 0.0
        self._mode = "say"

    @property
    def is_recording(self) -> bool:
        with self._lock:
            return self._state == "held"

    @property
    def is_capturing(self) -> bool:
        """True while held or collecting release padding (for sound gating)."""
        with self._lock:
            return self._state in ("held", "tail")

    @property
    def buffered_bytes(self) -> int:
        """Retained audio bytes, exposed for diagnostics and boundedness tests."""
        with self._lock:
            return self._ring_bytes + sum(map(len, self._parts))

    def _remember(self, pcm: bytes) -> None:
        if not self._pre_bytes or not pcm:
            return
        # Discard the front of an oversized callback before retaining it.
        pcm = pcm[-self._pre_bytes:]
        self._ring.append(pcm)
        self._ring_bytes += len(pcm)
        excess = self._ring_bytes - self._pre_bytes
        while excess > 0:
            first = self._ring.popleft()
            removed = min(excess, len(first))
            if removed < len(first):
                self._ring.appendleft(first[removed:])
            self._ring_bytes -= removed
            excess -= removed

    def _finish(self) -> CapturedAudio:
        clip = CapturedAudio(
            pcm=b"".join(self._parts), sample_rate=self.sample_rate,
            mode=self._mode, held_seconds=self._held_seconds,
            sequence=self._sequence,
        )
        self._parts = []
        self._state = "idle"
        self._remaining_tail = 0
        # Already-assigned audio must not become the next utterance's pre-roll.
        self._ring.clear()
        self._ring_bytes = 0
        return clip

    def start(self, mode: str = "say", now: Optional[float] = None):
        if mode not in ("say", "say_team"):
            raise ValueError("mode must be 'say' or 'say_team'")
        with self._lock:
            if self._state == "held" or self._waiting_release:
                return False, ()
            completed = ()
            if self._state == "tail":
                # A rapid second press owns all subsequent samples. The first
                # clip retains only the release padding already received.
                completed = (self._finish(),)
            self._sequence += 1
            self._mode = mode
            self._started_at = self._clock() if now is None else now
            self._held_seconds = 0.0
            self._held_bytes = 0
            self._parts = list(self._ring)
            self._ring.clear()
            self._ring_bytes = 0
            self._state = "held"
            return True, completed

    def stop(self, now: Optional[float] = None):
        with self._lock:
            if self._waiting_release:
                self._waiting_release = False
                return True, ()
            if self._state != "held":
                return False, ()
            stopped_at = self._clock() if now is None else now
            self._held_seconds = min(
                self.max_seconds, max(0.0, stopped_at - self._started_at)
            )
            self._state = "tail"
            self._remaining_tail = self._tail_bytes
            return True, (() if self._remaining_tail else (self._finish(),))

    def feed(self, pcm: bytes):
        if len(pcm) % 2:
            raise ValueError("PCM16 input must contain whole two-byte samples")
        if not pcm:
            return ()
        pcm = bytes(pcm)
        completed = []
        with self._lock:
            offset = 0
            if self._state == "held":
                count = min(len(pcm), self._max_bytes - self._held_bytes)
                if count:
                    self._parts.append(pcm[:count])
                    self._held_bytes += count
                    offset += count
                if self._held_bytes >= self._max_bytes:
                    self._held_seconds = self.max_seconds
                    self._waiting_release = True
                    self._state = "tail"
                    self._remaining_tail = self._tail_bytes
            if self._state == "tail":
                count = min(len(pcm) - offset, self._remaining_tail)
                if count:
                    self._parts.append(pcm[offset:offset + count])
                    self._remaining_tail -= count
                    offset += count
                if not self._remaining_tail:
                    completed.append(self._finish())
            if self._state == "idle" and offset < len(pcm):
                self._remember(pcm[offset:])
        return tuple(completed)

    def discard(self) -> None:
        """Drop current/idle audio without producing a partial transcription."""
        with self._lock:
            self._parts.clear()
            self._ring.clear()
            self._ring_bytes = 0
            self._state = "idle"
            self._waiting_release = False
            self._remaining_tail = 0


def _mono_pcm16(pcm: bytes, channels: int) -> bytes:
    """Downmix interleaved PCM16 stereo with bounded, overflow-safe integers."""
    if channels == 1:
        if len(pcm) % 2:
            raise ValueError("Microphone returned an incomplete PCM16 sample")
        return pcm
    if channels != 2 or len(pcm) % 4:
        raise ValueError("Microphone returned an incomplete stereo PCM16 frame")
    samples = array("h")
    samples.frombytes(pcm)
    if sys.byteorder != "little":
        samples.byteswap()
    mono = array("h", ((int(samples[i]) + int(samples[i + 1])) // 2
                       for i in range(0, len(samples), 2)))
    if sys.byteorder != "little":
        mono.byteswap()
    return mono.tobytes()


def list_devices():
    """Return input device dictionaries; imports PyAudio only when requested."""
    pyaudio = importlib.import_module("pyaudio")
    audio = pyaudio.PyAudio()
    try:
        devices = []
        for index in range(audio.get_device_count()):
            info = dict(audio.get_device_info_by_index(index))
            if info.get("maxInputChannels", 0) > 0:
                try:
                    info["host_api_name"] = str(audio.get_host_api_info_by_index(
                        int(info.get("hostApi", 0))).get("name", ""))
                except Exception:
                    info["host_api_name"] = ""
                devices.append(info)
        return devices
    finally:
        audio.terminate()


def resolve_input_device(audio, selection):
    """Resolve an index or Windows-friendly name; never substitute another mic.

    Named selections let the native GUI show inputs before PyAudio is installed.
    Prefer an exact WASAPI name; an unambiguous MME truncated name is a fallback.
    """
    text = str(selection).strip()
    if text.casefold() == "auto":
        return audio.get_default_input_device_info()
    if not text.casefold().startswith("name:"):
        return audio.get_device_info_by_index(int(text))
    wanted = text[5:].strip().casefold()
    if not wanted:
        raise ValueError("Select a microphone name or use auto.")
    exact, truncated = [], []
    for index in range(audio.get_device_count()):
        item = dict(audio.get_device_info_by_index(index))
        if item.get("maxInputChannels", 0) < 1:
            continue
        name = str(item.get("name", "")).strip().casefold()
        try:
            host = str(audio.get_host_api_info_by_index(int(item.get("hostApi", 0))).get("name", ""))
        except Exception:
            host = ""
        score = 0 if "wasapi" in host.casefold() else 1
        if name == wanted:
            exact.append((score, index, item))
        elif len(name) >= 20 and (wanted.startswith(name) or name.startswith(wanted)):
            truncated.append((score, index, item))
    matches = exact or truncated
    if not matches:
        raise RuntimeError("Selected microphone is unavailable: " + text[5:] + ". Refresh microphones and select an available input.")
    matches.sort(key=lambda row: (row[0], row[1]))
    if len(matches) > 1 and matches[0][0] == matches[1][0]:
        raise RuntimeError("More than one input matches this microphone name. Refresh microphones and select a numbered input to disambiguate.")
    return matches[0][2]


class AudioCapture:
    """One persistent PortAudio stream, a pre-roll, and immutable PTT clips.

    ``open`` returns success and reports actionable failures through ``on_error``.
    ``on_clip`` and asynchronous ``on_error`` callbacks run on a dispatcher
    thread; they should enqueue work and return promptly. The dispatcher queue
    is bounded. No model inference, disk write, or logging runs in PortAudio's
    callback. ``close`` discards unfinished audio instead of sending it.
    """

    def __init__(self, device="auto", pre_roll_ms=150, tail_ms=120,
                 max_seconds=30, on_clip=None, on_error=None, log=print):
        # Validate settings without importing or touching the audio device.
        CaptureBuffer(16000, pre_roll_ms, tail_ms, max_seconds)
        self.device = device
        self.pre_roll_ms = pre_roll_ms
        self.tail_ms = tail_ms
        self.max_seconds = max_seconds
        self.on_clip = on_clip or (lambda clip: None)
        self.on_error = on_error or (lambda message: log(message))
        self.log = log
        self.sample_rate = 16000
        self.channels = 1
        self.device_name = ""
        self._audio = None
        self._stream = None
        self._pyaudio = None
        self._buffer = None
        self._ready = False
        self._lifecycle_lock = threading.RLock()
        # Serialize state transitions AND their completed-clip enqueue, so a
        # callback cannot deliver a newer clip before a rapid press flushes an
        # older clip. Do not use the lifecycle lock inside PortAudio callbacks:
        # close() waits for those callbacks while stopping the stream.
        self._capture_lock = threading.Lock()
        self._events = queue.Queue(maxsize=16)
        self._dispatch_stop = threading.Event()
        self._dispatcher = None
        self._overflow_notice = threading.Event()
        self._status_reported = False

    @property
    def ready(self) -> bool:
        # PortAudio can stop without invoking another callback, e.g. when the
        # input device disappears. Poll its lightweight stream-state API from
        # the controlling thread instead of continuing to report stale READY.
        with self._lifecycle_lock:
            if not self._ready:
                return False
            try:
                active = getattr(self._stream, "is_active", None)
                if self._stream is None or (active is not None and not active()):
                    self._ready = False
                    self._enqueue("error", "Microphone stream stopped. Check the "
                                  "selected device and restart JoyVox.")
            except Exception as exc:
                self._ready = False
                self._enqueue("error", f"Microphone stream is unavailable: {exc}. "
                              "Check the selected device and restart JoyVox.")
            return self._ready

    @property
    def is_recording(self) -> bool:
        buffer = self._buffer
        return bool(self._ready and buffer and buffer.is_recording)

    @property
    def is_capturing(self) -> bool:
        buffer = self._buffer
        return bool(self._ready and buffer and buffer.is_capturing)

    def _enqueue(self, kind, value) -> None:
        try:
            self._events.put_nowait((kind, value))
        except queue.Full:
            self._overflow_notice.set()

    def _dispatch(self) -> None:
        while not self._dispatch_stop.is_set():
            if self._overflow_notice.is_set():
                self._overflow_notice.clear()
                try:
                    self.on_error("Audio delivery queue is full; a clip or status "
                                  "update was dropped. Wait for transcription.")
                except Exception:
                    pass
            try:
                kind, value = self._events.get(timeout=0.1)
            except queue.Empty:
                continue
            try:
                if self._dispatch_stop.is_set():
                    continue
                if kind == "clip":
                    self.on_clip(value)
                else:
                    self.on_error(value)
            except Exception as exc:
                try:
                    self.on_error(f"Audio delivery callback failed: {exc}")
                except Exception:
                    pass
            finally:
                self._events.task_done()

    def _audio_callback(self, in_data, frame_count, time_info, status_flags):
        try:
            if status_flags and not self._status_reported:
                self._status_reported = True
                self._enqueue("error", "Microphone reported an audio overflow or "
                              "stream warning; some speech may be missing.")
            with self._capture_lock:
                buffer = self._buffer
                if buffer is not None and in_data:
                    for clip in buffer.feed(_mono_pcm16(in_data, self.channels)):
                        self._enqueue("clip", clip)
            return None, self._pyaudio.paContinue
        except Exception as exc:
            self._ready = False
            self._enqueue("error", f"Microphone capture stopped: {exc}. Restart JoyVox.")
            return None, self._pyaudio.paAbort

    def open(self) -> bool:
        with self._lifecycle_lock:
            if self.ready:
                return True
            if self._stream is not None or self._audio is not None:
                self.close()
            try:
                self._pyaudio = importlib.import_module("pyaudio")
                self._audio = self._pyaudio.PyAudio()
                info = resolve_input_device(self._audio, self.device)
                device_index = int(info["index"])
                max_channels = int(info.get("maxInputChannels", 0))
                if max_channels < 1:
                    raise RuntimeError("The selected audio device has no microphone inputs")
                self.device_name = str(info.get("name", device_index))
                native_rate = int(round(float(info.get("defaultSampleRate", 16000))))
                rates = [16000]
                if native_rate not in rates and 1 <= native_rate <= 384000:
                    rates.append(native_rate)
                formats = [(rate, channels)
                           for channels in ([1, 2] if max_channels >= 2 else [1])
                           for rate in rates]
                failures = []
                for rate, channels in formats:
                    try:
                        self._stream = self._audio.open(
                            format=self._pyaudio.paInt16, channels=channels,
                            rate=rate, input=True, input_device_index=device_index,
                            frames_per_buffer=max(64, round(rate * 0.02)),
                            stream_callback=self._audio_callback, start=False,
                        )
                        self.sample_rate = rate
                        self.channels = channels
                        break
                    except Exception as exc:
                        failures.append(f"{rate} Hz / {channels} channel(s): {exc}")
                if self._stream is None:
                    raise RuntimeError("Cannot open selected microphone. " + " | ".join(failures))
                self._buffer = CaptureBuffer(
                    self.sample_rate, self.pre_roll_ms, self.tail_ms, self.max_seconds
                )
                self._status_reported = False
                self._dispatch_stop.clear()
                self._dispatcher = threading.Thread(
                    target=self._dispatch, name="JoyVox-audio-delivery", daemon=True
                )
                self._dispatcher.start()
                self._ready = True
                self._stream.start_stream()
                if not self._ready:
                    raise RuntimeError("The microphone stopped during startup")
                self.log(f"Microphone ready: {self.device_name} "
                         f"({self.sample_rate} Hz, {self.channels} input channel(s)); "
                         f"{self.pre_roll_ms} ms pre-roll, {self.tail_ms} ms release padding.")
                return True
            except Exception as exc:
                self.close()
                self.on_error(f"Could not start microphone capture: {exc}. "
                              "Check STT_AUDIO_INPUT_INDEX and Windows microphone access.")
                return False

    def start(self, mode="say") -> bool:
        with self._lifecycle_lock:
            if not self._ready or self._buffer is None:
                return False
            with self._capture_lock:
                accepted, clips = self._buffer.start(mode)
                for clip in clips:
                    self._enqueue("clip", clip)
            return accepted

    def stop(self) -> bool:
        with self._lifecycle_lock:
            if not self._ready or self._buffer is None:
                return False
            with self._capture_lock:
                accepted, clips = self._buffer.stop()
                for clip in clips:
                    self._enqueue("clip", clip)
            return accepted

    def close(self) -> None:
        with self._lifecycle_lock:
            self._ready = False
            self._dispatch_stop.set()
            stream, self._stream = self._stream, None
            if stream is not None:
                try:
                    stream.stop_stream()
                except Exception:
                    pass
                try:
                    stream.close()
                except Exception:
                    pass
            if self._buffer is not None:
                self._buffer.discard()
                self._buffer = None
            audio, self._audio = self._audio, None
            if audio is not None:
                try:
                    audio.terminate()
                except Exception:
                    pass
            dispatcher, self._dispatcher = self._dispatcher, None
            if dispatcher is not None and dispatcher is not threading.current_thread():
                dispatcher.join(timeout=1)
            while True:
                try:
                    self._events.get_nowait()
                    self._events.task_done()
                except queue.Empty:
                    break
            self._overflow_notice.clear()
