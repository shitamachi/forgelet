#!/bin/sh
# Populate only public SDK packages, with this image's actual CGO/link flags.
set -eu
export GOTOOLCHAIN=local GOWORK=off GOMAXPROCS=2 GOCACHE=/root/.cache/go-build
go_bin=/opt/hostedtoolcache/go/1.26.4/x64/bin/go
cd /tmp
"$go_bin" install -p=2 std
"$go_bin" install -p=2 -race std
for mode in normal race; do
    if [ "$mode" = race ]; then
        stale=$("$go_bin" list -race -f '{{if .Stale}}{{.ImportPath}}: {{.StaleReason}}{{end}}' std)
    else
        stale=$("$go_bin" list -f '{{if .Stale}}{{.ImportPath}}: {{.StaleReason}}{{end}}' std)
    fi
    if [ -n "$stale" ]; then
        printf '%s standard library cache remains stale:\n%s\n' "$mode" "$stale" >&2
        exit 1
    fi
done
echo 'Public Go normal/race standard library cache is ready'
