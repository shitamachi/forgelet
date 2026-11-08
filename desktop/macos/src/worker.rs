use crate::activity::Tail;
use crate::connection::Docker;
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
    #[serde(rename = "profile", skip_serializing_if = "Option::is_none")]
    pub legacy_profile: Option<&'a str>,
    pub token: &'a str,
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
    pub fn start(
        connection: Connection<'_>,
        docker: Option<&Docker>,
        logs: Arc<Mutex<Tail>>,
    ) -> Result<Self, String> {
        let mut program = std::env::current_exe().map_err(|_| "无法定位应用程序")?;
        program.set_file_name("forgelet-runner");
        let mut command = Command::new(program);
        if let Some(docker) = docker {
            command
                .env("DOCKER_CONFIG", &docker.config)
                .env("DOCKER_HOST", &docker.host)
                .env("BUILDX_BUILDER", &docker.builder);
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
