use serde::{Deserialize, Serialize};
use std::{
    fs,
    io::{self, Read, Write},
    path::{Path, PathBuf},
};

/// What the app remembers between launches. It is written by the app, never
/// edited by hand, and holds no credential: the paired runner stores that in its
/// own cache directory.
#[derive(Clone, Default, Serialize, Deserialize)]
#[serde(deny_unknown_fields, default)]
pub struct Settings {
    /// `wslc` runs each attempt in a fresh container; `wsl2` runs natively in a
    /// distribution, where the machine's Docker and tools are available.
    pub backend: Backend,
    pub url: String,
    pub wslc: Wslc,
    pub wsl2: Wsl2,
}

#[derive(Clone, Copy, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Backend {
    #[default]
    Wslc,
    Wsl2,
}

impl Backend {
    pub fn label(self) -> &'static str {
        match self {
            Backend::Wslc => "WSLC 容器",
            Backend::Wsl2 => "WSL2 发行版",
        }
    }
}

/// The Linux container backend. The image is the job environment; it is pinned
/// by digest once the administrator pushes it.
#[derive(Clone, Default, Serialize, Deserialize)]
#[serde(deny_unknown_fields, default)]
pub struct Wslc {
    pub image: String,
    pub cpus: Option<f64>,
    pub memory: Option<String>,
    pub ca_file: Option<PathBuf>,
    pub control_url: Option<String>,
}

/// Native execution inside a WSL distribution. Empty names mean the WSL default
/// distribution and its default user.
#[derive(Clone, Default, Serialize, Deserialize)]
#[serde(deny_unknown_fields, default)]
pub struct Wsl2 {
    pub distro: String,
    pub user: String,
}

impl Settings {
    pub fn directory() -> Option<PathBuf> {
        std::env::var_os("APPDATA").map(|app_data| PathBuf::from(app_data).join("io.forgelet.runner"))
    }

    /// Where the runner keeps its credential, caches and the embedded binary.
    /// It matches the Go runner's `os.UserCacheDir()/io.forgelet.runner`.
    pub fn work_root() -> Option<PathBuf> {
        match std::env::var_os("LOCALAPPDATA") {
            Some(local) => Some(PathBuf::from(local).join("io.forgelet.runner")),
            None => Some(std::env::temp_dir().join("io.forgelet.runner")),
        }
    }

    pub fn load() -> Result<Self, &'static str> {
        let Some(dir) = Self::directory() else {
            return Ok(Self::default());
        };
        let path = dir.join("settings.json");
        let data = match read_file(&path) {
            Ok(data) => data,
            Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(Self::default()),
            Err(_) => return Err("无法读取本机设置，请检查文件权限"),
        };
        serde_json::from_str(&data).map_err(|_| "本机设置格式无效")
    }

    pub fn save(&self) -> Result<(), String> {
        let Some(dir) = Self::directory() else {
            return Err("无法定位本机设置目录".into());
        };
        fs::create_dir_all(&dir).map_err(|_| "无法创建设置目录".to_string())?;
        let data = serde_json::to_string_pretty(self).map_err(|_| "无法编码设置".to_string())?;
        write_private(&dir.join("settings.json"), &data).map_err(|_| "无法保存设置".to_string())
    }

    /// validate rejects values that cannot start a job, before anything runs.
    pub fn validate(&self) -> Result<(), String> {
        let url = self.url.trim();
        if url.is_empty() {
            return Err("请输入控制面地址".into());
        }
        if !url.starts_with("https://") && !url.starts_with("http://") {
            return Err("控制面地址必须以 https:// 开头".into());
        }
        match self.backend {
            Backend::Wslc => {
                if self.wslc.image.trim().is_empty() {
                    return Err("请填写执行镜像，例如 registry/runner@sha256:…".into());
                }
                if self.wslc.image.contains(char::is_whitespace) {
                    return Err("执行镜像不能包含空格".into());
                }
                if self.cpus_ok() && self.wslc.memory.as_deref().is_some_and(|m| m.contains(char::is_whitespace)) {
                    return Err("内存大小不能包含空格".into());
                }
            }
            Backend::Wsl2 => {
                if self.wsl2.distro.contains(char::is_whitespace) {
                    return Err("WSL 发行版名称不能包含空格".into());
                }
                if self.wsl2.user.contains(char::is_whitespace) || self.wsl2.user.contains(':') {
                    return Err("WSL 用户名不能包含空格或冒号".into());
                }
            }
        }
        Ok(())
    }

    fn cpus_ok(&self) -> bool {
        !self.wslc.cpus.is_some_and(|cpus| cpus < 0.0)
    }
}

fn read_file(path: &Path) -> io::Result<String> {
    const LIMIT: u64 = 64 * 1024;
    let metadata = fs::symlink_metadata(path)?;
    if !metadata.is_file() || metadata.file_type().is_symlink() {
        return Err(io::ErrorKind::PermissionDenied.into());
    }
    let mut data = String::new();
    fs::File::open(path)?.take(LIMIT + 1).read_to_string(&mut data)?;
    if data.len() as u64 > LIMIT {
        return Err(io::ErrorKind::InvalidData.into());
    }
    Ok(data)
}

fn write_private(path: &Path, data: &str) -> io::Result<()> {
    let mut options = fs::OpenOptions::new();
    options.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    options.open(path)?.write_all(data.as_bytes())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn settings_round_trip_and_validation() {
        let settings = Settings {
            url: "https://forgelet.example".into(),
            wslc: Wslc { image: "zot/runner@sha256:abc".into(), cpus: Some(2.0), ..Wslc::default() },
            ..Settings::default()
        };
        let text = serde_json::to_string(&settings).unwrap();
        let decoded: Settings = serde_json::from_str(&text).unwrap();
        assert_eq!(decoded.url, settings.url);
        assert!(decoded.validate().is_ok());

        let missing = Settings::default();
        assert!(missing.validate().is_err());
        let no_image = Settings { url: "https://f".into(), ..Settings::default() };
        assert!(no_image.validate().is_err());
        let spaced = Settings {
            url: "https://f".into(),
            wslc: Wslc { image: "a b".into(), ..Wslc::default() },
            ..Settings::default()
        };
        assert!(spaced.validate().is_err());
        // The WSL2 backend needs no image; it only has to name a valid user.
        let wsl2 = Settings {
            backend: Backend::Wsl2,
            url: "https://f".into(),
            wsl2: Wsl2 { distro: "archlinux".into(), user: "guo".into() },
            ..Settings::default()
        };
        assert!(wsl2.validate().is_ok());
        let bad_user = Settings {
            backend: Backend::Wsl2,
            url: "https://f".into(),
            wsl2: Wsl2 { distro: String::new(), user: "a:b".into() },
            ..Settings::default()
        };
        assert!(bad_user.validate().is_err());
        assert!(serde_json::from_str::<Settings>(r#"{"url":"https://f","unknown":1}"#).is_err());
    }
}
