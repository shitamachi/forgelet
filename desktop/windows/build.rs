// The Windows app ships one executable: both Go runners are compiled here and
// embedded - the Windows supervisor and, for the WSL2 backend, the Linux one it
// installs into the distribution.
use std::{env, path::PathBuf, process::Command};

fn build(out: &PathBuf, goos: &str, name: &str, repo: &PathBuf) {
    let target = out.join(name);
    for path in ["cmd/runner", "internal", "go.mod", "go.sum"] {
        println!("cargo:rerun-if-changed={}", repo.join(path).display());
    }
    let status = Command::new("go")
        .current_dir(repo)
        .env("GOOS", goos)
        .env("GOARCH", "amd64")
        .env("CGO_ENABLED", "0")
        .args(["build", "-trimpath", "-o", &target.to_string_lossy(), "./cmd/runner"])
        .status()
        .expect("go build the runners (is Go 1.27+ on PATH?)");
    if !status.success() {
        panic!("go build ./cmd/runner for {goos} failed: {status}");
    }
}

fn main() {
    let out = PathBuf::from(env::var("OUT_DIR").expect("OUT_DIR"));
    let repo = PathBuf::from(env::var("CARGO_MANIFEST_DIR").expect("CARGO_MANIFEST_DIR"))
        .parent()
        .and_then(|path| path.parent())
        .expect("repository root")
        .to_path_buf();
    build(&out, "windows", "forgelet-runner.exe", &repo);
    build(&out, "linux", "forgelet-runner-linux", &repo);
    println!("cargo:rustc-env=FORGELET_RUNNER={}", out.join("forgelet-runner.exe").display());
    println!("cargo:rustc-env=FORGELET_LINUX_RUNNER={}", out.join("forgelet-runner-linux").display());
}
