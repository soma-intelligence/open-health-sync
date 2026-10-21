// SPDX-License-Identifier: AGPL-3.0-only

//! The spool: lines are appended to `spool/<id>.ndjson` as values arrive,
//! and sealed into a queued part every minute or 5,000 lines. A spool file
//! left by a crash is sealed when the spool opens again. Its id becomes the
//! part's id, so sealing twice (a crash between queuing the part and removing
//! the spool file) queues one part.

use std::fs::{self, File, OpenOptions};
use std::io::{self, Write};
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};

use chrono::Utc;
use flate2::write::GzEncoder;
use flate2::Compression;

use crate::line;
use crate::queue::{Header, Queue};

/// How long a spool file stays open.
pub const ROTATE_AFTER: Duration = Duration::from_secs(60);

/// How many lines a spool file holds at most.
pub const ROTATE_LINES: u64 = 5_000;

/// The batch prefix of Bluetooth parts. Bump it when the line format changes,
/// so new parts never collide with stored ones.
pub const BATCH_PREFIX: &str = "bt1-";

pub struct Spool {
    dir: PathBuf,
    install: String,
    current: Option<Current>,
}

struct Current {
    id: u64,
    file: File,
    lines: u64,
    opened: Instant,
}

impl Spool {
    /// Opens the spool under the state directory and seals what a previous
    /// run left in it.
    pub fn open(state: &Path, install: &str, queue: &mut Queue) -> io::Result<Spool> {
        let dir = state.join("spool");
        fs::create_dir_all(&dir)?;
        let mut left = Vec::new();
        for entry in fs::read_dir(&dir)? {
            let name = entry?.file_name();
            if let Some(id) = name
                .to_str()
                .and_then(|n| n.strip_suffix(".ndjson"))
                .and_then(|s| s.parse::<u64>().ok())
            {
                queue.observe(id);
                left.push(id);
            }
        }
        let s = Spool {
            dir,
            install: install.to_string(),
            current: None,
        };
        left.sort_unstable();
        for id in left {
            s.seal(id, queue)?;
        }
        Ok(s)
    }

    fn path(&self, id: u64) -> PathBuf {
        self.dir.join(format!("{id:016}.ndjson"))
    }

    /// Appends one line (newline included). The line is in the operating
    /// system's hands when this returns: a crash of the bridge loses nothing.
    pub fn append(&mut self, line: &str, queue: &mut Queue) -> io::Result<()> {
        if self.current.is_none() {
            let id = queue.next_id();
            let file = OpenOptions::new()
                .create_new(true)
                .append(true)
                .open(self.path(id))?;
            self.current = Some(Current {
                id,
                file,
                lines: 0,
                opened: Instant::now(),
            });
        }
        let c = self.current.as_mut().expect("opened above");
        c.file.write_all(line.as_bytes())?;
        c.lines += 1;
        if c.lines >= ROTATE_LINES {
            self.rotate(queue)?;
        }
        Ok(())
    }

    /// Seals the open spool file when it is a minute old. True when a part
    /// was queued.
    pub fn rotate_if_due(&mut self, queue: &mut Queue) -> io::Result<bool> {
        match &self.current {
            Some(c) if c.opened.elapsed() >= ROTATE_AFTER => self.rotate(queue),
            _ => Ok(false),
        }
    }

    /// Seals the open spool file now. True when a part was queued.
    pub fn rotate(&mut self, queue: &mut Queue) -> io::Result<bool> {
        let Some(c) = self.current.take() else {
            return Ok(false);
        };
        c.file.sync_all()?;
        drop(c.file);
        self.seal(c.id, queue)
    }

    /// Turns a spool file into a queued part and removes it.
    fn seal(&self, id: u64, queue: &mut Queue) -> io::Result<bool> {
        let path = self.path(id);
        if queue.has(id) {
            fs::remove_file(&path)?;
            return Ok(false);
        }
        let mut bytes = fs::read(&path)?;
        // A crash mid-write leaves a partial last line: drop it.
        bytes.truncate(bytes.iter().rposition(|b| *b == b'\n').map_or(0, |i| i + 1));
        if bytes.is_empty() {
            fs::remove_file(&path)?;
            return Ok(false);
        }
        let lines = bytes.iter().filter(|b| **b == b'\n').count() as u64;
        let first = bytes.split(|b| *b == b'\n').next().unwrap_or_default();
        let day = std::str::from_utf8(first)
            .ok()
            .and_then(line::utc_day)
            .unwrap_or_else(|| Utc::now().format("%Y-%m-%d").to_string());
        let header = Header {
            route: "bluetooth".into(),
            data_type: "gatt".into(),
            day,
            batch: format!("{BATCH_PREFIX}{}-{id}", self.install),
            part: 0,
            lines,
            file_sha256: None,
        };
        queue.add(id, &header, &gzip(&bytes)?)?;
        fs::remove_file(&path)?;
        Ok(true)
    }
}

