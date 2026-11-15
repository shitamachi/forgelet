use crate::activity::Tail;
use crate::settings::{Backend, Settings, Wslc};
use serde::{Deserialize, Serialize};
use std::{
    fs,
    io::{BufRead, BufReader, Write},
    path::{Path, PathBuf},
    process::{Child, ChildStdin, Command, Stdio},
    sync::{
        Arc, Mutex,
        mpsc::{self, Receiver},
    },
    thread::JoinHandle,
};

/// The Go runners are compiled by `build.rs` and shipped inside this executable,
/// so the app is one file with nothing to install beside it. Their fingerprints
/// name the cached copies: a rebuilt runner of the same size still replaces the
/// one on disk.
const EMBEDDED_RUNNER: &[u8] = include_bytes!(concat!(env!("OUT_DIR"), "/forgelet-runner.exe"));
const RUNNER_ID: &str = env!("FORGELET_RUNNER_ID");
/// The Linux executor the WSL2 backend installs into the distribution.
const EMBEDDED_LINUX_RUNNER: &[u8] = include_bytes!(concat!(env!("OUT_DIR"), "/forgelet-runner-linux"));
/// The act worker that runs full GitHub workflows beside a container engine.
/// The supervisor copies it into each job sandbox, so it has to sit next to the
/// supervisor under the name it looks for.
const EMBEDDED_ACT_RUNNER: &[u8] = include_bytes!(concat!(env!("OUT_DIR"), "/forgelet-act-linux"));

#[derive(Serialize)]
pub struct Connection<'a> {
    pub url: &'a str,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub wslc: Option<Wslc>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub wsl2: Option<Wsl2Connection>,
}

/// The native WSL2 backend: which distribution to use and the host path of the
/// Linux executor the supervisor installs there.
#[derive(Serialize)]
pub struct Wsl2Connection {
    pub distro: String,
    pub user: String,
    pub runner_binary: String,
    pub act_binary: String,
}

#[derive(Clone, Default, PartialEq, Deserialize)]
#[serde(default)]
pub struct Job {
    pub run_id: String,
    pub repository: String,
    pub name: String,
    pub sha: String,
    pub platform: String,
}

#[derive(Clone, Default, PartialEq, Deserialize)]
#[serde(default)]
pub struct Status {
    pub state: String,
    pub message: String,
    pub code: String,
    pub expires_at: String,
    pub device: String,
    pub job: Option<Job>,
}

#[derive(Deserialize)]
#[serde(untagged)]
enum Update {
    Log { log: Log },
    Status(Status),
}

#[derive(Deserialize)]
struct Log {
    data: String,
}

pub struct Worker {
    child: Child,
    stdin: Option<ChildStdin>,
    pub updates: Receiver<Status>,
    reader: Option<JoinHandle<()>>,
}

