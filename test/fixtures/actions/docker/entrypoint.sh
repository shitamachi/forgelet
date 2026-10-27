set -eu
test -n "$GITHUB_WORKSPACE"
test -f "$GITHUB_WORKSPACE/node24/main.js"
printf 'answer=%s\n' "$1" >> "$GITHUB_OUTPUT"
printf 'docker-action-output-channel-ok\n'
