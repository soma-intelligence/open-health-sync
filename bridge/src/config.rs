// SPDX-License-Identifier: AGPL-3.0-only

//! Where the bridge keeps its state, and the server it sends to.

use std::env;
use std::fs::{self, File};
use std::io;
use std::path::{Path, PathBuf};

use crate::codec;
use crate::queue::write_atomic;

/// The state directory: OHS_BRIDGE_STATE, else the platform's place for
/// per-user application state.
pub fn state_dir() -> Result<PathBuf, String> {
    if let Some(p) = env::var_os("OHS_BRIDGE_STATE").filter(|p| !p.is_empty()) {
        return Ok(PathBuf::from(p));
    }
    let home = || {
        env::var_os("HOME")
            .filter(|h| !h.is_empty())
            .map(PathBuf::from)
    };
    let base = if cfg!(target_os = "macos") {
        home().map(|h| h.join("Library").join("Application Support"))
    } else if cfg!(windows) {
        env::var_os("LOCALAPPDATA")
            .filter(|p| !p.is_empty())
            .map(PathBuf::from)
    } else {
        env::var_os("XDG_STATE_HOME")
            .filter(|p| !p.is_empty())
            .map(PathBuf::from)
            .or_else(|| home().map(|h| h.join(".local").join("state")))
    };
    base.map(|b| b.join("ohs-bridge"))
        .ok_or_else(|| "no home directory: set OHS_BRIDGE_STATE".to_string())
}

/// Creates the state directory, readable by its owner only.
pub fn ensure_state(dir: &Path) -> io::Result<()> {
    fs::create_dir_all(dir)?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(dir, fs::Permissions::from_mode(0o700))?;
    }
    Ok(())
}

/// Holds the state directory for this process: two bridges on one queue
/// would send parts twice. Released when the file is dropped.
pub fn lock(dir: &Path) -> Result<File, String> {
    // Not truncated: on Windows a file another process holds locked may not be.
    let f = fs::OpenOptions::new()
        .create(true)
        .truncate(false)
        .write(true)
        .open(dir.join("lock"))
        .map_err(|e| format!("cannot open the lock file: {e}"))?;
    match f.try_lock() {
        Ok(()) => Ok(f),
        Err(fs::TryLockError::WouldBlock) => {
            Err("another ohs-bridge is running on this state directory (stop it, or set OHS_BRIDGE_STATE)".into())
        }
        Err(fs::TryLockError::Error(e)) => Err(format!("cannot lock the state directory: {e}")),
    }
}

/// This installation's id, made on first use: it keeps batch ids of two
/// computers of one user apart.
pub fn install_id(dir: &Path) -> io::Result<String> {
    let path = dir.join("install-id");
    if let Ok(s) = fs::read_to_string(&path) {
        let s = s.trim();
        if s.len() == 16 && s.bytes().all(|c| c.is_ascii_hexdigit()) {
            return Ok(s.to_string());
        }
    }
    let id = codec::random_id();
    write_atomic(&path, format!("{id}\n").as_bytes())?;
    Ok(id)
}

/// The server: its base URL, the upload token, and whether it is this computer.
pub struct Server {
    pub base: String,
    pub token: String,
    pub loopback: bool,
}

/// Reads OHS_URL and OHS_TOKEN.
pub fn server(allow_http: bool) -> Result<Server, String> {
    let url = env::var("OHS_URL").unwrap_or_default();
    let token = env::var("OHS_TOKEN").unwrap_or_default().trim().to_string();
    let (base, loopback) = base_url(&url, allow_http)?;
    if token.is_empty() {
        return Err("OHS_TOKEN is required: a user token (ohs_ut_) with the upload scope".into());
    }
    Ok(Server {
        base,
        token,
        loopback,
    })
}