impl Worker {
    /// start launches the supervisor with this device's settings. The WSL2
    /// backend additionally needs the Linux executor on disk; it is written from
    /// the same embedded copy.
    pub fn start(settings: &Settings, logs: Arc<Mutex<Tail>>) -> Result<Self, String> {
        let program = materialize("forgelet-runner.exe", RUNNER_ID, EMBEDDED_RUNNER)?;
        let mut connection = Connection {
            url: settings.url.trim(),
            wslc: None,
            wsl2: None,
        };
        match settings.backend {
            Backend::Wslc => connection.wslc = Some(settings.wslc.clone()),
            Backend::Wsl2 => {
                // The supervisor installs the Linux executor into the
                // distribution; the full act sandbox also copies an act worker
                // there, so both are written beside the supervisor first.
                let dir = runner_dir()?;
                materialize_fixed(&dir, "forgelet-runner-linux-amd64", EMBEDDED_LINUX_RUNNER)?;
                materialize_fixed(&dir, "forgelet-act-linux-amd64", EMBEDDED_ACT_RUNNER)?;
                connection.wsl2 = Some(Wsl2Connection {
                    distro: settings.wsl2.distro.trim().to_owned(),
                    user: settings.wsl2.user.trim().to_owned(),
                    runner_binary: dir.join("forgelet-runner-linux-amd64").to_string_lossy().into_owned(),
                    act_binary: dir.join("forgelet-act-linux-amd64").to_string_lossy().into_owned(),
                });
            }
        }
        let mut command = Command::new(program);
        // The runner is a console program; starting it from the windowed app
        // must not create a terminal. It writes to the pipes below, so it needs
        // no console of its own at all.
        #[cfg(windows)]
        {
            use std::os::windows::process::CommandExt;
            const DETACHED_PROCESS: u32 = 0x0000_0008;
            command.creation_flags(DETACHED_PROCESS);
        }
        let mut child = command
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .spawn()
            .map_err(|_| "无法启动内置执行器，请重新下载应用")?;
        let mut stdin = child.stdin.take().ok_or("执行器输入不可用")?;
        let stdout = child.stdout.take().ok_or("执行器输出不可用")?;
        let sent = serde_json::to_writer(&mut stdin, &connection)
            .map_err(|_| "无法传递连接配置")
            .and_then(|()| stdin.write_all(b"\n").map_err(|_| "执行器已退出"));
        if let Err(error) = sent {
            drop(stdin);
            let _ = child.wait();
            return Err(error.into());
        }
        let (send, updates) = mpsc::channel();
        let reader = std::thread::spawn(move || {
            for line in BufReader::new(stdout).lines().map_while(Result::ok) {
                match serde_json::from_str(&line) {
                    Ok(Update::Log { log }) => {
                        if let Ok(mut logs) = logs.lock() {
                            logs.push(log.data);
                        }
                    }
                    Ok(Update::Status(status)) => {
                        if status.state == "running"
                            && let Some(job) = &status.job
                            && let Ok(mut logs) = logs.lock()
                        {
                            logs.push(format!(
                                "\n── {} · {} @ {} ──\n",
                                job.name, job.repository, job.sha
                            ));
                        }
                        if send.send(status).is_err() {
                            break;
                        }
                    }
                    Err(_) => {}
                }
            }
        });
        Ok(Self {
            child,
            stdin: Some(stdin),
            updates,
            reader: Some(reader),
        })
    }

    pub fn stop(&mut self) {
        self.stdin.take();
    }

    pub fn exited(&mut self) -> std::io::Result<bool> {
        let Some(_) = self.child.try_wait()? else {
            return Ok(false);
        };
        if let Some(reader) = self.reader.take() {
            let _ = reader.join();
        }
        Ok(true)
    }
}

/// runner_dir is where the app keeps the copies it starts and hands to a WSL2
/// distribution.
fn runner_dir() -> Result<PathBuf, String> {
    let root = Settings::work_root().ok_or("无法定位本机缓存目录")?;
    let dir = root.join("bin");
    fs::create_dir_all(&dir).map_err(|_| "无法创建执行器目录")?;
    Ok(dir)
}

/// Writes an embedded runner to the account's cache and returns its path. The
/// fingerprint is part of the file name, so a rebuilt runner of the same size
/// never reuses a stale copy; older copies are removed once they are not in use.
fn materialize(name: &str, id: &str, bytes: &[u8]) -> Result<PathBuf, String> {
    let dir = runner_dir()?;
    let (stem, extension) = split_name(name);
    let path = dir.join(format!("{stem}-{id}{extension}"));
    write_if_changed(&dir, &path, stem, bytes)?;
    prune(&dir, stem, &path);
    Ok(path)
}

