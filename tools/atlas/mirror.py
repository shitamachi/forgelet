#!/usr/bin/env python3
"""One-shot publication of the verified public Atlas binary as a Zot OCI artifact.

Usage: DOCKER_CONFIG=/private/docker-config python3 tools/atlas/mirror.py CACHE_DIR
No credentials are included in the artifact or passed in command-line arguments.
"""

import base64
import concurrent.futures
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import urllib.error
import urllib.parse
import urllib.request


def main():
    source = json.loads(Path(__file__).with_name("source.json").read_text())
    directory = Path(sys.argv[1])
    directory.mkdir(parents=True, exist_ok=True)
    binary = directory / ("atlas-" + source["version"] + "-linux-amd64")

    def digest(path):
        with path.open("rb") as stream:
            return hashlib.file_digest(stream, "sha256").hexdigest()

    if not binary.exists() or digest(binary) != source["sha256"]:
        partial = binary.with_suffix(".part")
        fd = os.open(partial, os.O_CREAT | os.O_TRUNC | os.O_RDWR, 0o600)
        os.ftruncate(fd, source["size"])

        def download(start):
            end = min(start + (4 << 20), source["size"]) - 1
            part = directory / f"atlas-range-{start}"
            headers = directory / f"atlas-range-{start}.headers"
            try:
                subprocess.run(["curl", "--fail", "--silent", "--show-error", "--location",
                    "--retry", "2", "--retry-all-errors", "--connect-timeout", "10", "--max-time", "180",
                    "--range", f"{start}-{end}", "--dump-header", str(headers),
                    "--output", str(part), source["url"]], check=True)
                ranges = [line.strip().lower() for line in headers.read_text().splitlines() if line.lower().startswith("content-range:")]
                if ranges != [f"content-range: bytes {start}-{end}/{source['size']}"]:
                    raise RuntimeError("upstream did not honor the exact binary range")
                data = part.read_bytes()
                if len(data) != end - start + 1:
                    raise RuntimeError("incomplete upstream binary range")
                os.pwrite(fd, data, start)
            finally:
                part.unlink(missing_ok=True)
                headers.unlink(missing_ok=True)

        try:
            with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
                list(pool.map(download, range(0, source["size"], 4 << 20)))
        finally:
            os.close(fd)
        if digest(partial) != source["sha256"]:
            raise RuntimeError("official binary SHA256 mismatch; not publishing")
        partial.replace(binary)
    print("Verified official Atlas", source["version"], source["sha256"], flush=True)

    host = "zot.k.mygo.rocks"
    repository = "forgelet/dockerhub/arigaio/atlas-cli"
    base = f"https://{host}/v2/{repository}"
    config = json.loads((Path(os.environ["DOCKER_CONFIG"]) / "config.json").read_text())
    auth = config["auths"][host]
    encoded = auth.get("auth") or base64.b64encode((auth["username"] + ":" + auth["password"]).encode()).decode()

    # Do not forward persistent registry credentials through redirects.
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, *args):
            return None

    client = urllib.request.build_opener(NoRedirect)

    def request(url, method, data=None, headers=None):
        return client.open(urllib.request.Request(url, method=method, data=data,
            headers={"Authorization": "Basic " + encoded, **(headers or {})}), timeout=180)

    def upload(sha, size, stream):
        try:
            with request(base + "/blobs/sha256:" + sha, "HEAD"):
                return
        except urllib.error.HTTPError as error:
            if error.code != 404:
                raise
        with request(base + "/blobs/uploads/", "POST", b"") as response:
            location = urllib.parse.urljoin(base + "/", response.headers["Location"])
        parsed = urllib.parse.urlsplit(location)
        if parsed.scheme != "https" or parsed.netloc != host or not parsed.path.startswith(f"/v2/{repository}/blobs/uploads/"):
            raise RuntimeError("refusing registry upload outside the selected repository")
        separator = "&" if parsed.query else "?"
        with request(location + separator + "digest=sha256:" + sha, "PUT", stream,
                     {"Content-Type": "application/octet-stream", "Content-Length": str(size)}) as response:
            if response.status != 201:
                raise RuntimeError("registry did not finalize the blob")

    import io
    empty = b"{}"
    empty_sha = hashlib.sha256(empty).hexdigest()
    upload(empty_sha, len(empty), io.BytesIO(empty))
    with binary.open("rb") as stream:
        upload(source["sha256"], source["size"], stream)
    manifest = json.dumps({
        "schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
        "artifactType": "application/vnd.forgelet.tool.v1",
        "config": {"mediaType": "application/vnd.oci.empty.v1+json", "digest": "sha256:" + empty_sha, "size": len(empty)},
        "layers": [{"mediaType": "application/octet-stream", "digest": "sha256:" + source["sha256"],
                    "size": source["size"], "annotations": {"org.opencontainers.image.title": "atlas"}}],
        "annotations": {"org.opencontainers.image.version": source["version"],
                        "org.opencontainers.image.source": "https://github.com/ariga/atlas"},
    }, separators=(",", ":")).encode()
    with request(base + "/manifests/" + source["version"] + "-linux-amd64", "PUT", manifest,
                 {"Content-Type": "application/vnd.oci.image.manifest.v1+json"}) as response:
        if response.status != 201:
            raise RuntimeError("registry did not accept the tool artifact")
    url = base + "/blobs/sha256:" + source["sha256"]
    # Exercise anonymous streaming download, independently of the publisher credentials.
    with urllib.request.urlopen(url, timeout=120) as response:
        if hashlib.file_digest(response, "sha256").hexdigest() != source["sha256"]:
            raise RuntimeError("anonymous Zot binary verification failed")
    proof = {**source, "zot_blob_url": url, "manifest_digest": "sha256:" + hashlib.sha256(manifest).hexdigest()}
    (directory / "atlas-zot.json").write_text(json.dumps(proof, indent=2) + "\n")
    print(json.dumps(proof, indent=2), flush=True)


if __name__ == "__main__":
    main()
