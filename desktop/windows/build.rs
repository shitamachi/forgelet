// The Windows app ships one executable: both Go runners are compiled here and
// embedded - the Windows supervisor and, for the WSL2 backend, the Linux one it
// installs into the distribution.
//
// The bytes are fingerprinted so the app can tell its cached copies apart: a
// rebuilt runner of the same size still has to replace the one on disk.
use std::{
    env, fs,
    path::{Path, PathBuf},
    process::Command,
};

fn build(out: &Path, goos: &str, name: &str, repo: &Path, package: &str) {
    let target = out.join(name);
    for path in ["cmd", "internal", "go.mod", "go.sum"] {
        println!("cargo:rerun-if-changed={}", repo.join(path).display());
    }
    let status = Command::new("go")
        .current_dir(repo)
        .env("GOOS", goos)
        .env("GOARCH", "amd64")
        .env("CGO_ENABLED", "0")
        .args([
            "build",
            "-trimpath",
            "-o",
            &target.to_string_lossy(),
            package,
        ])
        .status()
        .expect("go build the runners (is Go 1.27+ on PATH?)");
    if !status.success() {
        panic!("go build ./cmd/runner for {goos} failed: {status}");
    }
}

/// fingerprint is an FNV-1a hash of the bytes: enough to name a cached copy.
fn fingerprint(path: &Path) -> String {
    let data = fs::read(path).expect("read the built runner");
    let mut hash: u64 = 0xcbf2_9ce4_8422_2325;
    for byte in data {
        hash ^= u64::from(byte);
        hash = hash.wrapping_mul(0x0000_0100_0000_01b3);
    }
    format!("{hash:016x}")
}

fn main() {
    // NSH loads icon resource 1 for the window; Explorer and the taskbar use
    // the same embedded icon. Keep it in the exe, not in a sidecar file.
    println!("cargo:rerun-if-changed=assets/runner.ico");
    if env::var("CARGO_CFG_TARGET_OS").as_deref() == Ok("windows") {
        winresource::WindowsResource::new()
            .set_icon("assets/runner.ico")
            .set("ProductName", "Forgelet Runner")
            .set("FileDescription", "Forgelet Runner")
            .set("OriginalFilename", "Forgelet Runner.exe")
            .compile()
            .expect("embed the Windows app icon (Windows SDK resource compiler required)");
    }
    let out = PathBuf::from(env::var("OUT_DIR").expect("OUT_DIR"));
    let repo = PathBuf::from(env::var("CARGO_MANIFEST_DIR").expect("CARGO_MANIFEST_DIR"))
        .parent()
        .and_then(|path| path.parent())
        .expect("repository root")
        .to_path_buf();
    build(
        &out,
        "windows",
        "forgelet-runner.exe",
        &repo,
        "./cmd/runner",
    );
    build(
        &out,
        "linux",
        "forgelet-runner-linux",
        &repo,
        "./cmd/runner",
    );
    build(
        &out,
        "linux",
        "forgelet-act-linux",
        &repo,
        "./cmd/act-worker",
    );
    let windows = out.join("forgelet-runner.exe");
    let linux = out.join("forgelet-runner-linux");
    let act = out.join("forgelet-act-linux");
    println!("cargo:rustc-env=FORGELET_RUNNER={}", windows.display());
    println!("cargo:rustc-env=FORGELET_LINUX_RUNNER={}", linux.display());
    println!("cargo:rustc-env=FORGELET_ACT_RUNNER={}", act.display());
    println!(
        "cargo:rustc-env=FORGELET_RUNNER_ID={}",
        fingerprint(&windows)
    );
}
