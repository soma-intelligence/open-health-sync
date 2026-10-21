// SPDX-License-Identifier: AGPL-3.0-only

//! The bridge's status: `status.json` in the state directory, read by
//! `ohs-bridge status`, and an optional one-line file for a menu bar or a
//! panel (`--status-file`). Both stay on this computer.

use std::fs::{self, File};
use std::io::{self, Write};
use std::path::{Path, PathBuf};

use chrono::{DateTime, Local, Utc};
use serde::{Deserialize, Serialize};

#[derive(Debug, Default, Clone, PartialEq, Serialize, Deserialize)]
pub struct Status {
    /// When the bridge last wrote this.
    pub updated: Option<DateTime<Utc>>,
    /// Whether the daemon is running (false once it stops cleanly).
    pub running: bool,
    pub queued_parts: usize,
    pub queued_bytes: u64,
    pub rejected_parts: usize,
    pub sent_parts: u64,
    pub last_upload: Option<DateTime<Utc>>,
    /// Why uploads are not going through, while they are not.
    pub problem: Option<String>,
    pub devices: Vec<DeviceStatus>,
}

#[derive(Debug, Default, Clone, PartialEq, Serialize, Deserialize)]
pub struct DeviceStatus {
    pub name: String,
    pub connected: bool,
    pub last_value: Option<DateTime<Utc>>,
    pub values: u64,
    pub heart_rate: Option<u16>,
    pub battery: Option<u8>,
}

impl Status {
    pub fn load(state: &Path) -> Option<Status> {
        serde_json::from_slice(&fs::read(state.join("status.json")).ok()?).ok()
    }

    /// Writes `status.json`, and the one-line file when asked.
    pub fn save(&self, state: &Path, line_file: Option<&Path>) -> io::Result<()> {
        replace(
            &state.join("status.json"),
            &serde_json::to_vec_pretty(self)?,
        )?;
        if let Some(p) = line_file {
            replace(p, format!("{}\n", self.line()).as_bytes())?;
        }
        Ok(())
    }

    /// Whether two statuses differ only in when they were written.
    pub fn same_as(&self, other: &Status) -> bool {
        Status {
            updated: other.updated,
            ..self.clone()
        } == *other
    }

    /// One line: live heart rate when a strap is connected, devices, queue,
    /// last upload or the problem.
    pub fn line(&self) -> String {
        if !self.running {
            return match self.queued_parts {
                0 => "OHS stopped".into(),
                n => format!("OHS stopped · {n} queued"),
            };
        }
        let mut parts = Vec::new();
        if let Some(hr) = self
            .devices
            .iter()
            .filter(|d| d.connected)
            .find_map(|d| d.heart_rate)
        {
            parts.push(format!("♥ {hr}"));
        }
        let connected = self.devices.iter().filter(|d| d.connected).count();
        parts.push(match connected {
            1 => "1 device".to_string(),
            n => format!("{n} devices"),
        });
        if self.queued_parts > 0 {
            parts.push(format!("{} queued", self.queued_parts));
        }
        match (&self.problem, self.last_upload) {
            (Some(_), _) => parts.push("not sending".into()),
            (None, Some(t)) => {
                parts.push(format!("sent {}", t.with_timezone(&Local).format("%H:%M")))
            }
            (None, None) => {}
        }
        format!("OHS {}", parts.join(" · "))
    }
}

/// Replaces a file through a rename, so a reader never sees half of it. Not
/// synced: a status needs no durability, and an SD card is spared a sync
/// every few seconds.
fn replace(path: &Path, bytes: &[u8]) -> io::Result<()> {
    let mut tmp = path.as_os_str().to_owned();
    tmp.push(".tmp");
    let tmp = PathBuf::from(tmp);
    File::create(&tmp)?.write_all(bytes)?;
    fs::rename(&tmp, path)
}

