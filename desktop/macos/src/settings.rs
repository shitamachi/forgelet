use serde::{Deserialize, Serialize};
use std::{
    fs,
    io::{self, Read, Write},
    os::unix::fs::PermissionsExt,
    path::{Path, PathBuf},
};

/// What the app remembers between launches. It is written by the app, never
/// edited by hand, and holds no credential: the paired runner stores that in its
/// own cache directory.
#[derive(Clone, Default, Serialize, Deserialize)]
#[serde(deny_unknown_fields, default)]
pub struct Settings {
    pub url: String,
    pub docker: Docker,
}

/// The Docker settings used by `forgelet/build@v1`. Empty fields fall back to
/// the account's Docker environment, as a terminal launch would.
#[derive(Clone, Default, Serialize, Deserialize)]
#[serde(deny_unknown_fields, default)]
pub struct Docker {
    pub config: String,
    pub host: String,
    pub builder: String,
}

impl Docker {
    pub fn configured(&self) -> bool {
        !self.config.trim().is_empty()
            || !self.host.trim().is_empty()
            || !self.builder.trim().is_empty()
    }
}

impl Settings {
    pub fn directory() -> Option<PathBuf> {
        std::env::var_os("HOME")
            .map(|home| PathBuf::from(home).join("Library/Application Support/io.forgelet.runner"))
    }

    /// Where the runner keeps its credential and caches.
    pub fn work_root() -> Option<PathBuf> {
        std::env::var_os("HOME")
            .map(|home| PathBuf::from(home).join("Library/Caches/io.forgelet.runner"))
    }

    pub fn load() -> Result<Self, &'static str> {
        let Some(dir) = Self::directory() else {
            return Ok(Self::default());
        };
        match read_file(&dir.join("settings.json")) {
            Ok(data) => serde_json::from_str(&data).map_err(|_| "本机设置格式无效"),
            Err(error) if error.kind() == io::ErrorKind::NotFound => Self::migrate(&dir),
            Err(_) => Err("无法读取本机设置，请检查文件权限"),
        }
    }

    /// A machine upgraded from the old app keeps working: the previous
    /// connection file supplies the address and Docker settings, and its device
    /// credential becomes the runner's stored credential, so no new approval is
    /// needed. Apps before 0.3 used `profile.json` for the same data, and the
    /// profile name it carried no longer exists, so only the rest is read.
    fn migrate(dir: &Path) -> Result<Self, &'static str> {
        Self::migrate_into(dir, Self::work_root().as_deref())
    }

    /// migrate_into is the testable form: the credential goes to `root` and the
    /// settings to `dir`, never to the real account during a test.
    fn migrate_into(dir: &Path, root: Option<&Path>) -> Result<Self, &'static str> {
        let mut data = None;
        for name in ["connection.json", "profile.json"] {
            match read_file(&dir.join(name)) {
                Ok(found) => {
                    data = Some(found);
                    break;
                }
                Err(error) if error.kind() == io::ErrorKind::NotFound => continue,
                Err(_) => return Err("无法读取旧的本机连接配置，请检查文件权限"),
            }
        }
        let Some(data) = data else {
            return Ok(Self::default());
        };
        let Some(settings) = parse_legacy(&data) else {
            return Err("旧的本机连接配置格式无效");
        };
        if let (Some(root), Some(token)) = (root, settings.0.clone())
            && fs::create_dir_all(root).is_ok()
        {
            let _ = write_private(&root.join("credential"), &token);
        }
        if dir.is_dir() {
            let _ = write_private(
                &dir.join("settings.json"),
                &serde_json::to_string_pretty(&settings.1).unwrap_or_default(),
            );
        }
        Ok(settings.1)
    }

    pub fn save(&self) -> Result<(), String> {
        let Some(dir) = Self::directory() else {
            return Err("无法定位本机设置目录".into());
        };
        fs::create_dir_all(&dir).map_err(|_| "无法创建设置目录".to_string())?;
        let data = serde_json::to_string_pretty(self).map_err(|_| "无法编码设置".to_string())?;
        write_private(&dir.join("settings.json"), &data).map_err(|_| "无法保存设置".to_string())
    }

    pub fn validate(&self) -> Result<(), String> {
        let url = self.url.trim();
        if url.is_empty() {
            return Err("请输入控制面地址".into());
        }
        if !url.starts_with("https://") && !url.starts_with("http://") {
            return Err("控制面地址必须以 https:// 开头".into());
        }
        if self.docker.configured() {
            let config = self.docker.config.trim();
            if !config.is_empty() && !Path::new(config).is_absolute() {
                return Err("Docker 配置目录必须使用绝对路径".into());
            }
            if config.is_empty()
                || self.docker.host.trim().is_empty()
                || self.docker.builder.trim().is_empty()
            {
                return Err("Docker 设置需要同时填写配置目录、Host 和 Builder".into());
            }
        }
        Ok(())
    }
}

