"""Text, config, console-output and atomic-write regression tests."""
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "app"))
from joyvox_core import (
    DEFAULT_CONFIG, EMPTY_ALIASES, DictionaryCorrector, GameFiles,
    build_output, load_config, load_dictionary,
    post_process_text, sanitize_game_text, split_message, validate_config,
)


class DictionaryTests(unittest.TestCase):
    def test_phrase_case_spacing_punctuation_and_word_boundaries(self):
        correct = DictionaryCorrector({"boiler room": "The Boiler Room", "l4": "Elfor"})
        self.assertEqual(
            correct.apply('Meet in (BOILER\t ROOM), L4! Ignore xl4, l40 and boiler rooms.'),
            'Meet in (The Boiler Room), Elfor! Ignore xl4, l40 and boiler rooms.',
        )

    def test_longest_phrase_wins_and_replacements_do_not_cascade(self):
        correct = DictionaryCorrector({"boiler": "heater", "boiler room": "base",
                                       "l4": "Elfor", "elfor": "Joy"})
        self.assertEqual(correct.apply("boiler room, boiler, l4, Elfor"),
                         "base, heater, Elfor, Joy")

    def test_dictionary_loading_dequotes_and_deduplicates(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "dictionary.cfg"
            path.write_text('L4=Elfor\nl4=Elfor\nboiler room="boiler room"\n'
                            "base='home base'\n // comment\nempty=\n", encoding="utf-8")
            dictionary = load_dictionary(path)
            self.assertEqual(dictionary, {"l4": "Elfor", "boiler room": "boiler room",
                                          "base": "home base", "empty": ""})
            self.assertEqual(DictionaryCorrector(dictionary).apply('BASE and BOILER ROOM'),
                             'home base and boiler room')

    def test_replacement_metacharacters_are_literal(self):
        correct = DictionaryCorrector({"l4": r"player\1\g<name>"})
        self.assertEqual(correct.apply("l4!"), r"player\1\g<name>!")


class ConfigAndFormattingTests(unittest.TestCase):
    def test_customized_existing_settings_are_preserved(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "joyvox_config.cfg"
            path.write_text(
                "STT_LISTEN=mouse4\nSTT_KEY=j\nSTT_TEAMKEY=k\n"
                "STT_KEY_AUTO=f5\nSTT_TEAMKEY_AUTO=f6\n"
                "STT_SENTENCECASE=no\nSTT_CAPITALIZE=no\n"
                "STT_ENDWITHPERIOD=no\nSTT_ALLOWEXCLAMATION=no\n"
                "STT_WHISPER_MODEL=base.en\nSTT_OUTPUT_PATH=game\n"
                "STT_PROMPT=See https://example.com/help // actual comment\n",
                encoding="utf-8",
            )
            config = load_config(path)
            for key in ("STT_LISTEN", "STT_KEY", "STT_TEAMKEY", "STT_KEY_AUTO",
                        "STT_TEAMKEY_AUTO", "STT_SENTENCECASE", "STT_CAPITALIZE",
                        "STT_ENDWITHPERIOD", "STT_ALLOWEXCLAMATION"):
                self.assertEqual(config[key], DEFAULT_CONFIG[key])
            self.assertEqual(config["STT_WHISPER_MODEL"], "base.en")
            self.assertEqual(config["STT_OUTPUT_PATH"], str(Path(directory) / "game"))
            self.assertEqual(config["STT_PROMPT"], "See https://example.com/help")
            self.assertEqual(post_process_text("Elfor is at the Safe. Go!", config),
                             "elfor is at the safe. go")

    def test_formatting_options_and_acronyms(self):
        config = dict(DEFAULT_CONFIG)
        self.assertEqual(post_process_text("GG! Are YOU Ready?", config),
                         "GG. are YOU ready?")
        self.assertEqual(post_process_text("Wait...", config), "wait...")
        config.update(STT_SENTENCECASE="yes", STT_CAPITALIZE="yes",
                      STT_ENDWITHPERIOD="yes", STT_ALLOWEXCLAMATION="yes")
        self.assertEqual(post_process_text("go to Elfor", config), "Go to Elfor.")
        self.assertEqual(post_process_text("Go!", config), "Go!")
        self.assertEqual(post_process_text("  \n\t ", config), "")

    def test_unsafe_bindings_and_filenames_are_rejected(self):
        bad_values = {
            "STT_KEY": ['j; quit', 'j"', 'j\nbind x quit', 'mouse6', ''],
            "STT_OUTPUT_FILE": ['../autoexec.cfg', 'a;quit.cfg', 'a".cfg',
                                'folder/joyvox.cfg', r'folder\joyvox.cfg', 'x.cfg\n'],
            "STT_EXEC_FILE": ['../../evil.cfg', 'bad file.cfg'],
        }
        for key, values in bad_values.items():
            for value in values:
                with self.subTest(key=key, value=value):
                    config = dict(DEFAULT_CONFIG)
                    config[key] = value
                    with self.assertRaises(ValueError):
                        validate_config(config)

    def test_alias_bindings_and_numeric_ranges_are_validated(self):
        config = dict(DEFAULT_CONFIG, STT_KEY="Key.Return", STT_TEAMKEY="pgdn")
        validate_config(config)
        self.assertEqual(config["STT_KEY"], "enter")
        self.assertEqual(config["STT_TEAMKEY"], "page_down")
        for key, value in (("STT_KEY", "mouse4"), ("STT_OUTPUT_FILE", "joyvox.cfg"),
                           ("STT_QUEUE_SIZE", "1.5"), ("STT_CPU_THREADS", "0"),
                           ("STT_MIN_AUDIO_LENGTH", "nan"),
                           ("STT_MAX_RECORD_SECONDS", "inf")):
            with self.subTest(key=key, value=value):
                config = dict(DEFAULT_CONFIG)
                config[key] = value
                with self.assertRaises(ValueError):
                    validate_config(config)

    def test_engine_equivalent_modifier_binds_cannot_overwrite_one_another(self):
        config = dict(DEFAULT_CONFIG, STT_LISTEN="ctrl_l", STT_KEY="ctrl_r")
        with self.assertRaises(ValueError):
            validate_config(config)


class ConsoleTextTests(unittest.TestCase):
    def test_text_cannot_add_commands_quotes_macros_or_control_characters(self):
        text = 'hello"; quit\nexec evil.cfg\r$pass\x00\\\\more'
        clean = sanitize_game_text(text)
        for disallowed in ('"', ';', '$', '\n', '\r', '\x00', '\\', '//'):
            self.assertNotIn(disallowed, clean)
        output = build_output([text])
        self.assertEqual(output.count('\n'), 2)
        self.assertEqual(output.count('"'), 4)
        self.assertNotIn(';', output)
        self.assertNotIn('$', output)
        self.assertNotIn('//', output)
        self.assertTrue(output.startswith('alias sttoutput "say '))

    def test_unicode_normalization_does_not_reintroduce_delimiters(self):
        clean = sanitize_game_text('hello ＂；＄ ／／／／ exit')
        for disallowed in ('"', ';', '$', '//'):
            self.assertNotIn(disallowed, clean)

    def test_repeated_slashes_cannot_preserve_comment_delimiters(self):
        output = build_output(['hello //// goodbye', r'\\\\where'])
        self.assertNotIn('//', output)

    def test_accent_transliteration_and_legacy_encoding(self):
        self.assertEqual(sanitize_game_text('café — “yes”…'), "cafe - 'yes'...")
        legacy = sanitize_game_text('café 🎉', ascii_only=False)
        self.assertEqual(legacy, "café ?")
        self.assertEqual(legacy.encode('cp1252').decode('cp1252'), legacy)

    def test_long_single_word_is_retained_across_chunks(self):
        text = "a" * 265
        chunks = split_message(text)
        self.assertEqual(list(map(len, chunks)), [130, 130, 5])
        self.assertEqual(''.join(chunks), text)

    def test_word_splitting_and_truncation_are_explicit_and_bounded(self):
        original = ' '.join(f'w{i}' for i in range(70))
        self.assertEqual(' '.join(split_message(original)), original)
        chunks = split_message(' '.join(['longword'] * 100))
        self.assertEqual(len(chunks), 3)
        self.assertTrue(all(0 < len(chunk) <= 130 for chunk in chunks))
        self.assertTrue(chunks[-1].endswith('...'))
        self.assertEqual(split_message(''), [])
        output = build_output(['first', 'second', 'third'])
        self.assertEqual(output.count(';'), 4)
        self.assertIn('say first; say second; say third', output)


class GameFileTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.config = dict(DEFAULT_CONFIG, STT_OUTPUT_PATH=self.directory.name)
        self.files = GameFiles(self.config)

    def test_generation_guard_and_explicit_alias_reset(self):
        first = self.files.write(['old text'])
        second = self.files.write(['new text'])
        self.assertGreater(second, first)
        self.assertFalse(self.files.clear(expected_generation=first))
        self.assertIn('new text', self.files.output_file.read_text(encoding='cp1252'))
        self.assertTrue(self.files.clear(expected_generation=second))
        self.assertEqual(self.files.output_file.read_text(encoding='cp1252'), EMPTY_ALIASES)
        self.assertFalse(self.files.clear(expected_generation=second))

    def test_atomic_write_failure_retains_old_file_and_generation(self):
        generation = self.files.write(['known text'])
        previous = self.files.output_file.read_bytes()
        with patch('joyvox_core.os.replace', side_effect=PermissionError('file locked')):
            with self.assertRaises(PermissionError):
                self.files.write(['new text'])
        self.assertEqual(self.files.generation, generation)
        self.assertEqual(self.files.output_file.read_bytes(), previous)
        self.assertEqual(sorted(p.name for p in Path(self.directory.name).iterdir()),
                         [self.config['STT_OUTPUT_FILE']])

    def test_failed_clear_does_not_advance_generation(self):
        generation = self.files.write(['known text'])
        with patch('joyvox_core.atomic_write', side_effect=OSError('failure')):
            with self.assertRaises(OSError):
                self.files.clear(expected_generation=generation)
        self.assertEqual(self.files.generation, generation)
        self.assertIn('known text', self.files.output_file.read_text(encoding='cp1252'))

    def test_initialize_preserves_binds_and_removes_stale_output(self):
        self.files.write(['stale speech'])
        self.files.initialize()
        self.assertEqual(self.files.output_file.read_text(encoding='cp1252'), EMPTY_ALIASES)
        output = (Path(self.directory.name) / self.config['STT_EXEC_FILE']).read_text(encoding='cp1252')
        self.assertIn('bind mouse4 +stt', output)
        self.assertIn('bind j sttsay', output)
        self.assertIn('bind k sttsayteam', output)
        self.assertIn('exec joyvoxinput.cfg; sttoutput', output)

    def test_unavailable_game_folder_is_reported(self):
        config = dict(self.config, STT_OUTPUT_PATH=str(Path(self.directory.name) / 'missing'))
        with self.assertRaisesRegex(FileNotFoundError, 'STT_OUTPUT_PATH'):
            GameFiles(config).initialize()


if __name__ == '__main__':
    unittest.main()
