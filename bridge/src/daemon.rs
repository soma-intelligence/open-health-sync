// SPDX-License-Identifier: AGPL-3.0-only

//! The commands: wiring Bluetooth, the spool, the queue, folders and uploads.

use std::collections::{BTreeMap, HashMap};
use std::path::{Path, PathBuf};
use std::sync::{mpsc as std_mpsc, Arc, Mutex};
use std::thread;
use std::time::{Duration, Instant};

use btleplug::api::Central as _;
use chrono::{Local, Utc};
use tokio::sync::mpsc;

use crate::ble::{self, Event, Options, Target};
use crate::cli;
use crate::codec;
use crate::config::{self, Server};
use crate::files::{Scanner, Seen};
use crate::gatt;
use crate::live;
use crate::queue::{write_atomic, Queue};
use crate::spool::Spool;
use crate::status::{self, DeviceStatus, Status};
use crate::upload::{locked, Backoff, Drain, Report, Uploader};

/// How often watched folders are walked.
const FILES_EVERY: Duration = Duration::from_secs(10);

/// How long the uploader sleeps with nothing to do.
const IDLE: Duration = Duration::from_secs(60);

/// How long uploads pause after the server refused the token or the route.
const STOPPED: Duration = Duration::from_secs(300);

fn log(msg: &str) {
    eprintln!("{} {msg}", Local::now().format("%H:%M:%S"));
}

fn runtime() -> Result<tokio::runtime::Runtime, String> {
    tokio::runtime::Builder::new_multi_thread()
        .enable_all()
        .build()
        .map_err(|e| format!("cannot start: {e}"))
}

/// Resolves on Ctrl-C, or SIGTERM where there is one (systemd, launchd).
async fn shutdown() {
    #[cfg(unix)]
    {
        use tokio::signal::unix::{signal, SignalKind};
        match signal(SignalKind::terminate()) {
            Ok(mut term) => {
                tokio::select! {
                    _ = tokio::signal::ctrl_c() => {}
                    _ = term.recv() => {}
                }
            }
            Err(_) => {
                let _ = tokio::signal::ctrl_c().await;
            }
        }
    }
    #[cfg(not(unix))]
    let _ = tokio::signal::ctrl_c().await;
}

/// The state directory, created and locked for this process.
fn open_state() -> Result<(PathBuf, std::fs::File), String> {
    let state = config::state_dir()?;
    config::ensure_state(&state).map_err(|e| format!("cannot create {}: {e}", state.display()))?;
    let lock = config::lock(&state)?;
    Ok((state, lock))
}

fn io_err(what: &str) -> impl Fn(std::io::Error) -> String + '_ {
    move |e| format!("{what}: {e}")
}

pub fn scan(seconds: u64, all: bool) -> Result<(), String> {
    eprintln!("scanning for {seconds} seconds...");
    print!("{}", runtime()?.block_on(ble::scan(seconds, all))?);
    Ok(())
}

/// `ohs-bridge watch`: prints what devices send. Uploads nothing.
pub fn watch(devices: Vec<String>) -> Result<(), String> {
    let rt = runtime()?;
    rt.block_on(async {
        let adapter = ble::adapter().await?;
        adapter
            .start_scan(btleplug::api::ScanFilter::default())
            .await
            .map_err(|e| format!("cannot scan: {e}"))?;
        let target = Target::new(&devices, devices.is_empty());
        let opts = Arc::new(Options {
            device_user: None,
            glucose: Arc::default(),
        });
        let (tx, mut rx) = mpsc::channel(256);
        tokio::spawn(ble::connect_loop(adapter, target, opts, tx));
        eprintln!("looking for devices, Ctrl-C to stop");
        let mut names: HashMap<String, String> = HashMap::new();
        let stop = shutdown();
        tokio::pin!(stop);
        loop {
            let ev = tokio::select! {
                _ = &mut stop => return Ok(()),
                ev = rx.recv() => ev,
            };
            let Some(ev) = ev else { return Ok(()) };
            let name = |k: &str| names.get(k).cloned().unwrap_or_default();
            match ev {
                Event::Connected { key, name } => {
                    log(&format!("{name} connected"));
                    names.insert(key, name);
                }
                Event::Value {
                    key, char, value, ..
                } => {
                    let text = match char {
                        gatt::HEART_RATE_MEASUREMENT => match live::heart_rate(&value) {
                            Some(hr) => {
                                let mut t = format!("{} bpm", hr.bpm);
                                if !hr.rr_ms.is_empty() {
                                    let rr: Vec<String> =
                                        hr.rr_ms.iter().map(|r| format!("{r:.0}")).collect();
                                    t.push_str(&format!("  RR {} ms", rr.join(" ")));
                                }
                                if hr.contact == Some(false) {
                                    t.push_str("  (no skin contact)");
                                }
                                t
                            }
                            None => format!("heart rate {}", codec::hex(&value)),
                        },
                        gatt::BATTERY_LEVEL => match live::battery(&value) {
                            Some(b) => format!("battery {b}%"),
                            None => format!("battery {}", codec::hex(&value)),
                        },
                        c => format!("{} {}", gatt::char_name(c), codec::hex(&value)),
                    };
                    log(&format!("{}  {text}", name(&key)));
                }
                Event::Note { key, text } => log(&format!("{}: {text}", name(&key))),
                Event::Disconnected { key } => log(&format!("{} disconnected", name(&key))),
            }
        }
    })
}

