// SPDX-License-Identifier: AGPL-3.0-only

//! The upload queue: parts waiting for the server, one directory on disk.
//!
//! A part is two files, `parts/<id>.gz` (the gzip body, sent byte for byte)
//! and `parts/<id>.json` (its headers). The body is written first and each
//! file lands by an atomic rename, so a part is complete exactly when its
//! headers exist. Ids are increasing numbers: the uploader sends parts oldest
//! first. A part the server refuses moves to `rejected/` with the answer.

use std::collections::HashSet;
use std::fs::{self, File};
use std::io::{self, Write};
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use serde::{Deserialize, Serialize};

/// The upload headers of a part, and what the uploader needs to route it.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Header {
    /// The ingest route, after `/v1/ingest/`.
    pub route: String,
    /// X-OHS-Type.
    #[serde(rename = "type")]
    pub data_type: String,
    /// X-OHS-Day.
    pub day: String,
    /// X-OHS-Batch.
    pub batch: String,
    /// X-OHS-Part.
    pub part: u32,
    /// Lines in the part.
    pub lines: u64,
    /// For an activity file: the SHA-256 of the file, recorded as seen once
    /// the server has answered for it.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub file_sha256: Option<String>,
}

pub struct Queue {
    parts: PathBuf,
    rejected: PathBuf,
    last_id: u64,
}

impl Queue {
    /// Opens the queue under the state directory, removing what a crash left
    /// half written.
    pub fn open(state: &Path) -> io::Result<Queue> {
        let parts = state.join("parts");
        let rejected = state.join("rejected");
        fs::create_dir_all(&parts)?;
        fs::create_dir_all(&rejected)?;
        let mut q = Queue {
            parts,
            rejected,
            last_id: 0,
        };
        for entry in fs::read_dir(&q.parts)? {
            let path = entry?.path();
            let name = path
                .file_name()
                .and_then(|n| n.to_str())
                .unwrap_or_default();
            if name.ends_with(".tmp") {
                fs::remove_file(&path)?;
            } else if let Some(id) = name.strip_suffix(".gz").and_then(|s| s.parse::<u64>().ok()) {
                if !q.has(id) {
                    fs::remove_file(&path)?; // a body whose headers never landed
                }
            }
        }
        if let Some(max) = q.list()?.last() {
            q.last_id = *max;
        }
        Ok(q)
    }

    /// A new id, greater than every id handed out or seen: the time in
    /// milliseconds, or the last id plus one.
    pub fn next_id(&mut self) -> u64 {
        let now = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map_or(0, |d| d.as_millis() as u64);
        self.last_id = now.max(self.last_id + 1);
        self.last_id
    }

    /// Records an id in use elsewhere (a spool file), so `next_id` stays above it.
    pub fn observe(&mut self, id: u64) {
        self.last_id = self.last_id.max(id);
    }

    fn body(&self, id: u64) -> PathBuf {
        self.parts.join(format!("{id:016}.gz"))
    }

    fn header(&self, id: u64) -> PathBuf {
        self.parts.join(format!("{id:016}.json"))
    }

    /// Whether the part is queued.
    pub fn has(&self, id: u64) -> bool {
        self.header(id).exists()
    }

    /// Queues a part. Durable when it returns.
    pub fn add(&mut self, id: u64, header: &Header, gz: &[u8]) -> io::Result<()> {
        self.observe(id);
        write_atomic(&self.body(id), gz)?;
        write_atomic(&self.header(id), &serde_json::to_vec(header)?)?;
        sync_dir(&self.parts);
        Ok(())
    }

    /// The queued part ids, oldest first.
    pub fn list(&self) -> io::Result<Vec<u64>> {
        let mut ids = Vec::new();
        for entry in fs::read_dir(&self.parts)? {
            let name = entry?.file_name();
            if let Some(id) = name
                .to_str()
                .and_then(|n| n.strip_suffix(".json"))
                .and_then(|s| s.parse().ok())
            {
                ids.push(id);
            }
        }
        ids.sort_unstable();
        Ok(ids)
    }

    /// A part's headers and body.
    pub fn load(&self, id: u64) -> io::Result<(Header, Vec<u8>)> {
        let h: Header = serde_json::from_slice(&fs::read(self.header(id))?)?;
        Ok((h, fs::read(self.body(id))?))
    }

    /// Removes a part the server stored.
    pub fn remove(&self, id: u64) -> io::Result<()> {
        fs::remove_file(self.header(id))?;
        match fs::remove_file(self.body(id)) {
            Err(e) if e.kind() != io::ErrorKind::NotFound => Err(e),
            _ => Ok(()),
        }
    }

    /// Moves a part the server refused to `rejected/`, with the answer.
    pub fn reject(&self, id: u64, answer: &str) -> io::Result<()> {
        let name = format!("{id:016}");
        fs::write(self.rejected.join(format!("{name}.txt")), answer)?;
        fs::rename(self.body(id), self.rejected.join(format!("{name}.gz")))?;
        fs::rename(self.header(id), self.rejected.join(format!("{name}.json")))
    }