/// materialize_fixed keeps a file under its exact name: the supervisor finds the
/// Linux executor and the act worker that way, so the name cannot carry a
/// fingerprint. The bytes are compared instead, which costs one read per start.
fn materialize_fixed(dir: &Path, name: &str, bytes: &[u8]) -> Result<PathBuf, String> {
    let path = dir.join(name);
    let current = fs::read(&path).ok();
    if current.as_deref() != Some(bytes) {
        let temporary = dir.join(format!("{name}.{}.tmp", std::process::id()));
        fs::write(&temporary, bytes).map_err(|_| "无法写入内置执行器")?;
        if fs::rename(&temporary, &path).is_err() {
            let _ = fs::remove_file(&temporary);
            // A copy still running cannot be replaced on Windows; it is only
            // acceptable when it already holds the bytes we need.
            if fs::read(&path).ok().as_deref() != Some(bytes) {
                return Err("无法更新内置执行器，请关闭其他实例后重试".into());
            }
        }
    }
    Ok(path)
}

/// write_if_changed writes bytes unless the target already holds them.
fn write_if_changed(dir: &Path, path: &Path, stem: &str, bytes: &[u8]) -> Result<(), String> {
    if matches!(fs::metadata(path), Ok(metadata) if metadata.len() == bytes.len() as u64) {
        return Ok(());
    }
    let temporary = dir.join(format!("{stem}.{}.tmp", std::process::id()));
    fs::write(&temporary, bytes).map_err(|_| "无法写入内置执行器")?;
    if fs::rename(&temporary, path).is_err() {
        let _ = fs::remove_file(&temporary);
        // Another instance may be running the old copy; use it if its size
        // still matches, otherwise report the failure.
        match fs::metadata(path) {
            Ok(metadata) if metadata.len() == bytes.len() as u64 => {}
            _ => return Err("无法更新内置执行器，请关闭其他实例后重试".into()),
        }
    }
    Ok(())
}

/// prune removes superseded fingerprint copies, leaving the one about to run and
/// the fixed-name files alone. A copy still executing cannot be deleted on
/// Windows; that failure is harmless.
fn prune(dir: &Path, stem: &str, keep: &Path) {
    let Ok(entries) = fs::read_dir(dir) else {
        return;
    };
    let prefix = format!("{stem}-");
    for entry in entries.flatten() {
        let path = entry.path();
        if path == keep || !path.is_file() {
            continue;
        }
        let fingerprinted = path
            .file_stem()
            .and_then(|value| value.to_str())
            .and_then(|value| value.strip_prefix(&prefix))
            .is_some_and(|rest| rest.len() == 16 && rest.chars().all(|c| c.is_ascii_hexdigit()));
        if fingerprinted {
            let _ = fs::remove_file(&path);
        }
    }
}

/// split_name separates a cached runner's stem from its extension: the Windows
/// copy keeps `.exe`, the Linux one has none.
fn split_name(name: &str) -> (&str, String) {
    match name.split_once('.') {
        Some((stem, extension)) => (stem, format!(".{extension}")),
        None => (name, String::new()),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn cached_runners_are_named_by_fingerprint() {
        assert_eq!(split_name("forgelet-runner.exe"), ("forgelet-runner", ".exe".into()));
        assert_eq!(split_name("forgelet-runner-linux"), ("forgelet-runner-linux", String::new()));
    }

    /// The supervisor finds the Linux executor and the act worker by exact
    /// name, so a rebuild with different bytes must replace the cached copy.
    #[test]
    fn fixed_names_are_replaced_when_the_bytes_change() {
        let dir = std::env::temp_dir().join(format!("forgelet-fixed-{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        fs::create_dir_all(&dir).unwrap();
        let path = materialize_fixed(&dir, "forgelet-act-linux-amd64", b"first").unwrap();
        assert_eq!(fs::read(&path).unwrap(), b"first");
        // The same bytes are left alone, so a start does not rewrite the file.
        assert_eq!(materialize_fixed(&dir, "forgelet-act-linux-amd64", b"first").unwrap(), path);
        // Different bytes of the same length still replace it.
        assert_eq!(materialize_fixed(&dir, "forgelet-act-linux-amd64", b"other").unwrap(), path);
        assert_eq!(fs::read(&path).unwrap(), b"other");
        let _ = fs::remove_dir_all(&dir);
    }
}