/// The glucose sequence numbers, per device key: `glucose.json`.
fn load_glucose(state: &Path) -> HashMap<String, u16> {
    std::fs::read(state.join("glucose.json"))
        .ok()
        .and_then(|b| serde_json::from_slice(&b).ok())
        .unwrap_or_default()
}

/// `ohs-bridge run`: the daemon.
pub fn run(opts: cli::Run) -> Result<(), String> {
    let (state, _lock) = open_state()?;
    let server = config::server(opts.allow_http)?;
    let install = config::install_id(&state).map_err(io_err("cannot write the install id"))?;
    let mut queue = Queue::open(&state).map_err(io_err("cannot open the queue"))?;
    let mut spool =
        Spool::open(&state, &install, &mut queue).map_err(io_err("cannot open the spool"))?;
    let queue = Arc::new(Mutex::new(queue));
    let seen = Seen::load(&state).map_err(io_err("cannot read files.seen"))?;
    let report = Arc::new(Mutex::new(Report::default()));
    let (wake, wake_rx) = std_mpsc::channel::<()>();
    {
        let (queue, report, files) = (queue.clone(), report.clone(), opts.files.clone());
        thread::spawn(move || uploader(server, queue, seen, files, wake_rx, report));
    }

    let rt = runtime()?;
    rt.block_on(async {
        let (tx, mut rx) = mpsc::channel::<Event>(1024);
        let glucose = Arc::new(Mutex::new(load_glucose(&state)));
        if !opts.devices.is_empty() || opts.any {
            let adapter = ble::adapter().await?;
            adapter
                .start_scan(btleplug::api::ScanFilter::default())
                .await
                .map_err(|e| format!("cannot scan: {e}"))?;
            let target = Target::new(&opts.devices, opts.any);
            let o = Arc::new(Options { device_user: opts.device_user, glucose: glucose.clone() });
            tokio::spawn(ble::connect_loop(adapter, target, o, tx.clone()));
            log("looking for devices");
        }
        if !opts.files.is_empty() {
            log(&format!("watching {} folders for activity files", opts.files.len()));
        }

        let mut devices: BTreeMap<String, DeviceStatus> = BTreeMap::new();
        let mut tick = tokio::time::interval(Duration::from_secs(1));
        let mut last_status = Instant::now() - Duration::from_secs(60);
        // Unchanged, the status is rewritten once a minute: enough for `updated`
        // to show the bridge is alive, without a write every two seconds.
        let mut saved: Option<(Status, Instant)> = None;
        let mut write_status = |devices: &BTreeMap<String, DeviceStatus>, running: bool| {
            let (queued_parts, queued_bytes) = locked(&queue).size().unwrap_or_default();
            let rejected_parts = locked(&queue).rejected();
            let r = report.lock().unwrap_or_else(|e| e.into_inner());
            let s = Status {
                updated: Some(Utc::now()),
                running,
                queued_parts,
                queued_bytes,
                rejected_parts,
                sent_parts: r.sent,
                last_upload: r.last_upload,
                problem: r.problem.clone(),
                devices: devices.values().cloned().collect(),
            };
            drop(r);
            if let Some((prev, at)) = &saved {
                if prev.same_as(&s) && at.elapsed() < Duration::from_secs(60) {
                    return;
                }
            }
            if let Err(e) = s.save(&state, opts.status_file.as_deref()) {
                log(&format!("cannot write the status: {e}"));
            }
            saved = Some((s, Instant::now()));
        };
        let stop = shutdown();
        tokio::pin!(stop);
        loop {
            tokio::select! {
                _ = &mut stop => break,
                ev = rx.recv() => match ev {
                    Some(Event::Connected { key, name }) => {
                        log(&format!("{name} connected"));
                        let d = devices.entry(key).or_default();
                        d.name = name;
                        d.connected = true;
                    }
                    Some(Event::Value { key, char, value, line }) => {
                        let d = devices.entry(key.clone()).or_default();
                        if let Some(l) = line {
                            spool.append(&l, &mut locked(&queue)).map_err(io_err("cannot write the spool"))?;
                            d.values += 1;
                            d.last_value = Some(Utc::now());
                            if let Some(seq) = (char == gatt::GLUCOSE_MEASUREMENT).then(|| gatt::glucose_sequence(&value)).flatten() {
                                let mut g = glucose.lock().unwrap_or_else(|e| e.into_inner());
                                g.insert(key, seq);
                                let b = serde_json::to_vec(&*g).unwrap_or_default();
                                drop(g);
                                write_atomic(&state.join("glucose.json"), &b).map_err(io_err("cannot write glucose.json"))?;
                            }
                        }
                        match char {
                            gatt::HEART_RATE_MEASUREMENT => d.heart_rate = live::heart_rate(&value).map(|h| h.bpm),
                            gatt::BATTERY_LEVEL => d.battery = live::battery(&value),
                            _ => {}
                        }
                    }
                    Some(Event::Note { key, text }) => {
                        let name = devices.get(&key).map(|d| d.name.clone()).unwrap_or_default();
                        log(&format!("{name}: {text}"));
                    }
                    Some(Event::Disconnected { key }) => {
                        if let Some(d) = devices.get_mut(&key) {
                            d.connected = false;
                            d.heart_rate = None;
                            log(&format!("{} disconnected", d.name));
                        }
                    }
                    None => {}
                },
                _ = tick.tick() => {
                    if spool.rotate_if_due(&mut locked(&queue)).map_err(io_err("cannot seal the spool"))? {
                        let _ = wake.send(());
                    }
                    if last_status.elapsed() >= Duration::from_secs(2) {
                        write_status(&devices, true);
                        last_status = Instant::now();
                    }
                }
            }
        }
        // What arrived is queued before exiting; it is sent on the next run.
        spool.rotate(&mut locked(&queue)).map_err(io_err("cannot seal the spool"))?;
        for d in devices.values_mut() {
            d.connected = false;
        }
        write_status(&devices, false);
        log("stopped");
        Ok(())
    })
}

