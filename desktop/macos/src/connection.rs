use serde::Deserialize;
use std::{
    fs,
    io::{self, Read},
    os::unix::fs::PermissionsExt,
    path::{Path, PathBuf},
};

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub struct LocalConnection {
    pub url: String,
    #[serde(rename = "profile")]
    pub legacy_profile: Option<String>,
    token_file: PathBuf,
    pub docker: Option<Docker>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Docker {
    pub config: PathBuf,
    pub host: String,
    pub builder: String,
}

impl LocalConnection {
    pub fn load() -> Result<Option<Self>, &'static str> {
        let Some(home) = std::env::var_os("HOME") else {
            return Ok(None);
        };
        let dir = PathBuf::from(home).join("Library/Application Support/io.forgelet.runner");
        let data = match read_private(&dir.join("connection.json")).or_else(|error| {
            if error.kind() == io::ErrorKind::NotFound {
                read_private(&dir.join("profile.json"))
            } else {
                Err(error)
            }
        }) {
            Ok(data) => data,
            Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(None),
            Err(_) => return Err("无法读取本机连接配置，请检查文件及权限（0600）"),
        };
        let config: Self = serde_json::from_str(&data).map_err(|_| "本机连接配置格式无效")?;
        if config.url.trim().is_empty()
            || !config.token_file.is_absolute()
            || config.docker.as_ref().is_some_and(|docker| {
                !docker.config.is_absolute() || docker.host.is_empty() || docker.builder.is_empty()
            })
        {
            return Err("本机连接配置缺少地址或绝对路径");
        }
        Ok(Some(config))
    }

    pub fn token(&self) -> Result<String, &'static str> {
        read_private(&self.token_file)
            .map(|data| data.trim().to_owned())
            .map_err(|_| "无法读取本机设备凭据，请检查文件及权限（0600）")
    }
}

fn read_private(path: &Path) -> io::Result<String> {
    const LIMIT: u64 = 16 * 1024;
    let metadata = fs::symlink_metadata(path)?;
    if !metadata.is_file() || metadata.permissions().mode() & 0o077 != 0 {
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

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::{SystemTime, UNIX_EPOCH};

    #[test]
    fn only_reads_private_regular_files() {
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let path = std::env::temp_dir().join(format!("forgelet-private-profile-{nonce}"));
        fs::create_dir(&path).unwrap();
        assert_eq!(
            read_private(&path).unwrap_err().kind(),
            io::ErrorKind::PermissionDenied
        );
        let file = path.join("test-file");
        fs::write(&file, "fixture").unwrap();
        fs::set_permissions(&file, fs::Permissions::from_mode(0o644)).unwrap();
        assert_eq!(
            read_private(&file).unwrap_err().kind(),
            io::ErrorKind::PermissionDenied
        );
        fs::set_permissions(&file, fs::Permissions::from_mode(0o600)).unwrap();
        assert_eq!(read_private(&file).unwrap(), "fixture");
        let link = path.join("test-link");
        std::os::unix::fs::symlink(&file, &link).unwrap();
        assert_eq!(
            read_private(&link).unwrap_err().kind(),
            io::ErrorKind::PermissionDenied
        );
        fs::write(&file, "x".repeat(16385)).unwrap();
        assert_eq!(
            read_private(&file).unwrap_err().kind(),
            io::ErrorKind::InvalidData
        );
        fs::remove_dir_all(path).unwrap();
    }
}
