# Public Action source bundles in an optional executor image

The original public Actions still run through act and real Node processes.
This image option only removes repeated source-archive downloads for explicitly
locked commits. It does not replace Actions with Forgelet implementations.

`sources.json` records official GitHub codeload URLs, exact commit SHAs, compressed
archive lengths and SHA256 values for the seven Actions used by the Tasks
workflows. `fetch.py` downloads only those public URLs without repository tokens,
checks every archive, and atomically writes `manifest.json`. Corrupt or incomplete
downloads cannot replace a verified file. The archives are image assets, not Git
attachments. Do not update checksum locks automatically when a download differs.

```sh
bundle_context=/private/action-bundle-image
python3 tools/actions/fetch.py "$bundle_context/action-sources"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -o "$bundle_context/forgelet-act" ./cmd/act-worker
docker build -f tools/actions/Dockerfile \
  --build-arg EXECUTOR_IMAGE=your-current-supervisor@sha256:parent-digest \
  -t your-registry/executor-act:action-bundles "$bundle_context"
```

Publish this optional supervisor image to Zot and select its digest through the
Ubuntu profile's `image`. Keep the independent job tool image (`act.runner_image`)
and existing resource budgets. There is no additional service, PVC, database or
idle process; the seven compressed archives add about 15 MiB of image assets.
The cluster's normal image cache can retain them between jobs.

The worker reads bundles only from `/opt/forgelet/action-sources`, supplied by
the administrator's supervisor image. For an exact repository/commit entry it
checks archive length limits, SHA256, GitHub's commit header, and the existing
bounded extraction/path rules before atomically accepting the source directory.
An advertised but corrupt bundle fails without public fallback. Missing images,
entries or newer commits retain the existing public-download path and diagnostics.
Workflow content cannot supply this manifest or choose the bundle directory.

This caches source archives only. Actions may still download tools or contact
external services; repository checkout, Go/npm dependencies and Codecov uploads
retain their own network requirements. Native execution and unbundled profiles
retain their existing behavior.

Unit/HTTP regressions cover exact immutable matching, zero network requests for
preloaded success/corruption, malformed or oversized manifests and archives,
commit mismatch, unsafe paths, cancellation and temporary-source cleanup.
The optional Testcontainers case reads every real bundle in the actual image
with network disabled, a read-only root, nonroot user, 1 CPU and 512 MiB:

```sh
FORGELET_TEST_ACTION_BUNDLE_IMAGE=your-registry/executor-act:action-bundles \
  go test -tags=integration ./test/integration \
  -run '^TestPreloadedActionImageArchives$' -count=1 -v -timeout=7m
```

The test mounts only its own cross-compiled read-only probe binary; it does not
mount a host Docker socket or Kubernetes credentials. Actual workflow execution,
push permissions, cancellation and restart checks remain separate validations.
