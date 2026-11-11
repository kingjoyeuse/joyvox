"""Audio boundary and concurrency regressions; no microphone/dependencies needed."""
from array import array
from dataclasses import FrozenInstanceError
from pathlib import Path
import sys
import threading
import types
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "app"))
from joyvox_audio import AudioCapture, CaptureBuffer, _mono_pcm16


def pcm(values):
    data = array("h", values)
    if sys.byteorder != "little":
        data.byteswap()
    return data.tobytes()


def samples(data):
    out = array("h")
    out.frombytes(data)
    if sys.byteorder != "little":
        out.byteswap()
    return list(out)


class CaptureBufferTests(unittest.TestCase):
    def test_sound_guard_remains_active_through_release_padding(self):
        capture = CaptureBuffer(1000, pre_roll_ms=0, tail_ms=2)
        self.assertFalse(capture.is_capturing)
        capture.start(now=0)
        self.assertTrue(capture.is_capturing)
        capture.feed(pcm([1, 2]))
        capture.stop(now=.002)
        self.assertFalse(capture.is_recording)
        self.assertTrue(capture.is_capturing)
        capture.feed(pcm([3]))
        self.assertTrue(capture.is_capturing)
        capture.feed(pcm([4]))
        self.assertFalse(capture.is_capturing)

    def test_pre_roll_and_final_tail_are_exact(self):
        capture = CaptureBuffer(1000, pre_roll_ms=3, tail_ms=2)
        capture.feed(pcm([1, 2, 3, 4, 5]))
        self.assertEqual(capture.start("say_team", now=10), (True, ()))
        capture.feed(pcm([6, 7, 8]))
        self.assertEqual(capture.stop(now=10.003), (True, ()))
        self.assertFalse(capture.is_recording)
        self.assertEqual(capture.feed(pcm([9])), ())
        clip, = capture.feed(pcm([10, 11, 12]))
        self.assertEqual(samples(clip.pcm), [3, 4, 5, 6, 7, 8, 9, 10])
        self.assertEqual(clip.mode, "say_team")
        self.assertAlmostEqual(clip.held_seconds, .003)
        self.assertEqual(clip.sample_rate, 1000)
        with self.assertRaises(FrozenInstanceError):
            clip.mode = "say"

    def test_second_press_flushes_tail_without_audio_splicing(self):
        capture = CaptureBuffer(1000, pre_roll_ms=3, tail_ms=5)
        capture.feed(pcm([1, 2]))
        capture.start("say_team", now=0)
        capture.feed(pcm([3, 4]))
        capture.stop(now=.002)
        capture.feed(pcm([5]))
        accepted, (first,) = capture.start("say", now=.003)
        self.assertTrue(accepted)
        self.assertEqual(samples(first.pcm), [1, 2, 3, 4, 5])
        self.assertEqual(first.mode, "say_team")
        capture.feed(pcm([6, 7]))
        capture.stop(now=.005)
        second, = capture.feed(pcm([8, 9, 10, 11, 12]))
        self.assertEqual(samples(second.pcm), [6, 7, 8, 9, 10, 11, 12])
        self.assertEqual(second.mode, "say")
        self.assertEqual((first.sequence, second.sequence), (1, 2))

    def test_new_pre_roll_contains_only_unassigned_idle_samples(self):
        capture = CaptureBuffer(1000, pre_roll_ms=4, tail_ms=2)
        capture.start(now=0)
        capture.feed(pcm([1, 2]))
        capture.stop(now=.002)
        first, = capture.feed(pcm([3, 4, 5, 6]))
        capture.start(now=.006)
        capture.feed(pcm([7]))
        capture.stop(now=.007)
        second, = capture.feed(pcm([8, 9]))
        self.assertEqual(samples(first.pcm), [1, 2, 3, 4])
        self.assertEqual(samples(second.pcm), [5, 6, 7, 8, 9])

    def test_maximum_hold_stops_and_requires_release(self):
        capture = CaptureBuffer(1000, pre_roll_ms=2, tail_ms=2, max_seconds=.004)
        capture.feed(pcm([1, 2, 3]))
        capture.start(now=0)
        clip, = capture.feed(pcm([4, 5, 6, 7, 8, 9, 10, 11]))
        self.assertEqual(samples(clip.pcm), [2, 3, 4, 5, 6, 7, 8, 9])
        self.assertEqual(clip.held_seconds, .004)
        self.assertFalse(capture.is_recording)
        self.assertEqual(capture.start(now=.01), (False, ()))
        self.assertEqual(capture.stop(now=.011), (True, ()))
        self.assertTrue(capture.start(now=.012)[0])
        self.assertLessEqual(capture.buffered_bytes, (2 + 4 + 2) * 2)

    def test_idle_memory_is_bounded_even_for_large_chunks(self):
        capture = CaptureBuffer(1000, pre_roll_ms=3, tail_ms=0)
        for _ in range(100):
            capture.feed(pcm(range(1000)))
            self.assertLessEqual(capture.buffered_bytes, 6)
        capture.start(now=0)
        accepted, (clip,) = capture.stop(now=.001)
        self.assertTrue(accepted)
        self.assertEqual(samples(clip.pcm), [997, 998, 999])

    def test_minimum_held_time_does_not_include_pre_roll_or_tail(self):
        capture = CaptureBuffer(1000, pre_roll_ms=150, tail_ms=120)
        capture.feed(pcm([1] * 150))
        capture.start(now=1)
        capture.stop(now=1.01)
        clip, = capture.feed(pcm([2] * 120))
        self.assertAlmostEqual(clip.held_seconds, .01)
        self.assertEqual(len(clip.pcm), 540)

    def test_duplicate_press_release_and_discard(self):
        capture = CaptureBuffer(1000, tail_ms=1)
        self.assertEqual(capture.stop(), (False, ()))
        self.assertTrue(capture.start()[0])
        self.assertEqual(capture.start(), (False, ()))
        capture.feed(pcm([1]))
        capture.discard()
        self.assertFalse(capture.is_recording)
        self.assertEqual(capture.buffered_bytes, 0)

    def test_input_validation_and_stereo_overflow_safety(self):
        capture = CaptureBuffer(1000)
        with self.assertRaises(ValueError):
            capture.feed(b"a")
        with self.assertRaises(ValueError):
            CaptureBuffer(16000, max_seconds=float("nan"))
        self.assertEqual(samples(_mono_pcm16(
            pcm([32767, 32767, -32768, -32768, 200, -200]), 2
        )), [32767, -32768, 0])


