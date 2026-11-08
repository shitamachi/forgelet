#!/bin/sh
set -eu

repo_dir=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo_dir"
if [ "$(uname -sm)" != "Darwin arm64" ]; then
    echo "The macOS runner bundle must be built on Apple Silicon." >&2
    exit 1
fi

stage=$(mktemp -d "${TMPDIR:-/tmp}/forgelet-runner.XXXXXX")
trap 'rm -rf "$stage"' EXIT
bundle="$stage/Forgelet Runner.app"
iconset="$stage/RunnerIcon.iconset"
mkdir -p "$bundle/Contents/MacOS" "$bundle/Contents/Resources" "$iconset"
swift desktop/macos/icon.swift "$stage/RunnerIcon.png"
for size in 16 32 128 256 512; do
    double=$((size * 2))
    sips -z "$size" "$size" "$stage/RunnerIcon.png" --out "$iconset/icon_${size}x${size}.png" >/dev/null
    sips -z "$double" "$double" "$stage/RunnerIcon.png" --out "$iconset/icon_${size}x${size}@2x.png" >/dev/null
done
iconutil -c icns "$iconset" -o "$bundle/Contents/Resources/RunnerIcon.icns"
CARGO_NET_GIT_FETCH_WITH_CLI=true MACOSX_DEPLOYMENT_TARGET=14.0 cargo build --manifest-path desktop/macos/Cargo.toml --target-dir desktop/macos/target --locked --release
cp desktop/macos/target/release/forgelet-desktop "$bundle/Contents/MacOS/forgelet-desktop"
go build -trimpath -o "$bundle/Contents/MacOS/forgelet-runner" ./cmd/runner
cp desktop/macos/Info.plist "$bundle/Contents/Info.plist"
codesign --force --sign - "$bundle/Contents/MacOS/forgelet-runner"
codesign --force --sign - "$bundle"
codesign --verify --strict --deep "$bundle"

mkdir -p bin
rm -rf 'bin/Forgelet Runner.app'
mv "$bundle" 'bin/Forgelet Runner.app'
printf '%s\n' "$repo_dir/bin/Forgelet Runner.app"
