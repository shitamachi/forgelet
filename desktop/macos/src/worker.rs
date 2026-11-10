use crate::activity::Tail;
use crate::settings::Docker;
use serde::{Deserialize, Serialize};
use std::{
    io::{BufRead, BufReader, Write},
    process::{Child, ChildStdin, Command, Stdio},
    sync::{
        Arc, Mutex,
        mpsc::{self, Receiver},
    },
    thread::JoinHandle,
};

#[derive(Serialize)]
pub struct Connection<'a> {
    pub url: &'a str,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub docker: Option<Docker>,
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
    Status(Box<Status>),
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
        let mut program = std::env::current_exe().map_err(|_| "无法定位应用程序")?;
        program.set_file_name("forgelet-runner");
        let mut command = Command::new(program);
        if let Some(docker) = &connection.docker
            && !docker.config.trim().is_empty()
        {
            command.env("DOCKER_CONFIG", docker.config.trim());
            command.env("DOCKER_HOST", docker.host.trim());
            command.env("BUILDX_BUILDER", docker.builder.trim());
        }
        let mut child = command
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .spawn()
            .map_err(|_| "无法启动内置执行器，请从 .app 打开")?;
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
                        let status = *status;
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

#[cfg(test)]
mod tests {
    use super::*;

    // Captured from the bundled runner while it paired with a local control
    // plane and executed one job on this Mac. The app must read exactly these
    // shapes, including the pairing code and the job header it logs.
    const PAIRING: &str = r#"{"state":"pairing","message":"在 Forgelet 的 Runners 页面批准该设备","code":"EDDH-4L54","expires_at":"2026-11-09T13:56:11.799078Z"}"#;
    const AVAILABLE: &str = r#"{"state":"available","message":"设备已批准","device":"mac-e2e"}"#;
    const RUNNING: &str = r#"{"state":"running","job":{"job_id":"367fd75ddb83026f6cc86648","run_id":"ed69177889f1edf68746c03d","repository":"me/demo","name":"demo / build","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","lease_seconds":59}}"#;
    const LOG: &str =
        r#"{"log":{"seq":1,"step_id":"__step_1","data":"hello from Darwin arm64\n27.2\n"}}"#;

    #[test]
    fn pairing_code_reaches_the_window() {
        let Ok(Update::Status(status)) = serde_json::from_str::<Update>(PAIRING) else {
            panic!("pairing update not understood");
        };
        assert_eq!(status.state, "pairing");
        assert_eq!(status.code, "EDDH-4L54");
        assert!(status.expires_at.starts_with("2026-11-09"));
        assert!(status.job.is_none());
    }

    #[test]
    fn approval_and_job_updates_are_understood() {
        let Ok(Update::Status(approved)) = serde_json::from_str::<Update>(AVAILABLE) else {
            panic!("approval update not understood");
        };
        assert_eq!(approved.state, "available");
        assert_eq!(approved.device, "mac-e2e");

        let Ok(Update::Status(running)) = serde_json::from_str::<Update>(RUNNING) else {
            panic!("running update not understood");
        };
        let job = running.job.expect("job");
        assert_eq!(job.name, "demo / build");
        assert_eq!(job.repository, "me/demo");
        assert_eq!(job.platform, "");
    }

    #[test]
    fn log_lines_are_kept_apart_from_status() {
        let Ok(Update::Log { log }) = serde_json::from_str::<Update>(LOG) else {
            panic!("log update not understood");
        };
        assert_eq!(log.data, "hello from Darwin arm64\n27.2\n");
    }
}