class FakeStream:
    def __init__(self, callback):
        self.callback = callback
        self.started = False
        self.closed = False

    def start_stream(self):
        self.started = True

    def stop_stream(self):
        self.started = False

    def is_active(self):
        return self.started and not self.closed

    def close(self):
        self.closed = True


class HardwareWrapperTests(unittest.TestCase):
    def test_stream_health_check_detects_stop_without_another_callback(self):
        capture = AudioCapture(on_error=lambda message: None, log=lambda text: None)
        capture._stream = FakeStream(None)
        capture._stream.start_stream()
        capture._ready = True
        self.assertTrue(capture.ready)
        capture._stream.stop_stream()
        self.assertFalse(capture.ready)
        self.assertFalse(capture.ready)
        self.assertEqual(capture._events.qsize(), 1)
        kind, message = capture._events.get_nowait()
        capture._events.task_done()
        self.assertEqual(kind, "error")
        self.assertIn("stream stopped", message)
        capture.close()

    def test_stream_health_error_reports_unavailable_device(self):
        capture = AudioCapture(on_error=lambda message: None, log=lambda text: None)
        capture._stream = FakeStream(None)
        capture._stream.is_active = lambda: (_ for _ in ()).throw(OSError("device removed"))
        capture._ready = True
        self.assertFalse(capture.ready)
        kind, message = capture._events.get_nowait()
        capture._events.task_done()
        self.assertEqual(kind, "error")
        self.assertIn("device removed", message)
        capture.close()

    def test_wrapper_capture_state_includes_release_tail(self):
        capture = AudioCapture(on_error=lambda message: None, log=lambda text: None)
        capture._buffer = CaptureBuffer(1000, pre_roll_ms=0, tail_ms=2)
        capture._ready = True
        self.assertFalse(capture.is_capturing)
        self.assertTrue(capture.start())
        self.assertTrue(capture.is_capturing)
        capture._buffer.feed(pcm([1, 2]))
        self.assertTrue(capture.stop())
        self.assertFalse(capture.is_recording)
        self.assertTrue(capture.is_capturing)
        capture._buffer.feed(pcm([3, 4]))
        self.assertFalse(capture.is_capturing)
        capture.close()

    def test_one_stream_native_rate_fallback_and_dispatch(self):
        attempts = []
        streams = []
        errors = []
        clips = []
        delivered = threading.Event()

        class FakePortAudio:
            def get_default_input_device_info(self):
                return {"index": 7, "name": "Test mic", "maxInputChannels": 2,
                        "defaultSampleRate": 48000}

            def open(self, **kwargs):
                attempts.append((kwargs["rate"], kwargs["channels"]))
                if kwargs["rate"] != 48000:
                    raise ValueError("Unsupported sample rate")
                stream = FakeStream(kwargs["stream_callback"])
                streams.append(stream)
                return stream

            def terminate(self):
                pass

        module = types.SimpleNamespace(PyAudio=FakePortAudio, paInt16=8,
                                       paContinue=0, paAbort=2)
        def on_clip(clip):
            clips.append(clip)
            delivered.set()

        with patch.dict(sys.modules, {"pyaudio": module}):
            capture = AudioCapture(pre_roll_ms=0, tail_ms=0, on_clip=on_clip,
                                   on_error=errors.append, log=lambda text: None)
            try:
                self.assertTrue(capture.open())
                self.assertTrue(capture.ready)
                self.assertEqual(attempts, [(16000, 1), (48000, 1)])
                self.assertTrue(capture.open())
                self.assertEqual(len(streams), 1)
                self.assertTrue(capture.start("say_team"))
                streams[0].callback(pcm([1, 2]), 2, {}, 0)
                self.assertTrue(capture.stop())
                self.assertTrue(delivered.wait(1))
                self.assertEqual(samples(clips[0].pcm), [1, 2])
                self.assertEqual(clips[0].sample_rate, 48000)
                self.assertEqual(clips[0].mode, "say_team")
                self.assertTrue(capture.start())
                capture.stop()
                self.assertEqual(len(streams), 1)
                self.assertEqual(errors, [])
            finally:
                capture.close()
            self.assertFalse(capture.ready)
            self.assertTrue(streams[0].closed)
            self.assertFalse(capture.start())
            capture.close()

    def test_missing_pyaudio_is_actionable_without_importing_at_module_load(self):
        errors = []
        capture = AudioCapture(on_error=errors.append, log=lambda text: None)
        with patch("joyvox_audio.importlib.import_module", side_effect=ImportError("pyaudio")):
            self.assertFalse(capture.open())
        self.assertFalse(capture.ready)
        self.assertIn("pyaudio", errors[0])


if __name__ == "__main__":
    unittest.main()
