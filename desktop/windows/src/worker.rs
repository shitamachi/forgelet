use crate::activity::Tail;
use crate::settings::{Settings, Wslc};
use serde::{Deserialize, Serialize};
use std::{
    fs,
    io::{BufRead, BufReader, Write},
    path::PathBuf,
    process::{Child, ChildStdin, Command, Stdio},
    sync::{
        Arc, Mutex,
        mpsc::{self, Receiver},
    },
    thread::JoinHandle,
};

/// The Go runner is compiled by `build.rs` and shipped inside this executable,
/// so the app is one file with nothing to install beside it.
const EMBEDDED_RUNNER: &[u8] = include_bytes!(concat!(env!("OUT_DIR"), "/forgelet-runner.exe"));

#[derive(Serialize)]
pub struct Connection<'a> {
    pub url: &'a str,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub wslc: Option<&'a Wslc>,
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
    pub fn start(connection: Connection<'_>, logs: Arc<Mutex<Tail>>) -> Result<Self, String> {
        let program = materialize_runner()?;
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

/// Writes the embedded runner to the account's cache and returns its path. A
/// matching file is reused, so repeated starts cost one size check; a mismatched
/// one is replaced atomically (a running copy keeps working until it exits).
fn materialize_runner() -> Result<PathBuf, String> {
    let root = Settings::work_root().ok_or("无法定位本机缓存目录")?;
    let dir = root.join("bin");
    fs::create_dir_all(&dir).map_err(|_| "无法创建执行器目录")?;
    let path = dir.join("forgelet-runner.exe");
    if let Ok(metadata) = fs::metadata(&path)
        && metadata.len() == EMBEDDED_RUNNER.len() as u64
    {
        return Ok(path);
    }
    let temporary = dir.join(format!("forgelet-runner-{}.exe", std::process::id()));
    fs::write(&temporary, EMBEDDED_RUNNER).map_err(|_| "无法写入内置执行器")?;
    match fs::rename(&temporary, &path) {
        Ok(()) => Ok(path),
        Err(_) => {
            let _ = fs::remove_file(&temporary);
            // Another instance may be running the old copy; use it if its size
            // still matches, otherwise report the failure.
            match fs::metadata(&path) {
                Ok(metadata) if metadata.len() == EMBEDDED_RUNNER.len() as u64 => Ok(path),
                _ => Err("无法更新内置执行器，请关闭其他实例后重试".into()),
            }
        }
    }
}