/// The upload thread of `run`: walks watched folders, drains the queue when
/// woken (a new part) or when its wait is over, and honours the backoff.
fn uploader(
    server: Server,
    queue: Arc<Mutex<Queue>>,
    mut seen: Seen,
    files: Vec<PathBuf>,
    wake: std_mpsc::Receiver<()>,
    report: Arc<Mutex<Report>>,
) {
    let up = Uploader::new(&server.base, &server.token, server.loopback);
    let mut scanner = Scanner::default();
    let mut backoff = Backoff::new();
    let mut not_before = Instant::now();
    let mut next_scan = Instant::now();
    let mut said: Option<String> = None;
    loop {
        let now = Instant::now();
        if !files.is_empty() && now >= next_scan {
            match scanner.scan(&files, &mut locked(&queue), &seen) {
                Ok(s) if s.queued > 0 => log(&format!("{} new activity files queued", s.queued)),
                Ok(_) => {}
                Err(e) => log(&format!("cannot queue activity files: {e}")),
            }
            next_scan = now + FILES_EVERY;
        }
        let mut wait = if files.is_empty() {
            IDLE
        } else {
            next_scan.saturating_duration_since(now)
        };
        if now >= not_before {
            let mut r = std::mem::take(&mut *report.lock().unwrap_or_else(|e| e.into_inner()));
            match up.drain(&queue, &mut seen, &mut backoff, &mut r) {
                Ok(Drain::Done) => {}
                Ok(Drain::Wait(d)) => not_before = Instant::now() + d,
                Ok(Drain::Stop) => not_before = Instant::now() + STOPPED,
                Err(e) => {
                    r.problem = Some(format!("cannot read the queue: {e}"));
                    not_before = Instant::now() + IDLE;
                }
            }
            if r.problem != said {
                match &r.problem {
                    Some(p) => log(&format!("uploads paused: {p}")),
                    None => log("uploads resumed"),
                }
                said = r.problem.clone();
            }
            *report.lock().unwrap_or_else(|e| e.into_inner()) = r;
        }
        wait = wait.min(
            not_before
                .saturating_duration_since(Instant::now())
                .max(Duration::from_millis(100)),
        );
        if let Err(std_mpsc::RecvTimeoutError::Disconnected) = wake.recv_timeout(wait) {
            return;
        }
    }
}