/// Gzip of `bytes`.
pub fn gzip(bytes: &[u8]) -> io::Result<Vec<u8>> {
    let mut e = GzEncoder::new(
        Vec::with_capacity(bytes.len() / 4 + 64),
        Compression::default(),
    );
    e.write_all(bytes)?;
    e.finish()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::queue::tests::TempDir;
    use flate2::read::GzDecoder;
    use std::io::Read;

    const L1: &str = "{\"at\":\"2026-10-16T23:30:00.000-02:00\",\"char\":\"2a37\",\"value\":\"0048\",\"session\":\"abcdefgh\"}\n";
    const L2: &str = "{\"at\":\"2026-10-17T01:31:00.000+00:00\",\"char\":\"2a37\",\"value\":\"0049\",\"session\":\"abcdefgh\"}\n";

    fn gunzip(b: &[u8]) -> String {
        let mut s = String::new();
        GzDecoder::new(b).read_to_string(&mut s).unwrap();
        s
    }

    #[test]
    fn lines_become_one_part() {
        let dir = TempDir::new("spool");
        let mut q = Queue::open(&dir.0).unwrap();
        let mut s = Spool::open(&dir.0, "0123456789abcdef", &mut q).unwrap();
        assert!(!s.rotate(&mut q).unwrap(), "nothing open");
        s.append(L1, &mut q).unwrap();
        s.append(L2, &mut q).unwrap();
        assert!(!s.rotate_if_due(&mut q).unwrap(), "not a minute old");
        assert!(q.list().unwrap().is_empty());
        assert!(s.rotate(&mut q).unwrap());

        let ids = q.list().unwrap();
        assert_eq!(ids.len(), 1);
        let (h, gz) = q.load(ids[0]).unwrap();
        assert_eq!(h.route, "bluetooth");
        assert_eq!(h.data_type, "gatt");
        assert_eq!(h.day, "2026-10-17", "the UTC day of the first line");
        assert_eq!(h.batch, format!("bt1-0123456789abcdef-{}", ids[0]));
        assert_eq!(h.part, 0);
        assert_eq!(h.lines, 2);
        assert_eq!(gunzip(&gz), format!("{L1}{L2}"));
        assert_eq!(fs::read_dir(dir.0.join("spool")).unwrap().count(), 0);

        s.append(L1, &mut q).unwrap();
        s.rotate(&mut q).unwrap();
        let ids2 = q.list().unwrap();
        assert_eq!(ids2.len(), 2);
        assert!(ids2[1] > ids[0]);
    }

    #[test]
    fn rotates_at_the_line_cap() {
        let dir = TempDir::new("spool-cap");
        let mut q = Queue::open(&dir.0).unwrap();
        let mut s = Spool::open(&dir.0, "0123456789abcdef", &mut q).unwrap();
        for _ in 0..ROTATE_LINES + 1 {
            s.append(L1, &mut q).unwrap();
        }
        assert_eq!(q.list().unwrap().len(), 1);
        s.rotate(&mut q).unwrap();
        let ids = q.list().unwrap();
        assert_eq!(q.load(ids[0]).unwrap().0.lines, ROTATE_LINES);
        assert_eq!(q.load(ids[1]).unwrap().0.lines, 1);
    }

    #[test]
    fn a_crash_leaves_nothing_behind() {
        let dir = TempDir::new("spool-crash");
        let spool = dir.0.join("spool");
        fs::create_dir_all(&spool).unwrap();
        // A spool file cut mid-line, and one whose part was already queued.
        fs::write(
            spool.join("0000000000000007.ndjson"),
            format!("{L1}{{\"at\":\"2026"),
        )
        .unwrap();
        fs::write(spool.join("0000000000000003.ndjson"), L2).unwrap();
        fs::write(spool.join("0000000000000009.ndjson"), "{\"at\"").unwrap();
        let mut q = Queue::open(&dir.0).unwrap();
        q.add(3, &crate::queue::tests::header("already"), b"queued")
            .unwrap();

        let mut s = Spool::open(&dir.0, "0123456789abcdef", &mut q).unwrap();
        assert_eq!(fs::read_dir(&spool).unwrap().count(), 0);
        assert_eq!(q.list().unwrap(), vec![3, 7]);
        assert_eq!(q.load(3).unwrap().1, b"queued", "not sealed twice");
        let (h, gz) = q.load(7).unwrap();
        assert_eq!(h.lines, 1);
        assert_eq!(gunzip(&gz), L1);

        s.append(L2, &mut q).unwrap();
        s.rotate(&mut q).unwrap();
        assert!(
            *q.list().unwrap().last().unwrap() > 9,
            "new ids stay above the spool's"
        );
    }
}
