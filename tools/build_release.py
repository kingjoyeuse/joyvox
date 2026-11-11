#!/usr/bin/env python3
"""Build the native Windows GUI with Go and package its embedded support files.

No third-party Go modules, C compiler, Windows SDK, or live network access are
needed. The same command cross-compiles on Linux/macOS or builds on Windows.
"""
from __future__ import annotations
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import struct
import subprocess
import zipfile

ROOT = Path(__file__).resolve().parents[1]


def sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def payload_files():
    for path in sorted((ROOT / 'app').glob('*')):
        if path.suffix in ('.py', '.ogg'):
            yield path, 'app/' + path.name
    for name in ('runtime.ps1', 'check_dependencies.py'):
        yield ROOT / 'tools' / name, 'tools/' + name
    yield ROOT / 'requirements.txt', 'requirements.txt'
    yield ROOT / 'NOTICE.md', 'NOTICE.md'
    for name in ('joyvox_config.cfg', 'joyvox_dictionary.cfg'):
        yield ROOT / 'defaults' / name, 'defaults/' + name


def aligned(data: bytes, alignment: int = 4) -> bytes:
    return data + b'\0' * (-len(data) % alignment)


def version_block(key: str, value=b'', children=(), text=False) -> bytes:
    # VERSIONINFO's lengths are bytes except text values, which use UTF-16 units.
    header = struct.pack('<HHH', 0, len(value) // 2 if text else len(value), int(text))
    body = aligned(header + (key + '\0').encode('utf-16le')) + value
    for child in children:
        body = aligned(body) + child
    return struct.pack('<H', len(body)) + body[2:]


def version_resource(version: str) -> bytes:
    a, b, c = map(int, version.split('.'))
    fixed = struct.pack('<13I', 0xFEEF04BD, 0x00010000,
                        (a << 16) | b, c << 16, (a << 16) | b, c << 16,
                        0x3f, 0, 0x40004, 1, 0, 0, 0)
    strings = {'FileDescription': 'JoyVox - speech to in-game chat',
               'FileVersion': version, 'InternalName': 'joyvox',
               'OriginalFilename': 'joyvox.exe', 'ProductName': 'JoyVox',
               'ProductVersion': version}
    table = version_block('040904B0', children=[version_block(k, (v+'\0').encode('utf-16le'), text=True)
                                              for k, v in strings.items()], text=True)
    info = version_block('StringFileInfo', children=[table], text=True)
    translation = version_block('Translation', struct.pack('<HH', 0x0409, 1200))
    var = version_block('VarFileInfo', children=[translation], text=True)
    return version_block('VS_VERSION_INFO', fixed, [info, var])


def icon_resources(path: Path):
    data = path.read_bytes()
    reserved, icon_type, count = struct.unpack_from('<HHH', data, 0)
    if reserved != 0 or icon_type != 1 or count < 1:
        raise ValueError('Invalid ICO file')
    images = []
    group_entries = []
    for i in range(count):
        entry = struct.unpack_from('<BBBBHHII', data, 6 + i * 16)
        width, height, color_count, reserved2, planes, bit_count, size, offset = entry
        image = data[offset:offset + size]
        res_id = i + 1
        images.append((3, res_id, 1033, image))
        group_entries.append(struct.pack('<BBBBHHIH', width, height, color_count, reserved2, planes, bit_count, size, res_id))
    group = struct.pack('<HHH', 0, 1, count) + b''.join(group_entries)
    images.append((14, 1, 1033, group))
    return images


def write_resource_object(resources, path: Path):
    """Minimal AMD64 COFF .rsrc object for Go's linker."""
    tree = {}
    for resource_type, resource_id, language, data in resources:
        tree.setdefault(resource_type, {}).setdefault(resource_id, {})[language] = data
    blob = bytearray()
    def reserve_directory(named: int, ids: int):
        offset = len(blob)
        blob.extend(b'\0' * (16 + 8 * (named + ids)))
        struct.pack_into('<IIHHHH', blob, offset, 0, 0, 0, 0, named, ids)
        return offset
    root = reserve_directory(0, len(tree))
    relocations = []
    for type_index, resource_type in enumerate(sorted(tree)):
        ids = tree[resource_type]
        type_dir = reserve_directory(0, len(ids))
        struct.pack_into('<II', blob, root + 16 + type_index * 8, resource_type, type_dir | 0x80000000)
        for id_index, resource_id in enumerate(sorted(ids)):
            langs = ids[resource_id]
            id_dir = reserve_directory(0, len(langs))
            struct.pack_into('<II', blob, type_dir + 16 + id_index * 8, resource_id, id_dir | 0x80000000)
            for lang_index, language in enumerate(sorted(langs)):
                data = langs[language]
                data_entry = len(blob)
                blob.extend(b'\0' * 16)
                blob.extend(b'\0' * (-len(blob) % 4))
                offset = len(blob)
                blob.extend(data)
                blob.extend(b'\0' * (-len(blob) % 4))
                struct.pack_into('<II', blob, id_dir + 16 + lang_index * 8, language, data_entry)
                struct.pack_into('<IIII', blob, data_entry, offset, len(data), 0, 0)
                relocations.append(struct.pack('<IIH', data_entry, 0, 3))
    reloc = b''.join(relocations)
    raw_start = 20 + 40
    symbol_start = raw_start + len(blob) + len(reloc)
    header = struct.pack('<HHIIIHH', 0x8664, 1, 0, symbol_start, 1, 0, 0)
    section = struct.pack('<8sIIIIIIHHI', b'.rsrc\0\0\0', 0, 0, len(blob), raw_start,
                          raw_start + len(blob), 0, len(relocations), 0, 0x40000040)
    symbol = struct.pack('<8sIhHBB', b'.rsrc\0\0\0', 0, 1, 0, 3, 0)
    path.write_bytes(header + section + blob + reloc + symbol + struct.pack('<I', 4))


def generate_resources(version: str):
    output = ROOT / 'gui' / 'payload'
    if output.exists():
        shutil.rmtree(output)
    for source, relative in payload_files():
        if not source.is_file():
            raise FileNotFoundError(f'Missing embedded support file: {source}')
        destination = output / relative
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(source, destination)
    manifest = (ROOT / 'gui' / 'joyvox.manifest').read_bytes()
    resources = [(16, 1, 1033, version_resource(version)), (24, 1, 1033, manifest)]
    resources.extend(icon_resources(ROOT / 'gui' / 'joyvox.ico'))
    write_resource_object(resources, ROOT / 'gui' / 'resource_windows_amd64.syso')


def source_manifest(version: str):
    files = []
    for folder in ('app', 'gui', 'tools', 'defaults', 'tests', '.github'):
        base = ROOT / folder
        for path in base.rglob('*'):
            if not path.is_file() or '__pycache__' in path.parts or 'payload' in path.parts:
                continue
            if path.suffix.lower() in ('.py', '.go', '.cpp', '.json', '.manifest', '.ps1', '.cmd', '.bat', '.yml', '.yaml', '.cfg', '.ogg', '.ico'):
                files.append(path)
    for name in ('VERSION', 'requirements.txt', 'go.mod', 'go.sum', 'README.md', 'RELEASING.md',
                 'RELEASE_NOTES.md', 'NOTICE.md', '.gitignore', '.gitattributes',
                 'Publish-to-GitHub.cmd', 'CHANGELOG.md'):
        if (ROOT / name).is_file():
            files.append(ROOT / name)
    return {'version': version, 'files': {p.relative_to(ROOT).as_posix(): sha256(p)
                                         for p in sorted(set(files))}}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--resources-only', action='store_true')
    parser.add_argument('--package-only', action='store_true', help='Repackage an already-built EXE; maintainers only')
    args = parser.parse_args()
    version = (ROOT / 'VERSION').read_text(encoding='utf-8-sig').strip()
    if not re.fullmatch(r'\d+\.\d+\.\d+', version):
        raise ValueError('VERSION must contain three numeric components')
    generate_resources(version)
    if args.resources_only:
        return
    executable = ROOT / 'joyvox.exe'
    if not args.package_only:
        go = shutil.which('go')
        if not go:
            raise RuntimeError('Go 1.23 or later is needed to rebuild. End users only need joyvox.exe.')
        env = dict(os.environ, GOOS='windows', GOARCH='amd64', CGO_ENABLED='0',
                   GOTOOLCHAIN='local', GOPROXY='off', GOSUMDB='off')
        subprocess.run([go, 'build', '-trimpath', '-ldflags', '-s -w -H=windowsgui',
                        '-o', str(executable), './gui'], cwd=ROOT, env=env, check=True)
    if not executable.is_file() or executable.read_bytes()[:2] != b'MZ':
        raise RuntimeError('No Windows executable was produced')
    release = ROOT / 'release'
    release.mkdir(exist_ok=True)
    zip_path = release / f'joyvox-{version}-windows-x64-portable.zip'
    quick_start = f'''JoyVox {version} - Windows 10/11, Intel/AMD x64\n\n1. Close an older JoyVox, then put joyvox.exe in its existing writable folder.\n   Existing configuration, dictionary, models, and private runtime are retained.\n2. Open joyvox.exe. Select any Quake 2 engine game EXE and check the output folder.\n   Kingpin.exe defaults to main; other executable names default to baseq2.\n   Select your actual mod folder when it differs.\n3. Skip setup when dependencies are ready. Otherwise click Install / repair.\n   Grey means setup is ready; blue indicates required components need attention.\n   Compatible installed Python 3.10-3.12 is reused in an isolated environment.\n   Otherwise the installer downloads a private Python 3.12.10 runtime.\n   It adds exec joyvox.cfg only if no existing direct or chained exec loads it.\n4. Select your microphone by name. Click Start JoyVox; wait for READY.\n   First start downloads the selected voice model.\n5. Launch your game. If it was already running, execute the configured binding\n   file manually in the console, for example: exec joyvox.cfg\n\nDefault keys: hold Mouse 4 to talk; F5 public / F6 team; J/K manually send text.\nAdvanced config (gear icon) opens all settings, including dictionary/model paths.\nClick the key-only Quick bind buttons to capture new bindings. Esc cancels.\nAuto shows its detected model in parentheses and updates after dependency checks.\nStop recognition before editing settings.\n\nFresh download estimates: recognition packages 150-250 MB; NVIDIA libraries\nadd about 1.1-1.5 GB; private Python adds about 11 MB only when needed. Models\nare separate: small.en about 500 MB, large-v3-turbo about 1.6 GB. Actual totals\nvary with package versions, the selected model, and existing local downloads.\n\nThis is a small ONLINE setup package, not an offline AI model bundle.\nRecognition runs locally after setup. The native GUI needs no Python to open.\nWindows 7/8/8.1, ARM64, and 32-bit Windows are not supported by this complete\nrecognition stack. Installing an older Python does not make it compatible.\n\nMissing autoexec entries appear in red, but never block JoyVox or game launch.\nThe installer preserves autoexec.cfg and saves its first original contents as\nautoexec.cfg.joyvox.bak before adding the configured exec line once.\n\nThis EXE is unsigned. Windows GUI, microphone, CUDA and in-game behavior need\nvalidation on your Windows machine; the build host cannot exercise them.\n'''
    with zipfile.ZipFile(zip_path, 'w', zipfile.ZIP_DEFLATED, compresslevel=9) as archive:
        archive.write(executable, 'joyvox.exe')
        archive.write(ROOT / 'NOTICE.md', 'NOTICE.md')
        archive.writestr('QUICK_START.txt', quick_start.replace('\n', '\r\n'))
    (release / 'source-manifest.json').write_text(json.dumps(source_manifest(version), indent=2) + '\n', encoding='utf-8')
    (release / 'SHA256SUMS.txt').write_text(f'{sha256(zip_path)}  {zip_path.name}\n{sha256(executable)}  joyvox.exe\n', encoding='ascii')
    print(f'Built {executable.name}: {executable.stat().st_size:,} bytes')
    print(f'Portable ZIP: {zip_path.name}: {zip_path.stat().st_size:,} bytes')


if __name__ == '__main__':
    main()