/// Drains the queue in the foreground, waiting out the server's backoff a few
/// times. Prints a summary.
fn drain_now(server: &Server, queue: &Mutex<Queue>, seen: &mut Seen) -> Result<(), String> {
    let up = Uploader::new(&server.base, &server.token, server.loopback);
    let (mut backoff, mut r) = (Backoff::new(), Report::default());
    let mut tries = 0;
    let end = loop {
        match up
            .drain(queue, seen, &mut backoff, &mut r)
            .map_err(io_err("cannot read the queue"))?
        {
            Drain::Wait(d) if tries < 5 && d <= Duration::from_secs(60) => {
                tries += 1;
                eprintln!(
                    "{}: retrying in {} s",
                    r.problem.as_deref().unwrap_or("waiting"),
                    d.as_secs()
                );
                thread::sleep(d);
            }
            d => break d,
        }
    };
    let left = locked(queue).list().map(|l| l.len()).unwrap_or(0);
    println!(
        "parts: {} sent ({} already stored), {} refused, {} still queued",
        r.sent, r.duplicates, r.rejected, left
    );
    if r.rejected > 0 {
        println!("refused parts and the server's answers are in the rejected folder of the state directory");
    }
    match end {
        Drain::Done => Ok(()),
        _ => Err(r.problem.unwrap_or_else(|| "uploads did not finish".into())),
    }
}

/// `ohs-bridge files`: queues new activity files from folders and sends them.
pub fn files(dirs: Vec<PathBuf>, watch: bool, allow_http: bool) -> Result<(), String> {
    let (state, _lock) = open_state()?;
    let server = config::server(allow_http)?;
    let queue = Mutex::new(Queue::open(&state).map_err(io_err("cannot open the queue"))?);
    let mut seen = Seen::load(&state).map_err(io_err("cannot read files.seen"))?;
    let mut scanner = Scanner::default();
    loop {
        let s = scanner
            .scan(&dirs, &mut locked(&queue), &seen)
            .map_err(io_err("cannot queue activity files"))?;
        if s.queued > 0 || !watch {
            println!(
                "activity files: {} new, {} already sent, {} skipped (empty or over 20 MiB), {} unreadable",
                s.queued, s.known, s.skipped, s.unreadable
            );
        }
        let res = if locked(&queue).list().map(|l| l.is_empty()).unwrap_or(true) {
            Ok(())
        } else {
            drain_now(&server, &queue, &mut seen)
        };
        if !watch {
            return res;
        }
        if let Err(e) = res {
            log(&format!("uploads paused: {e}"));
        }
        thread::sleep(FILES_EVERY);
    }
}

/// `ohs-bridge flush`: sends the queued parts now.
pub fn flush(allow_http: bool) -> Result<(), String> {
    let (state, _lock) = open_state()?;
    let server = config::server(allow_http)?;
    let mut q = Queue::open(&state).map_err(io_err("cannot open the queue"))?;
    // A spool left by a stopped daemon is queued too.
    let install = config::install_id(&state).map_err(io_err("cannot read the install id"))?;
    Spool::open(&state, &install, &mut q).map_err(io_err("cannot open the spool"))?;
    let queue = Mutex::new(q);
    let mut seen = Seen::load(&state).map_err(io_err("cannot read files.seen"))?;
    drain_now(&server, &queue, &mut seen)
}

/// `ohs-bridge status`.
pub fn status() -> Result<(), String> {
    let state = config::state_dir()?;
    if !state.exists() {
        println!(
            "no state yet in {}: start `ohs-bridge run`",
            state.display()
        );
        return Ok(());
    }
    // Read only: the daemon may be running.
    let parts = state.join("parts");
    let mut queued = (0usize, 0u64);
    for e in std::fs::read_dir(&parts).into_iter().flatten().flatten() {
        let p = e.path();
        match p.extension().and_then(|x| x.to_str()) {
            Some("json") => queued.0 += 1,
            Some("gz") => queued.1 += e.metadata().map(|m| m.len()).unwrap_or(0),
            _ => {}
        }
    }
    let rejected = std::fs::read_dir(state.join("rejected"))
        .into_iter()
        .flatten()
        .flatten()
        .filter(|e| e.path().extension().is_some_and(|x| x == "json"))
        .count();
    print!("{}", status::print(&state, queued, rejected));
    Ok(())
}
