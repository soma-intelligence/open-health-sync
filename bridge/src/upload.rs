// SPDX-License-Identifier: AGPL-3.0-only

//! Sends queued parts to the server, oldest first.
//!
//! 200 (stored, or a duplicate of a stored part) removes the part. 401 and
//! 403 stop uploads: the token was refused. 404 and 405 stop them too: the
//! server has no such route yet, and the parts wait for an upgrade. 408, 429
//! and 5xx, and network errors, wait (Retry-After, else 1 s doubling to 5
//! minutes) and retry the same bytes. Another 4xx moves the part to
//! `rejected/` with the server's answer.

use std::io;
use std::sync::{Mutex, MutexGuard};
use std::time::Duration;

use chrono::{DateTime, Utc};
use serde::Deserialize;
use ureq::tls::{RootCerts, TlsConfig};
use ureq::Agent;

use crate::files::Seen;
use crate::queue::{Header, Queue};

/// How long one request may take, upload included: a part is at most a few
/// MiB on a slow uplink.
const REQUEST_TIMEOUT: Duration = Duration::from_secs(300);

/// What the server's answer means for a part.
#[derive(Debug, PartialEq)]
pub enum Answer {
    /// Stored, now or before.
    Stored { duplicate: bool },
    /// Refused for good: the part moves to `rejected/`.
    Refused { status: u16, detail: String },
    /// Nothing more can be sent until the user acts.
    Stop(String),
    /// Try again later.
    Later {
        retry_after: Option<Duration>,
        why: String,
    },
}

/// Reads an answer.
pub fn answer(status: u16, retry_after: Option<&str>, body: &str) -> Answer {
    match status {
        200 => {
            #[derive(Deserialize)]
            struct Stored {
                #[serde(default)]
                duplicate: bool,
            }
            let duplicate = serde_json::from_str::<Stored>(body).is_ok_and(|s| s.duplicate);
            Answer::Stored { duplicate }
        }
        401 | 403 => Answer::Stop(format!(
            "the server refused the token ({status}): OHS_TOKEN must be a user token (ohs_ut_) with the upload scope"
        )),
        404 | 405 => Answer::Stop(format!(
            "the server has no upload route for these parts ({status}): check OHS_URL, or update the server"
        )),
        300..=399 => Answer::Stop(format!("the server answered a redirect ({status}): set OHS_URL to the final address")),
        408 | 429 | 500..=599 => Answer::Later {
            retry_after: retry_after.and_then(parse_retry_after),
            why: format!("the server is busy ({status})"),
        },
        400..=499 => Answer::Refused { status, detail: detail(body) },
        _ => Answer::Later { retry_after: None, why: format!("unexpected answer ({status})") },
    }
}

/// Retry-After in seconds (the HTTP date form is not used by the server),
/// capped at an hour.
fn parse_retry_after(v: &str) -> Option<Duration> {
    v.trim()
        .parse::<u64>()
        .ok()
        .map(|s| Duration::from_secs(s.min(3600)))
}

/// The detail of a problem answer, else the start of the body.
fn detail(body: &str) -> String {
    #[derive(Deserialize)]
    struct Problem {
        #[serde(default)]
        title: String,
        #[serde(default)]
        detail: String,
    }
    match serde_json::from_str::<Problem>(body) {
        Ok(p) if !p.detail.is_empty() => p.detail,
        Ok(p) if !p.title.is_empty() => p.title,
        _ => body.chars().take(500).collect(),
    }
}

/// Waits between retries: Retry-After when the server sends one, else 1 s
/// doubling to 5 minutes.
pub struct Backoff {
    next: Duration,
}

impl Backoff {
    const FIRST: Duration = Duration::from_secs(1);
    const MAX: Duration = Duration::from_secs(300);

    pub fn new() -> Backoff {
        Backoff { next: Self::FIRST }
    }

    pub fn delay(&mut self, retry_after: Option<Duration>) -> Duration {
        let d = retry_after.unwrap_or(self.next);
        self.next = (self.next * 2).min(Self::MAX);
        d
    }

    pub fn reset(&mut self) {
        self.next = Self::FIRST;
    }
}

/// What uploads did, for the status line.
#[derive(Debug, Default)]
pub struct Report {
    pub sent: u64,
    pub duplicates: u64,
    pub rejected: u64,
    pub last_upload: Option<DateTime<Utc>>,
    /// Why uploads are not going through, while they are not.
    pub problem: Option<String>,
}