/// `ohs-bridge status`: what the daemon last wrote, and the queue on disk.
pub fn print(state: &Path, queued: (usize, u64), rejected: usize) -> String {
    let mut out = String::new();
    let local = |t: DateTime<Utc>| {
        t.with_timezone(&Local)
            .format("%Y-%m-%d %H:%M:%S")
            .to_string()
    };
    out.push_str(&format!("state      {}\n", state.display()));
    out.push_str(&format!(
        "queue      {} waiting, {} KiB\n",
        queued.0,
        queued.1.div_ceil(1024)
    ));
    if rejected > 0 {
        out.push_str(&format!(
            "rejected   {rejected} (see {})\n",
            state.join("rejected").display()
        ));
    }
    let Some(s) = Status::load(state) else {
        out.push_str("daemon     no status yet: start `ohs-bridge run`\n");
        return out;
    };
    let when = s.updated.map(local).unwrap_or_default();
    out.push_str(&format!(
        "daemon     {} (updated {when})\n",
        if s.running { "running" } else { "stopped" }
    ));
    match s.last_upload {
        Some(t) => out.push_str(&format!("last sent  {}\n", local(t))),
        None => out.push_str("last sent  never\n"),
    }
    if let Some(p) = &s.problem {
        out.push_str(&format!("problem    {p}\n"));
    }
    for d in &s.devices {
        let mut l = format!(
            "device     {}: {}",
            d.name,
            if d.connected {
                "connected"
            } else {
                "not connected"
            }
        );
        if let Some(t) = d.last_value {
            l.push_str(&format!(", last value {}", local(t)));
        }
        if let Some(hr) = d.heart_rate.filter(|_| d.connected) {
            l.push_str(&format!(", {hr} bpm"));
        }
        if let Some(b) = d.battery {
            l.push_str(&format!(", battery {b}%"));
        }
        out.push_str(&l);
        out.push('\n');
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::queue::tests::TempDir;

    #[test]
    fn one_line() {
        let mut s = Status {
            running: true,
            ..Default::default()
        };
        assert_eq!(s.line(), "OHS 0 devices");
        s.devices = vec![
            DeviceStatus {
                name: "Polar H10".into(),
                connected: true,
                heart_rate: Some(72),
                ..Default::default()
            },
            DeviceStatus {
                name: "BLEsmart".into(),
                connected: false,
                heart_rate: Some(64),
                ..Default::default()
            },
        ];
        s.queued_parts = 2;
        s.problem = Some("the server is busy (503)".into());
        assert_eq!(s.line(), "OHS ♥ 72 · 1 device · 2 queued · not sending");
        s.devices[0].connected = false;
        s.problem = None;
        s.queued_parts = 0;
        assert_eq!(s.line(), "OHS 0 devices");
        s.running = false;
        assert_eq!(s.line(), "OHS stopped");
        s.queued_parts = 1;
        assert_eq!(s.line(), "OHS stopped · 1 queued");
    }

    #[test]
    fn same_but_for_the_time() {
        let a = Status {
            updated: Some(Utc::now()),
            running: true,
            ..Default::default()
        };
        let mut b = Status {
            updated: None,
            running: true,
            ..Default::default()
        };
        assert!(a.same_as(&b));
        b.queued_parts = 1;
        assert!(!a.same_as(&b));
    }

    #[test]
    fn saved_and_printed() {
        let dir = TempDir::new("status");
        assert!(print(&dir.0, (0, 0), 0).contains("no status yet"));
        let s = Status {
            updated: Some(Utc::now()),
            running: true,
            last_upload: Some(Utc::now()),
            devices: vec![DeviceStatus {
                name: "Polar H10".into(),
                connected: true,
                battery: Some(80),
                ..Default::default()
            }],
            ..Default::default()
        };
        let line = dir.0.join("line.txt");
        s.save(&dir.0, Some(&line)).unwrap();
        assert!(fs::read_to_string(&line)
            .unwrap()
            .starts_with("OHS 1 device · sent "));
        let out = print(&dir.0, (3, 2048), 1);
        assert!(out.contains("queue      3 waiting, 2 KiB"), "{out}");
        assert!(out.contains("rejected   1 (see"), "{out}");
        assert!(out.contains("daemon     running"), "{out}");
        assert!(
            out.contains("device     Polar H10: connected, battery 80%"),
            "{out}"
        );
    }
}