/// Parses the old connection file, returning the credential it referenced (when
/// readable) and the settings it represented.
fn parse_legacy(data: &str) -> Option<(Option<String>, Settings)> {
    #[derive(Deserialize)]
    #[serde(deny_unknown_fields)]
    struct Legacy {
        url: String,
        #[serde(default, rename = "profile")]
        _profile: Option<String>,
        token_file: PathBuf,
        #[serde(default)]
        docker: Option<LegacyDocker>,
    }
    #[derive(Deserialize)]
    #[serde(deny_unknown_fields)]
    struct LegacyDocker {
        config: PathBuf,
        host: String,
        builder: String,
    }
    let legacy: Legacy = serde_json::from_str(data).ok()?;
    if legacy.url.trim().is_empty() {
        return None;
    }
    let token = read_private(&legacy.token_file)
        .ok()
        .map(|token| token.trim().to_owned())
        .filter(|token| !token.is_empty());
    let docker = legacy.docker.map_or_else(Docker::default, |docker| Docker {
        config: docker.config.to_string_lossy().into_owned(),
        host: docker.host,
        builder: docker.builder,
    });
    Some((
        token,
        Settings {
            url: legacy.url,
            docker,
        },
    ))
}

fn read_file(path: &Path) -> io::Result<String> {
    const LIMIT: u64 = 64 * 1024;
    let metadata = fs::symlink_metadata(path)?;
    if !metadata.is_file() || metadata.file_type().is_symlink() {
        return Err(io::ErrorKind::PermissionDenied.into());
    }
    let mut data = String::new();
    fs::File::open(path)?
        .take(LIMIT + 1)
        .read_to_string(&mut data)?;
    if data.len() as u64 > LIMIT {
        return Err(io::ErrorKind::InvalidData.into());
    }
    Ok(data)
}

fn read_private(path: &Path) -> io::Result<String> {
    let metadata = fs::symlink_metadata(path)?;
    if !metadata.is_file() || metadata.permissions().mode() & 0o077 != 0 {
        return Err(io::ErrorKind::PermissionDenied.into());
    }
    read_file(path)
}

fn write_private(path: &Path, data: &str) -> io::Result<()> {
    use std::os::unix::fs::{OpenOptionsExt, PermissionsExt};
    let mut file = fs::OpenOptions::new()
        .write(true)
        .create(true)
        .truncate(true)
        .mode(0o600)
        .open(path)?;
    file.set_permissions(fs::Permissions::from_mode(0o600))?;
    file.write_all(data.as_bytes())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn settings_round_trip_and_validation() {
        let settings = Settings {
            url: "https://forgelet.example".into(),
            docker: Docker::default(),
        };
        let text = serde_json::to_string(&settings).unwrap();
        let decoded: Settings = serde_json::from_str(&text).unwrap();
        assert_eq!(decoded.url, settings.url);
        assert!(decoded.validate().is_ok());

        assert!(Settings::default().validate().is_err());
        let partial = Settings {
            url: "https://f".into(),
            docker: Docker {
                config: "/tmp/docker".into(),
                ..Docker::default()
            },
        };
        assert!(partial.validate().is_err());
        let relative = Settings {
            url: "https://f".into(),
            docker: Docker {
                config: "relative".into(),
                host: "unix:///tmp/d.sock".into(),
                builder: "b".into(),
            },
        };
        assert!(relative.validate().is_err());
        assert!(serde_json::from_str::<Settings>(r#"{"url":"https://f","unknown":1}"#).is_err());
    }

    #[test]
    fn older_profile_file_still_migrates() {
        let dir = std::env::temp_dir().join(format!("forgelet-migrate-{}", std::process::id()));
        let cache = dir.join("cache");
        let _ = fs::remove_dir_all(&dir);
        fs::create_dir_all(&dir).unwrap();
        let token = dir.join("token");
        fs::write(
            &token,
            "frr_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n",
        )
        .unwrap();
        fs::set_permissions(&token, fs::Permissions::from_mode(0o600)).unwrap();
        let profile = format!(
            r#"{{"url":"https://forgelet.example","profile":"old-device","token_file":{:?}}}"#,
            token.to_string_lossy()
        );
        fs::write(dir.join("profile.json"), profile).unwrap();
        fs::set_permissions(dir.join("profile.json"), fs::Permissions::from_mode(0o600)).unwrap();

        let settings = Settings::migrate_into(&dir, Some(&cache)).expect("migrated settings");
        assert_eq!(settings.url, "https://forgelet.example");
        assert!(!settings.docker.configured());
        let credential = fs::read_to_string(cache.join("credential")).expect("credential");
        assert_eq!(
            credential.trim(),
            "frr_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
        );
        let saved: Settings =
            serde_json::from_str(&fs::read_to_string(dir.join("settings.json")).unwrap()).unwrap();
        assert_eq!(saved.url, settings.url);
        let _ = fs::remove_dir_all(&dir);
    }

    #[test]
    fn legacy_connection_is_understood() {
        let data = r#"{
            "url": "https://forgelet.example",
            "profile": "old",
            "token_file": "/Users/mac/token",
            "docker": { "config": "/Users/mac/.docker", "host": "unix:///tmp/d.sock", "builder": "mac" }
        }"#;
        let (token, settings) = parse_legacy(data).expect("legacy config");
        assert_eq!(settings.url, "https://forgelet.example");
        assert_eq!(settings.docker.builder, "mac");
        assert!(token.is_none(), "the token file does not exist here");
    }
}
