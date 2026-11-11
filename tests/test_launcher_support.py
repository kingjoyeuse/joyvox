"""Focused checks for the native launcher's new backend integration points."""
from contextlib import redirect_stdout
import io
import json
from pathlib import Path
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'app'))
import joyvox
from joyvox_audio import resolve_input_device


class Inputs:
    def __init__(self, devices):
        self.devices = devices
    def get_device_count(self):
        return len(self.devices)
    def get_device_info_by_index(self, i):
        return self.devices[i]
    def get_default_input_device_info(self):
        return self.devices[0]
    def get_host_api_info_by_index(self, i):
        return {'name': ('MME', 'Windows WASAPI')[i]}


def mic(index, name, host=0, channels=1):
    return dict(index=index, name=name, hostApi=host, maxInputChannels=channels)


class LauncherSupportTests(unittest.TestCase):
    def test_default_and_numbered_inputs(self):
        devices = Inputs([mic(0, 'USB microphone'), mic(1, 'Headset')])
        self.assertEqual(resolve_input_device(devices, 'auto')['index'], 0)
        self.assertEqual(resolve_input_device(devices, '1')['name'], 'Headset')

    def test_named_input_prefers_exact_wasapi(self):
        devices = Inputs([mic(0, 'USB microphone'), mic(1, 'USB microphone', host=1)])
        self.assertEqual(resolve_input_device(devices, 'name:USB MICROPHONE')['index'], 1)

    def test_truncated_name_fallback_is_unambiguous(self):
        devices = Inputs([mic(0, 'Microphone (Realtek High Definit')])
        self.assertEqual(resolve_input_device(devices, 'name:Microphone (Realtek High Definition Audio)')['index'], 0)

    def test_missing_name_does_not_substitute_another_microphone(self):
        with self.assertRaisesRegex(RuntimeError, 'unavailable'):
            resolve_input_device(Inputs([mic(0, 'Other mic')]), 'name:Missing headset')

    def test_duplicate_names_require_numbered_input(self):
        with self.assertRaisesRegex(RuntimeError, 'More than one'):
            resolve_input_device(Inputs([mic(0, 'Headset', 1), mic(1, 'Headset', 1)]), 'name:Headset')

    def test_output_only_device_is_not_selected(self):
        with self.assertRaisesRegex(RuntimeError, 'unavailable'):
            resolve_input_device(Inputs([mic(0, 'Speakers', channels=0)]), 'name:Speakers')

    def test_device_json_includes_name_and_host_without_loading_model(self):
        data = [dict(index=3, name='USB microphone', host_api_name='Windows WASAPI')]
        output = io.StringIO()
        with patch('joyvox_audio.list_devices', return_value=data), redirect_stdout(output):
            self.assertEqual(joyvox.main(['--list-devices-json']), 0)
        self.assertEqual(json.loads(output.getvalue()), [dict(index=3, name='USB microphone', host_api='Windows WASAPI')])

    def test_initialize_game_uses_custom_filename_without_opening_audio(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            config = root / 'settings.cfg'
            config.write_text(f'STT_OUTPUT_PATH={root}\nSTT_EXEC_FILE=custom-bindings.cfg\n', encoding='utf-8')
            with patch('joyvox.JoyVox', side_effect=AssertionError('must not start the recognizer')), redirect_stdout(io.StringIO()):
                self.assertEqual(joyvox.main(['--config', str(config), '--initialize-game']), 0)
            self.assertIn('exec custom-bindings.cfg', (root / 'custom-bindings.cfg').read_text())
            self.assertFalse((root / 'joyvox.cfg').exists())


if __name__ == '__main__':
    unittest.main()