    /// Queued parts and their body bytes.
    pub fn size(&self) -> io::Result<(usize, u64)> {
        let ids = self.list()?;
        let bytes = ids
            .iter()
            .filter_map(|id| fs::metadata(self.body(*id)).ok())
            .map(|m| m.len())
            .sum();
        Ok((ids.len(), bytes))
    }

    /// Parts in `rejected/`.
    pub fn rejected(&self) -> usize {
        fs::read_dir(&self.rejected)
            .map(|d| {
                d.filter_map(Result::ok)
                    .filter(|e| e.path().extension().is_some_and(|x| x == "json"))
                    .count()
            })
            .unwrap_or(0)
    }

    /// The SHA-256 of every activity file waiting in the queue.
    pub fn queued_files(&self) -> io::Result<HashSet<String>> {
        let mut set = HashSet::new();
        for id in self.list()? {
            if let Ok(b) = fs::read(self.header(id)) {
                if let Ok(Header {
                    file_sha256: Some(h),
                    ..
                }) = serde_json::from_slice(&b)
                {
                    set.insert(h);
                }
            }
        }
        Ok(set)
    }
}

/// Writes a file by a synced temporary file and a rename: readers see the old
/// file or the whole new one.
pub fn write_atomic(path: &Path, bytes: &[u8]) -> io::Result<()> {
    let mut tmp = path.as_os_str().to_owned();
    tmp.push(".tmp");
    let tmp = PathBuf::from(tmp);
    let mut f = File::create(&tmp)?;
    f.write_all(bytes)?;
    f.sync_all()?;
    drop(f);
    fs::rename(&tmp, path)
}

/// Makes renames in a directory durable, where the platform allows it.
pub fn sync_dir(dir: &Path) {
    #[cfg(unix)]
    if let Ok(d) = File::open(dir) {
        let _ = d.sync_all();
    }
    #[cfg(not(unix))]
    let _ = dir;
}

#[cfg(test)]
pub mod tests {
    use super::*;

    /// A fresh directory under the system temp directory, removed on drop.
    pub struct TempDir(pub PathBuf);

    impl TempDir {
        pub fn new(name: &str) -> TempDir {
            let p = std::env::temp_dir().join(format!(
                "ohs-bridge-test-{name}-{}",
                crate::codec::random_id()
            ));
            fs::create_dir_all(&p).unwrap();
            TempDir(p)
        }
    }

    impl Drop for TempDir {
        fn drop(&mut self) {
            let _ = fs::remove_dir_all(&self.0);
        }
    }

    pub fn header(batch: &str) -> Header {
        Header {
            route: "bluetooth".into(),
            data_type: "gatt".into(),
            day: "2026-10-16".into(),
            batch: batch.into(),
            part: 0,
            lines: 1,
            file_sha256: None,
        }
    }

    #[test]
    fn parts_come_back_oldest_first() {
        let dir = TempDir::new("queue");
        let mut q = Queue::open(&dir.0).unwrap();
        let a = q.next_id();
        let b = q.next_id();
        assert!(b > a);
        q.add(b, &header("b"), b"bbb").unwrap();
        q.add(a, &header("a"), b"aaaa").unwrap();
        assert_eq!(q.list().unwrap(), vec![a, b]);
        assert_eq!(q.size().unwrap(), (2, 7));
        let (h, body) = q.load(a).unwrap();
        assert_eq!(h, header("a"));
        assert_eq!(body, b"aaaa");

        q.remove(a).unwrap();
        q.reject(b, "400 Bad Request").unwrap();
        assert!(q.list().unwrap().is_empty());
        assert_eq!(q.rejected(), 1);
        assert_eq!(
            fs::read_to_string(dir.0.join("rejected").join(format!("{b:016}.txt"))).unwrap(),
            "400 Bad Request"
        );
    }

    #[test]
    fn ids_stay_above_what_is_on_disk() {
        let dir = TempDir::new("queue-ids");
        let mut q = Queue::open(&dir.0).unwrap();
        let far = q.next_id() + 1_000_000;
        q.add(far, &header("far"), b"x").unwrap();
        let mut q = Queue::open(&dir.0).unwrap();
        assert!(q.next_id() > far);
        q.observe(far + 10);
        assert_eq!(q.next_id(), far + 11);
    }

    #[test]
    fn half_written_parts_are_dropped_on_open() {
        let dir = TempDir::new("queue-crash");
        let parts = dir.0.join("parts");
        fs::create_dir_all(&parts).unwrap();
        fs::write(parts.join("0000000000000001.gz"), b"orphan body").unwrap();
        fs::write(parts.join("0000000000000002.gz.tmp"), b"tmp").unwrap();
        let q = Queue::open(&dir.0).unwrap();
        assert!(q.list().unwrap().is_empty());
        assert_eq!(fs::read_dir(&parts).unwrap().count(), 0);
    }

    #[test]
    fn queued_files_by_hash() {
        let dir = TempDir::new("queue-files");
        let mut q = Queue::open(&dir.0).unwrap();
        let mut h = header("af1-x");
        h.file_sha256 = Some("ab".repeat(32));
        let id = q.next_id();
        q.add(id, &h, b"x").unwrap();
        let id = q.next_id();
        q.add(id, &header("bt1-x"), b"y").unwrap();
        assert_eq!(q.queued_files().unwrap(), HashSet::from(["ab".repeat(32)]));
    }
}
