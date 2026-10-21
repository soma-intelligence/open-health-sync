// SPDX-License-Identifier: AGPL-3.0-only

//! Activity files from folders: a watch mounted over USB (Garmin, COROS and
//! others keep their workouts as `.fit` files). Each new file is queued as
//! one part for `POST /v1/ingest/activity-files`, with the line and batch of
//! `ohs import files`, so the server answers a file sent both ways as a
//! duplicate. A file the server has answered for is remembered by its
//! SHA-256 in `files.seen` and not sent again. File names are never printed
//! or sent.

use std::collections::{HashMap, HashSet};
use std::fs::{self, File, OpenOptions};
use std::io::{self, BufRead, BufReader, Write};
use std::path::{Path, PathBuf};
use std::time::SystemTime;

use chrono::{DateTime, Utc};
use sha2::{Digest, Sha256};

use crate::codec;
use crate::queue::{Header, Queue};
use crate::spool::gzip;

/// The largest file sent, the server's limit.
pub const MAX_FILE_BYTES: u64 = 20 << 20;

/// The batch prefix of `ohs import files`: the same file gets the same batch.
const BATCH_PREFIX: &str = "af1-";

/// How deep a folder is walked.
const MAX_DEPTH: usize = 8;

/// The SHA-256 of every file the server has answered for.
pub struct Seen {
    file: File,
    set: HashSet<String>,
}

impl Seen {
    pub fn load(state: &Path) -> io::Result<Seen> {
        let path = state.join("files.seen");
        let mut set = HashSet::new();
        if let Ok(f) = File::open(&path) {
            for line in BufReader::new(f).lines() {
                let line = line?;
                let h = line.trim();
                if h.len() == 64 && h.bytes().all(|c| c.is_ascii_hexdigit()) {
                    set.insert(h.to_string());
                }
            }
        }
        let file = OpenOptions::new().create(true).append(true).open(&path)?;
        Ok(Seen { file, set })
    }

    pub fn contains(&self, sha256: &str) -> bool {
        self.set.contains(sha256)
    }

    pub fn record(&mut self, sha256: &str) -> io::Result<()> {
        if self.set.insert(sha256.to_string()) {
            self.file.write_all(format!("{sha256}\n").as_bytes())?;
            self.file.sync_data()?;
        }
        Ok(())
    }

    #[cfg(test)]
    pub fn len(&self) -> usize {
        self.set.len()
    }
}

/// What a scan found.
#[derive(Debug, Default, PartialEq)]
pub struct Scan {
    /// New files queued.
    pub queued: u64,
    /// Files already sent or queued.
    pub known: u64,
    /// Files over 20 MiB, or empty.
    pub skipped: u64,
    /// Files that could not be read.
    pub unreadable: u64,
}

/// Finds new `.fit` files in folders and queues them. It remembers the size
/// and modification time of files it has handled, so a watch loop reads a
/// file once.
#[derive(Default)]
pub struct Scanner {
    handled: HashMap<PathBuf, (u64, Option<SystemTime>)>,
}

impl Scanner {
    pub fn scan(&mut self, dirs: &[PathBuf], queue: &mut Queue, seen: &Seen) -> io::Result<Scan> {
        let mut found = Vec::new();
        for d in dirs {
            walk(d, 0, &mut found);
        }
        found.sort();
        let mut queued = queue.queued_files()?;
        let mut scan = Scan::default();
        for path in found {
            let Ok(meta) = fs::metadata(&path) else {
                scan.unreadable += 1;
                continue;
            };
            let stamp = (meta.len(), meta.modified().ok());
            if self.handled.get(&path) == Some(&stamp) {
                scan.known += 1;
                continue;
            }
            if meta.len() == 0 || meta.len() > MAX_FILE_BYTES {
                scan.skipped += 1;
                self.handled.insert(path, stamp);
                continue;
            }
            let Ok(data) = fs::read(&path) else {
                scan.unreadable += 1;
                continue;
            };
            let sha = codec::hex(&Sha256::digest(&data));
            if seen.contains(&sha) || queued.contains(&sha) {
                scan.known += 1;
            } else {
                let (header, gz) = part(&data, &sha, stamp.1)?;
                let id = queue.next_id();
                queue.add(id, &header, &gz)?;
                queued.insert(sha);
                scan.queued += 1;
            }
            self.handled.insert(path, stamp);
        }
        Ok(scan)
    }
}