/// The end of a drain.
#[derive(Debug, PartialEq)]
pub enum Drain {
    /// The queue is empty.
    Done,
    /// Try again after this long.
    Wait(Duration),
    /// The user must act (`Report::problem` says what).
    Stop,
}

pub struct Uploader {
    agent: Agent,
    base: String,
    token: String,
}

impl Uploader {
    /// An uploader to a server base URL (checked by `config::server`). Proxy
    /// settings come from the environment, except for a loopback server.
    pub fn new(base: &str, token: &str, loopback: bool) -> Uploader {
        let mut cfg = Agent::config_builder()
            .timeout_global(Some(REQUEST_TIMEOUT))
            .http_status_as_error(false)
            .max_redirects(0)
            .user_agent(concat!("ohs-bridge/", env!("CARGO_PKG_VERSION")))
            .tls_config(
                TlsConfig::builder()
                    .root_certs(RootCerts::PlatformVerifier)
                    .build(),
            );
        if loopback {
            cfg = cfg.proxy(None);
        }
        Uploader {
            agent: cfg.build().into(),
            base: base.trim_end_matches('/').to_string(),
            token: token.to_string(),
        }
    }

    /// Sends one part.
    pub fn send(&self, h: &Header, gz: &[u8]) -> Answer {
        let res = self
            .agent
            .post(format!("{}/v1/ingest/{}", self.base, h.route))
            .header("Authorization", format!("Bearer {}", self.token))
            .header("Content-Type", "application/gzip")
            .header("X-OHS-Type", &h.data_type)
            .header("X-OHS-Day", &h.day)
            .header("X-OHS-Batch", &h.batch)
            .header("X-OHS-Part", h.part.to_string())
            .send(gz);
        match res {
            Ok(mut r) => {
                let status = r.status().as_u16();
                let retry = r
                    .headers()
                    .get("retry-after")
                    .and_then(|v| v.to_str().ok())
                    .map(str::to_owned);
                let body = r
                    .body_mut()
                    .with_config()
                    .limit(64 * 1024)
                    .read_to_string()
                    .unwrap_or_default();
                answer(status, retry.as_deref(), &body)
            }
            Err(e) => Answer::Later {
                retry_after: None,
                why: format!("the server is unreachable ({e})"),
            },
        }
    }

    /// Sends the queued parts, oldest first, until the queue is empty or an
    /// answer says to wait or stop. The queue is locked for disk work only,
    /// never across a request.
    pub fn drain(
        &self,
        queue: &Mutex<Queue>,
        seen: &mut Seen,
        backoff: &mut Backoff,
        report: &mut Report,
    ) -> io::Result<Drain> {
        let ids = locked(queue).list()?;
        for id in ids {
            let loaded = locked(queue).load(id);
            let (h, gz) = match loaded {
                Ok(p) => p,
                Err(e) => {
                    locked(queue).reject(id, &format!("unreadable part: {e}\n"))?;
                    report.rejected += 1;
                    continue;
                }
            };
            match self.send(&h, &gz) {
                Answer::Stored { duplicate } => {
                    locked(queue).remove(id)?;
                    if let Some(f) = &h.file_sha256 {
                        seen.record(f)?;
                    }
                    report.sent += 1;
                    report.duplicates += duplicate as u64;
                    report.last_upload = Some(Utc::now());
                    report.problem = None;
                    backoff.reset();
                }
                Answer::Refused { status, detail } => {
                    locked(queue).reject(id, &format!("{status} {detail}\n"))?;
                    // A file the server refuses (not an activity) is not retried.
                    if let Some(f) = &h.file_sha256 {
                        seen.record(f)?;
                    }
                    report.rejected += 1;
                    backoff.reset();
                }
                Answer::Stop(why) => {
                    report.problem = Some(why);
                    return Ok(Drain::Stop);
                }
                Answer::Later { retry_after, why } => {
                    report.problem = Some(why);
                    return Ok(Drain::Wait(backoff.delay(retry_after)));
                }
            }
        }
        Ok(Drain::Done)
    }
}

