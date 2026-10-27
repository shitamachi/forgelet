#!/bin/sh
set -eu
jobs=${1:-2}
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
case "$jobs" in 1|2|3|4) ;; *) echo 'build parallelism must be 1..4' >&2; exit 1 ;; esac
: "${DUCKDB_SOURCE:?}" "${DUCKDB_BUILD:?}" "${DUCKDB_RESULT:?}"
cmake -S "$DUCKDB_SOURCE" -B "$DUCKDB_BUILD" -G Ninja \
    -DCMAKE_BUILD_TYPE=Release -DCMAKE_SYSTEM_NAME=Linux -DCMAKE_SYSTEM_PROCESSOR=x86_64 \
    -DCMAKE_C_COMPILER=x86_64-linux-gnu-gcc -DCMAKE_CXX_COMPILER=x86_64-linux-gnu-g++ \
    -DDUCKDB_EXPLICIT_PLATFORM=linux_amd64 -DDUCKDB_EXPLICIT_VERSION=v2.0.0 \
    -DGIT_COMMIT_HASH=d54cbbe3df17e8e5ce68312f483b4155836112aa \
    -DDUCKDB_OPTIMIZATION_PROFILE=EXTENSION -DNATIVE_ARCH=OFF \
    -DBUILD_UNITTESTS=OFF -DBUILD_SHELL=OFF
cmake --build "$DUCKDB_BUILD" --target duckdb --parallel "$jobs"
mkdir -p "$DUCKDB_RESULT"
cp "$DUCKDB_BUILD/src/libduckdb.so" "$DUCKDB_RESULT/libduckdb.so"
x86_64-linux-gnu-strip --strip-unneeded "$DUCKDB_RESULT/libduckdb.so"
cp "$DUCKDB_SOURCE/LICENSE" "$DUCKDB_RESULT/LICENSE"
x86_64-linux-gnu-gcc -march=x86-64-v2 -I"$DUCKDB_SOURCE/src/include" \
    "$script_dir/probe.c" -L"$DUCKDB_RESULT" -lduckdb \
    -Wl,-rpath,'$ORIGIN/../lib' -o "$DUCKDB_RESULT/duckdb-probe"
python3 - "$DUCKDB_RESULT" "$DUCKDB_SOURCE" <<'PY'
import hashlib, json, pathlib, shutil, sys
p = pathlib.Path(sys.argv[1])
source = pathlib.Path(sys.argv[2])
for root in ('third_party', 'extension'):
    for f in (source / root).rglob('*'):
        if f.is_file() and f.name.upper().startswith(('LICENSE', 'LICENCE', 'COPYING')):
            dest = p / 'licenses' / f.relative_to(source)
            dest.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(f, dest)
(p / 'provenance.json').write_text(json.dumps({
    'version': 'v2.0.0',
    'source_commit': 'd54cbbe3df17e8e5ce68312f483b4155836112aa',
    'source_archive_sha256': 'e2b89ce6058186436cc076f2e94e3bf7f3742cd6f3748a4a585ad0635af94b8a',
    'architecture': 'linux/amd64', 'cpu_baseline': 'x86-64-v2',
    'optimization_profile': 'EXTENSION', 'native_arch': False,
    'compiler': 'Debian GCC 12 cross compiler',
    'builtin_extensions': ['core_functions', 'parquet', 'json', 'icu'],
    'library_sha256': hashlib.sha256((p / 'libduckdb.so').read_bytes()).hexdigest(),
}, indent=2) + '\n')
PY
