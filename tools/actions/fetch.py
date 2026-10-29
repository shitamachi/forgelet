#!/usr/bin/env python3
"""Fetch the locked public Action archives for an optional executor image."""
from pathlib import Path
from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
import os
import re
import subprocess
import sys
import tempfile


def main():
    root = Path(sys.argv[1]).resolve()
    records = json.loads(Path(__file__).with_name('sources.json').read_text())
    if not 1 <= len(records) <= 128:
        raise ValueError('expected 1..128 immutable public Actions')

    def fetch(item):
        key, record = item
        if not re.fullmatch(r'[A-Za-z0-9_-][A-Za-z0-9_.-]*/[A-Za-z0-9_-][A-Za-z0-9_.-]*@[0-9a-f]{40}', key):
            raise ValueError('invalid immutable Action key')
        repo, sha = key.rsplit('@', 1)
        url = f'https://codeload.github.com/{repo}/tar.gz/{sha}'
        if record['url'] != url or not re.fullmatch(r'[0-9a-f]{64}', record['sha256']):
            raise ValueError('invalid Action origin/checksum')
        if not 0 < record['bytes'] <= 32 << 20:
            raise ValueError('Action archive exceeds budget')
        path = root / repo / (sha + '.tar.gz')
        path.parent.mkdir(parents=True, exist_ok=True)

        def valid(candidate):
            if not candidate.is_file() or candidate.stat().st_size != record['bytes']:
                return False
            with candidate.open('rb') as source:
                return hashlib.file_digest(source, 'sha256').hexdigest() == record['sha256']

        if not valid(path):
            fd, name = tempfile.mkstemp(prefix='.download-', dir=path.parent)
            os.close(fd)
            temporary = Path(name)
            try:
                subprocess.run(['curl', '--proto', '=https', '--fail', '--silent', '--show-error',
                                '--connect-timeout', '15', '--max-time', '600', '--retry', '2',
                                '--retry-all-errors', '--max-filesize', str(32 << 20),
                                url, '--output', str(temporary)], check=True)
                if not valid(temporary):
                    raise ValueError('official Action archive length/SHA256 mismatch: ' + key)
                temporary.chmod(0o644)
                temporary.replace(path)
            finally:
                temporary.unlink(missing_ok=True)
        print('Verified ' + key, flush=True)
        return key, record['sha256']

    with ThreadPoolExecutor(max_workers=3) as pool:
        manifest = dict(pool.map(fetch, records.items()))
    target = root / 'manifest.json'
    temporary = root / '.manifest.tmp'
    temporary.write_text(json.dumps(manifest, indent=2) + '\n')
    temporary.replace(target)


if __name__ == '__main__':
    main()