/// Checks a base URL: http or https, a host, no user or password, no query.
/// Plain http only to this computer, or with --allow-http. Returns the URL
/// without a trailing slash, and whether the host is a loopback one.
pub fn base_url(url: &str, allow_http: bool) -> Result<(String, bool), String> {
    let url = url.trim();
    if url.is_empty() {
        return Err(
            "OHS_URL is required: the base URL of the server, for example https://ohs.example.org"
                .into(),
        );
    }
    let (https, rest) = if let Some(r) = url.strip_prefix("https://") {
        (true, r)
    } else if let Some(r) = url.strip_prefix("http://") {
        (false, r)
    } else {
        return Err("OHS_URL must be an http or https URL".into());
    };
    if rest.contains(['?', '#']) {
        return Err("OHS_URL must not hold a query or a fragment".into());
    }
    let authority = rest.split('/').next().unwrap_or_default();
    if authority.contains('@') {
        return Err("OHS_URL must not hold a user or password: the token goes in OHS_TOKEN".into());
    }
    let host = if let Some(v6) = authority.strip_prefix('[') {
        v6.split(']').next().unwrap_or_default()
    } else {
        authority.split(':').next().unwrap_or_default()
    };
    if host.is_empty() {
        return Err("OHS_URL has no host".into());
    }
    let loopback = loopback(host);
    if !https && !loopback && !allow_http {
        return Err(
            "OHS_URL sends the token in clear: use https, or --allow-http on a network you trust"
                .into(),
        );
    }
    Ok((url.trim_end_matches('/').to_string(), loopback))
}

fn loopback(host: &str) -> bool {
    let host = host.to_ascii_lowercase();
    if host == "localhost" || host.ends_with(".localhost") || host == "::1" {
        return true;
    }
    host.parse::<std::net::Ipv4Addr>()
        .is_ok_and(|ip| ip.is_loopback())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::queue::tests::TempDir;

    #[test]
    fn base_urls() {
        assert_eq!(
            base_url("https://ohs.example.org/", false),
            Ok(("https://ohs.example.org".into(), false))
        );
        assert_eq!(
            base_url(" https://ohs.example.org/base ", false),
            Ok(("https://ohs.example.org/base".into(), false))
        );
        assert_eq!(
            base_url("http://localhost:8080", false),
            Ok(("http://localhost:8080".into(), true))
        );
        assert_eq!(
            base_url("http://127.0.0.1:8080", false),
            Ok(("http://127.0.0.1:8080".into(), true))
        );
        assert_eq!(
            base_url("http://[::1]:8080", false),
            Ok(("http://[::1]:8080".into(), true))
        );
        assert_eq!(
            base_url("http://pi.local:8080", true),
            Ok(("http://pi.local:8080".into(), false))
        );
        for bad in [
            "",
            "ohs.example.org",
            "ftp://ohs.example.org",
            "http://pi.local:8080",
            "https://user:pass@ohs.example.org",
            "https://ohs.example.org/?x=1",
            "https:///path",
        ] {
            assert!(base_url(bad, false).is_err(), "{bad}");
        }
    }

    #[test]
    fn install_id_is_kept() {
        let dir = TempDir::new("config");
        let a = install_id(&dir.0).unwrap();
        assert_eq!(a.len(), 16);
        assert_eq!(install_id(&dir.0).unwrap(), a);
    }

    #[test]
    fn one_bridge_per_state_directory() {
        let dir = TempDir::new("config-lock");
        let held = lock(&dir.0).unwrap();
        assert!(lock(&dir.0).unwrap_err().contains("another ohs-bridge"));
        drop(held);
        assert!(lock(&dir.0).is_ok());
    }

    #[cfg(unix)]
    #[test]
    fn state_is_private() {
        use std::os::unix::fs::PermissionsExt;
        let dir = TempDir::new("config-mode");
        let state = dir.0.join("state");
        ensure_state(&state).unwrap();
        assert_eq!(
            fs::metadata(&state).unwrap().permissions().mode() & 0o777,
            0o700
        );
    }
}