/// The queue, even if another thread panicked while holding it: its state is
/// on disk, not in the lock.
pub fn locked(queue: &Mutex<Queue>) -> MutexGuard<'_, Queue> {
    queue.lock().unwrap_or_else(|e| e.into_inner())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::queue::tests::{header, TempDir};
    use std::io::{BufRead, BufReader, Read, Write};
    use std::net::TcpListener;
    use std::sync::mpsc;
    use std::thread;

    #[test]
    fn answers() {
        assert_eq!(
            answer(200, None, r#"{"key":"k","duplicate":true}"#),
            Answer::Stored { duplicate: true }
        );
        assert_eq!(
            answer(200, None, r#"{"key":"k","duplicate":false}"#),
            Answer::Stored { duplicate: false }
        );
        assert_eq!(answer(200, None, ""), Answer::Stored { duplicate: false });
        assert!(matches!(answer(401, None, ""), Answer::Stop(_)));
        assert!(matches!(answer(403, None, ""), Answer::Stop(_)));
        assert!(matches!(answer(404, None, ""), Answer::Stop(_)));
        assert!(matches!(answer(302, None, ""), Answer::Stop(_)));
        assert_eq!(
            answer(429, Some("7"), ""),
            Answer::Later {
                retry_after: Some(Duration::from_secs(7)),
                why: "the server is busy (429)".into()
            }
        );
        assert!(matches!(
            answer(503, Some("Wed, 21 Oct 2026 07:28:00 GMT"), ""),
            Answer::Later {
                retry_after: None,
                ..
            }
        ));
        assert!(matches!(answer(408, None, ""), Answer::Later { .. }));
        assert_eq!(
            answer(
                400,
                None,
                r#"{"title":"Bad Request","status":400,"detail":"line 1: value must be hex"}"#
            ),
            Answer::Refused {
                status: 400,
                detail: "line 1: value must be hex".into()
            }
        );
        assert_eq!(
            answer(409, None, "conflict"),
            Answer::Refused {
                status: 409,
                detail: "conflict".into()
            }
        );
        assert_eq!(
            answer(413, None, r#"{"title":"Content Too Large"}"#),
            Answer::Refused {
                status: 413,
                detail: "Content Too Large".into()
            }
        );
    }

    #[test]
    fn backoff_doubles_to_five_minutes() {
        let mut b = Backoff::new();
        let delays: Vec<u64> = (0..12).map(|_| b.delay(None).as_secs()).collect();
        assert_eq!(
            delays,
            vec![1, 2, 4, 8, 16, 32, 64, 128, 256, 300, 300, 300]
        );
        assert_eq!(
            b.delay(Some(Duration::from_secs(7))),
            Duration::from_secs(7)
        );
        b.reset();
        assert_eq!(b.delay(None), Duration::from_secs(1));
    }

    struct Request {
        head: String,
        body: Vec<u8>,
    }

    /// A one-thread HTTP server answering each request with the next scripted
    /// response (status line and headers, body), then closing the connection.
    fn fake_server(
        responses: Vec<(&'static str, &'static str)>,
    ) -> (String, mpsc::Receiver<Request>) {
        let l = TcpListener::bind("127.0.0.1:0").unwrap();
        let addr = format!("http://{}", l.local_addr().unwrap());
        let (tx, rx) = mpsc::channel();
        thread::spawn(move || {
            for (head_out, body_out) in responses {
                let (mut s, _) = l.accept().unwrap();
                let mut r = BufReader::new(s.try_clone().unwrap());
                let mut head = String::new();
                loop {
                    let mut line = String::new();
                    r.read_line(&mut line).unwrap();
                    if line == "\r\n" || line.is_empty() {
                        break;
                    }
                    head.push_str(&line);
                }
                let len = head
                    .lines()
                    .find_map(|l| {
                        l.to_ascii_lowercase()
                            .strip_prefix("content-length:")
                            .map(|v| v.trim().parse::<usize>().unwrap())
                    })
                    .unwrap_or(0);
                let mut body = vec![0; len];
                r.read_exact(&mut body).unwrap();
                let resp = format!(
                    "HTTP/1.1 {head_out}\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body_out}",
                    body_out.len()
                );
                s.write_all(resp.as_bytes()).unwrap();
                s.flush().unwrap();
                tx.send(Request { head, body }).unwrap();
            }
        });
        (addr, rx)
    }

    const OK: (&str, &str) = (
        "200 OK\r\nContent-Type: application/json",
        r#"{"key":"k","duplicate":false}"#,
    );
    const DUP: (&str, &str) = (
        "200 OK\r\nContent-Type: application/json",
        r#"{"key":"k","duplicate":true}"#,
    );
    const BAD: (&str, &str) = (
        "400 Bad Request\r\nContent-Type: application/problem+json",
        r#"{"title":"Bad Request","detail":"not an activity"}"#,
    );
    const BUSY: (&str, &str) = ("429 Too Many Requests\r\nRetry-After: 7", "");
    const DOWN: (&str, &str) = ("503 Service Unavailable", "");
    const DENIED: (&str, &str) = ("401 Unauthorized", "");

    #[test]
    fn drain_against_a_server() {
        let dir = TempDir::new("upload");
        let mut q = Queue::open(&dir.0).unwrap();
        let mut seen = Seen::load(&dir.0).unwrap();
        let ids: Vec<u64> = (0..4).map(|_| q.next_id()).collect();
        q.add(ids[0], &header("bt1-a"), b"first").unwrap();
        let mut file = header("af1-0123");
        file.route = "activity-files".into();
        file.data_type = "fit".into();
        file.file_sha256 = Some("cd".repeat(32));
        q.add(ids[1], &file, b"second").unwrap();
        q.add(ids[2], &header("bt1-c"), b"third").unwrap();
        q.add(ids[3], &header("bt1-d"), b"fourth").unwrap();

        let q = Mutex::new(q);
        let (base, rx) = fake_server(vec![OK, BAD, DUP, BUSY, DOWN, OK, DENIED]);
        let up = Uploader::new(&base, "ohs_ut_test", true);
        let (mut b, mut report) = (Backoff::new(), Report::default());

        // 200, 400 (the file is refused and remembered), 200 duplicate, then 429.
        assert_eq!(
            up.drain(&q, &mut seen, &mut b, &mut report).unwrap(),
            Drain::Wait(Duration::from_secs(7))
        );
        assert_eq!(locked(&q).list().unwrap(), vec![ids[3]]);
        assert_eq!((report.sent, report.duplicates, report.rejected), (2, 1, 1));
        assert!(seen.contains(&"cd".repeat(32)));
        assert!(report.problem.as_deref().unwrap().contains("429"));
        let first = rx.recv().unwrap();
        assert!(
            first
                .head
                .starts_with("POST /v1/ingest/bluetooth HTTP/1.1\r\n"),
            "{}",
            first.head
        );
        let head = first.head.to_ascii_lowercase();
        for h in [
            "authorization: bearer ohs_ut_test",
            "content-type: application/gzip",
            "x-ohs-type: gatt",
            "x-ohs-day: 2026-10-16",
            "x-ohs-batch: bt1-a",
            "x-ohs-part: 0",
        ] {
            assert!(head.contains(h), "{h} in {head}");
        }
        assert_eq!(first.body, b"first");
        let second = rx.recv().unwrap();
        assert!(second.head.starts_with("POST /v1/ingest/activity-files "));
        assert!(second.head.to_ascii_lowercase().contains("x-ohs-type: fit"));
        rx.recv().unwrap();
        let retried = rx.recv().unwrap();
        assert_eq!(retried.body, b"fourth");

        // 503 right after the 429: the second wait in a row, 2 s. Then 200
        // empties the queue.
        assert_eq!(
            up.drain(&q, &mut seen, &mut b, &mut report).unwrap(),
            Drain::Wait(Duration::from_secs(2))
        );
        assert_eq!(rx.recv().unwrap().body, b"fourth", "the same bytes again");
        assert_eq!(
            up.drain(&q, &mut seen, &mut b, &mut report).unwrap(),
            Drain::Done
        );
        assert!(locked(&q).list().unwrap().is_empty());
        assert!(report.problem.is_none());
        assert!(report.last_upload.is_some());

        // 401 stops and keeps the part.
        let id = locked(&q).next_id();
        locked(&q).add(id, &header("bt1-e"), b"fifth").unwrap();
        assert_eq!(
            up.drain(&q, &mut seen, &mut b, &mut report).unwrap(),
            Drain::Stop
        );
        assert_eq!(locked(&q).list().unwrap(), vec![id]);
        assert!(report.problem.as_deref().unwrap().contains("token"));

        // Nobody listening: wait.
        let gone = Uploader::new("http://127.0.0.1:9", "ohs_ut_test", true);
        assert!(matches!(
            gone.drain(&q, &mut seen, &mut b, &mut report).unwrap(),
            Drain::Wait(_)
        ));
        assert!(report.problem.as_deref().unwrap().contains("unreachable"));
    }
}