/// The part of one FIT file: the line of `ohs import files`, batch
/// `af1-<first 32 hex of its SHA-256>`, part 0, day the UTC day it was
/// last modified.
fn part(data: &[u8], sha: &str, modified: Option<SystemTime>) -> io::Result<(Header, Vec<u8>)> {
    let line = format!(
        "{{\"type\":\"fit\",\"data\":\"{}\"}}\n",
        codec::base64(data)
    );
    let day: DateTime<Utc> = modified.map_or_else(Utc::now, DateTime::from);
    let header = Header {
        route: "activity-files".into(),
        data_type: "fit".into(),
        day: day.format("%Y-%m-%d").to_string(),
        batch: format!("{BATCH_PREFIX}{}", &sha[..32]),
        part: 0,
        lines: 1,
        file_sha256: Some(sha.to_string()),
    };
    Ok((header, gzip(line.as_bytes())?))
}

/// Collects `.fit` files (any case) under a folder. Links are not followed.
fn walk(dir: &Path, depth: usize, out: &mut Vec<PathBuf>) {
    let Ok(entries) = fs::read_dir(dir) else {
        return;
    };
    for e in entries.flatten() {
        let Ok(t) = e.file_type() else {
            continue;
        };
        let path = e.path();
        if t.is_dir() && depth < MAX_DEPTH {
            walk(&path, depth + 1, out);
        } else if t.is_file()
            && path
                .extension()
                .is_some_and(|x| x.eq_ignore_ascii_case("fit"))
        {
            out.push(path);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::queue::tests::TempDir;
    use flate2::read::GzDecoder;
    use std::io::Read;

    #[test]
    fn new_files_are_queued_once() {
        let state = TempDir::new("files-state");
        let watch = TempDir::new("files-watch");
        let activity = watch.0.join("GARMIN").join("Activity");
        fs::create_dir_all(&activity).unwrap();
        fs::write(activity.join("A1.FIT"), b"\x0e\x10fit bytes").unwrap();
        fs::write(activity.join("copy.fit"), b"\x0e\x10fit bytes").unwrap(); // same content
        fs::write(activity.join("B2.fit"), b"other").unwrap();
        fs::write(activity.join("empty.fit"), b"").unwrap();
        fs::write(activity.join("notes.txt"), b"x").unwrap();

        let mut q = Queue::open(&state.0).unwrap();
        let mut seen = Seen::load(&state.0).unwrap();
        let mut sc = Scanner::default();
        let dirs = vec![watch.0.clone()];
        let s = sc.scan(&dirs, &mut q, &seen).unwrap();
        assert_eq!(
            s,
            Scan {
                queued: 2,
                known: 1,
                skipped: 1,
                unreadable: 0
            }
        );

        let ids = q.list().unwrap();
        let sha = codec::hex(&Sha256::digest(b"\x0e\x10fit bytes"));
        let (h, gz) = q.load(ids[0]).unwrap();
        assert_eq!(h.route, "activity-files");
        assert_eq!(h.data_type, "fit");
        assert_eq!(h.batch, format!("af1-{}", &sha[..32]));
        assert_eq!(h.part, 0);
        assert_eq!(h.day.len(), 10);
        assert_eq!(h.file_sha256.as_deref(), Some(sha.as_str()));
        let mut body = String::new();
        GzDecoder::new(&gz[..]).read_to_string(&mut body).unwrap();
        assert_eq!(
            body,
            format!(
                "{{\"type\":\"fit\",\"data\":\"{}\"}}\n",
                codec::base64(b"\x0e\x10fit bytes")
            )
        );

        // Again: nothing new, in this process or the next.
        assert_eq!(sc.scan(&dirs, &mut q, &seen).unwrap().queued, 0);
        assert_eq!(
            Scanner::default()
                .scan(&dirs, &mut q, &seen)
                .unwrap()
                .queued,
            0
        );

        // Once answered, the hash is remembered across runs.
        for id in q.list().unwrap() {
            let (h, _) = q.load(id).unwrap();
            seen.record(h.file_sha256.as_deref().unwrap()).unwrap();
            q.remove(id).unwrap();
        }
        let seen = Seen::load(&state.0).unwrap();
        assert_eq!(seen.len(), 2);
        assert_eq!(
            Scanner::default()
                .scan(&dirs, &mut q, &seen)
                .unwrap()
                .queued,
            0
        );
        assert!(q.list().unwrap().is_empty());
    }

    #[test]
    fn missing_folder_is_empty() {
        let state = TempDir::new("files-missing");
        let mut q = Queue::open(&state.0).unwrap();
        let seen = Seen::load(&state.0).unwrap();
        let s = Scanner::default()
            .scan(&[state.0.join("nope")], &mut q, &seen)
            .unwrap();
        assert_eq!(s, Scan::default());
    }
}
