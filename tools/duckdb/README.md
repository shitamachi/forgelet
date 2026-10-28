# Optional DuckDB 2.0 tools for older amd64 workers

DuckDB 2.0's default Linux amd64 optimization profile is x86-64-v3.
A worker without AVX can SIGILL during Go's native driver initialization,
including when integration tests are disabled. Changing Go's CPU budget does
not change the native library's instruction set.

This optional image builds the **same DuckDB 2.0.0 source** with upstream's
`EXTENSION` optimization profile (x86-64-v2), using Debian bookworm's GCC 12
cross compiler. Core functions, JSON, Parquet and ICU remain built in.
The source archive has a fixed commit and SHA256; `provenance.json` records
the library SHA256 and build policy. No application source or credentials are
part of the image. It is not an upstream release binary.

From the repository root:

```sh
docker buildx build --platform linux/amd64 -f tools/duckdb/Dockerfile \
  --target portable-tools --build-arg BUILD_JOBS=2 \
  --build-arg RUNNER_IMAGE=YOUR_EXISTING_TOOLS_IMAGE_AT_DIGEST \
  -t forgelet-portable-duckdb:dev --load .
FORGELET_TEST_DUCKDB_IMAGE=forgelet-portable-duckdb:dev \
  go test -tags=integration ./test/integration -run '^TestPortableDuckDBTools$' -count=1 -timeout=25m
```

The input image must supply Debian bookworm-compatible runtime libraries and
Go 1.26.4 at `/opt/hostedtoolcache/go/1.26.4/x64`. The build uses a native
build-platform compiler targeting amd64, rather than emulating the C++ build.
Limit the Docker build worker's CPU/memory as appropriate; `BUILD_JOBS` is
bounded to 1..4. The `duckdb-library` target can export just the library and
provenance with a local output exporter.

Administrators explicitly select the resulting immutable image as an act
runner image. It sets `GOFLAGS=-tags=duckdb_use_lib` and
`CGO_LDFLAGS=-L/opt/forgelet/duckdb/lib`, and registers the library with the
runtime linker. Those settings select the driver's documented dynamic-link
path; they affect Go commands in that tools image. Existing workflows can
override them, and Dockerfile build stages do not inherit them. The default
native, act and Rust images are unchanged. Do not silently apply this policy
to a driver expecting a different native API version; rebuild and revalidate
when upgrading DuckDB.

After setting the link flags, the image rebuilds the public Go 1.26.4 standard
library cache in normal and race modes. A cache prepared with different
`CGO_LDFLAGS` leaves `runtime/cgo` and its dependents stale. Both cache modes are
checked before running the driver regression. This contains no application
source or private modules and does not report a repository Actions cache hit.

The opt-in Testcontainers regression runs the real Go driver with race enabled,
checks version and SQL results, and exercises JSON, ICU and Parquet without
extension downloads. An amd64 container emulated on another host cannot prove
the physical worker's CPU compatibility: repeat it on the intended worker and
then run the original application workflows. This fixture does not replace
application tests, PostgreSQL/Mongo integration or deployment acceptance.

Sources: [architecture flags](https://github.com/duckdb/duckdb/blob/v2.0.0/scripts/architecture_flags.cmake),
[dynamic linking](https://github.com/duckdb/duckdb-go-bindings/tree/v0.20000.0#dynamic-libraries).
