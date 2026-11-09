// The Windows app ships one executable: the Go runner is compiled here and
// embedded, then written to the account's cache directory when the app starts.
use std::{env, path::PathBuf, process::Command};

fn main() {
    let out = PathBuf::from(env::var("OUT_DIR").expect("OUT_DIR")).join("forgelet-runner.exe");
    let repo = PathBuf::from(env::var("CARGO_MANIFEST_DIR").expect("CARGO_MANIFEST_DIR"))
        .parent()
        .and_then(|path| path.parent())
        .expect("repository root")
        .to_path_buf();
    for path in ["cmd/runner", "internal", "go.mod", "go.sum"] {
        println!("cargo:rerun-if-changed={}", repo.join(path).display());
    }
    let target = out.to_string_lossy().into_owned();
    let status = Command::new("go")
        .current_dir(&repo)
        .env("GOOS", "windows")
        .env("GOARCH", "amd64")
        .env("CGO_ENABLED", "0")
        .args(["build", "-trimpath", "-o", &target, "./cmd/runner"])
        .status()
        .expect("go build the Windows runner (is Go 1.27+ on PATH?)");
    if !status.success() {
        panic!("go build ./cmd/runner failed: {status}");
    }
    println!("cargo:rustc-env=FORGELET_RUNNER={}", out.display());
}
