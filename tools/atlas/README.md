# Atlas through the existing Zot registry

`source.json` pins the official standard Atlas CLI v1.3.0 Linux amd64 binary
and its upstream SHA256. It is not a custom or community build. The operational
publisher downloads the public binary once, verifies its exact length and hash,
and publishes an OCI artifact with a raw binary layer in Zot's existing
public-upstream namespace. No application source or credentials are published.

```sh
DOCKER_CONFIG=/private/registry-config \
  python3 tools/atlas/mirror.py /private/atlas-publication-cache
```

The Docker config must already authorize writes to `zot.k.mygo.rocks`. The
publisher only reads that local file; credentials stay out of process arguments,
redirects, output and artifacts. The selected namespace already allows anonymous
reads and denies anonymous writes; no new ACL or resident service is needed.
Deleting the OCI manifest may make the binary eligible for registry garbage
collection, so retain the pinned version while a workflow uses it.

The Tasks workflow uses the immutable Zot blob URL, verifies the same SHA256,
and caches the tool using `actions/cache@v4`. A corrupted cache is replaced only
after a verified download. Failed or incomplete downloads preserve an existing
installation; successful installation uses an atomic rename. Normal workflows
do not need registry credentials, Docker or public Atlas downloads for this step.
The tool remains Linux amd64 only. Its source version and checksum are explicit
upgrade inputs rather than a mutable `latest` redirect.

The business repository contains HTTP behavior tests for cache reuse, corruption,
wrong hashes, truncated responses, HTTP failures and temporary-file cleanup.
To verify the actual binary, business installer and both migration directories:

```sh
FORGELET_TEST_ATLAS_SOURCE_DIR=/path/to/steam-tasks \
FORGELET_TEST_ATLAS_IMAGE=your-current-linux-amd64-tools-image \
  go test -tags=integration ./test/integration \
  -run '^TestMirroredAtlasInstaller$' -count=1 -v -timeout=10m
```

This opt-in Testcontainers case runs the real binary, validates both migration
directories, checks a warm installation with an unusable network proxy, repairs
a corrupt cache and confirms a modified migration is rejected. It does not
replace the separate real push, Go cache restart or PostgreSQL 18 acceptance.

Upstream: [standard distribution](https://www.atlasgo.io/community-edition),
[versioned binary checksum](https://atlasbinaries.com/atlas/atlas-linux-amd64-v1.3.0.sha256).
