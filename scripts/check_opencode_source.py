#!/usr/bin/env python3
"""Verify the audited OpenCode source and optionally check npm stable drift."""
import argparse
import hashlib
import json
from pathlib import Path
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source', type=Path, help='checkout of the audited upstream release')
    parser.add_argument('--latest', action='store_true', help='check the official npm stable channel')
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[1]
    manifest = json.loads((root/'internal/proxy/opencode/manifest.json').read_text())
    okay = True
    print('Audited release:', manifest['version'], manifest['commit'])
    if args.source:
        for name, expected in manifest['source_sha256'].items():
            source = args.source/name
            match = source.is_file() and hashlib.sha256(source.read_bytes()).hexdigest() == expected
            print('MATCH' if match else 'DRIFT', name)
            okay &= match
    if args.latest:
        with urllib.request.urlopen('https://registry.npmjs.org/@opencode/cli/latest', timeout=15) as response:
            latest = json.load(response)['version']
        match = latest == manifest['version']
        print('MATCH' if match else 'NEW RELEASE: audit required', 'npm stable:', latest)
        okay &= match
    return 0 if okay else 1


if __name__ == '__main__':
    raise SystemExit(main())
