// SPDX-License-Identifier: AGPL-3.0-only

//! Bluetooth: finds devices, connects, subscribes, and forwards what they
//! send as events. It needs a radio, so only device matching is unit tested;
//! the rest stays thin over the pure modules.

use std::collections::{BTreeSet, HashMap, HashSet, VecDeque};
use std::sync::{Arc, Mutex};
use std::time::{Duration, SystemTime};

use btleplug::api::{
    Central, CharPropFlags, Characteristic, Manager as _, Peripheral as _, ScanFilter, WriteType,
};
use btleplug::platform::{Adapter, Manager, Peripheral, PeripheralId};
use chrono::Local;
use futures_util::StreamExt;
use sha2::{Digest, Sha256};
use tokio::sync::mpsc;
use tokio::time::Instant;
use uuid::Uuid;

use crate::codec;
use crate::gatt::{self, short};
use crate::line::{self, Device};
use crate::vendor::{self, Action, Context, Driver, Live, Peer, Step};

const CONNECT_TIMEOUT: Duration = Duration::from_secs(20);
const RETRY_FIRST: Duration = Duration::from_secs(5);
const RETRY_MAX: Duration = Duration::from_secs(60);
/// How long a vendor driver waits for an answer before it moves on.
const ANSWER_TIMEOUT: Duration = Duration::from_secs(5);

/// What a connection reports.
#[derive(Debug)]
pub enum Event {
    Connected {
        key: String,
        name: String,
    },
    /// A value as received; `line` is its upload line, for the
    /// characteristics the server decodes.
    Value {
        key: String,
        char: u16,
        value: Vec<u8>,
        line: Option<String>,
    },
    /// What a vendor driver made of a value: its upload lines (the value
    /// itself, or the samples it decoded), and what the live view shows.
    Vendor {
        key: String,
        lines: Vec<String>,
        live: Option<Live>,
    },
    /// Something the user may need to act on (pairing, mostly).
    Note {
        key: String,
        text: String,
    },
    Disconnected {
        key: String,
    },
}

/// What connections need from the caller.
pub struct Options {
    pub device_user: Option<u8>,
    /// The last glucose sequence number stored, per device key.
    pub glucose: Arc<Mutex<HashMap<String, u16>>>,
    /// The vendor protocols to run, and what to ask of them.
    pub vendor: vendor::Config,
    /// Values are queued for upload (`run`), not only shown (`watch`).
    pub upload: bool,
}

/// The first Bluetooth adapter.
pub async fn adapter() -> Result<Adapter, String> {
    let m = Manager::new()
        .await
        .map_err(|e| format!("Bluetooth is not available: {e}"))?;
    m.adapters()
        .await
        .map_err(|e| format!("Bluetooth is not available: {e}"))?
        .into_iter()
        .next()
        .ok_or_else(|| "no Bluetooth adapter found".to_string())
}

/// Which devices to connect to: a name fragment, or any device advertising a
/// supported service.
#[derive(Clone, Debug)]
pub struct Target {
    names: Vec<String>,
    any: bool,
}

impl Target {
    pub fn new(names: &[String], any: bool) -> Target {
        Target {
            names: names.iter().map(|n| n.to_lowercase()).collect(),
            any,
        }
    }

    pub fn matches(&self, name: Option<&str>, services: &[Uuid]) -> bool {
        if let Some(n) = name.map(str::to_lowercase) {
            if self.names.iter().any(|w| n.contains(w.as_str())) {
                return true;
            }
        }
        self.any
            && (supported(services).next().is_some()
                || !vendor::recognized(Peer { name, services }).is_empty())
    }
}

/// The supported health services among advertised ones.
pub fn supported(services: &[Uuid]) -> impl Iterator<Item = u16> + '_ {
    services
        .iter()
        .filter_map(short)
        .filter(|s| gatt::SERVICES.contains(s))
}

/// A stable key for a device that is not its address: kept in the state
/// directory (glucose sequence numbers) and never sent.
/// An error text without Bluetooth addresses (`C8:4F:...`, or BlueZ's
/// `dev_C8_4F_...` object paths): they identify the device, and the log keeps
/// none.
fn redact(text: &str) -> String {
    let b = text.as_bytes();
    let hex = |i: usize| b.get(i).is_some_and(u8::is_ascii_hexdigit);
    let is_address = |i: usize| {
        let sep = b.get(i + 2).copied();
        matches!(sep, Some(b':' | b'_' | b'-'))
            && (0..6).all(|k| hex(i + 3 * k) && hex(i + 3 * k + 1))
            && (0..5).all(|k| b.get(i + 3 * k + 2).copied() == sep)
            && !hex(i + 17)
            && (i == 0 || !b[i - 1].is_ascii_alphanumeric() || b[i - 1] == b'_')
    };
    let (mut out, mut i, mut from) = (String::new(), 0, 0);
    while i < b.len() {
        if is_address(i) {
            out.push_str(&text[from..i]);
            out.push_str("<address>");
            i += 17;
            from = i;
        } else {
            i += 1;
        }
    }
    out.push_str(&text[from..]);
    out
}

fn device_key(id: &PeripheralId) -> String {
    codec::hex(&Sha256::digest(id.to_string().as_bytes())[..8])
}

/// Scans continuously and keeps a connection open to every matching device,
/// reconnecting with a backoff when one drops or is out of range.
pub async fn connect_loop(
    adapter: Adapter,
    target: Target,
    opts: Arc<Options>,
    tx: mpsc::Sender<Event>,
) {
    let (done_tx, mut done_rx) = mpsc::unbounded_channel::<(PeripheralId, bool)>();
    let mut active: HashSet<PeripheralId> = HashSet::new();
    let mut retry: HashMap<PeripheralId, (Instant, Duration)> = HashMap::new();
    let mut tick = tokio::time::interval(Duration::from_secs(2));
    loop {
        tokio::select! {
            Some((id, connected)) = done_rx.recv() => {
                active.remove(&id);
                let wait = match (connected, retry.get(&id)) {
                    (false, Some((_, last))) => (*last * 2).min(RETRY_MAX),
                    _ => RETRY_FIRST,
                };
                retry.insert(id, (Instant::now() + wait, wait));
            }
            _ = tick.tick() => {
                let Ok(list) = adapter.peripherals().await else { continue };
                for p in list {
                    let id = p.id();
                    if active.contains(&id) || retry.get(&id).is_some_and(|(at, _)| Instant::now() < *at) {
                        continue;
                    }
                    let Ok(Some(props)) = p.properties().await else { continue };
                    let name = props.local_name.clone().or(props.advertisement_name.clone());
                    if !target.matches(name.as_deref(), &props.services) {
                        continue;
                    }
                    active.insert(id.clone());
                    let (opts, tx, done) = (opts.clone(), tx.clone(), done_tx.clone());
                    let name = name.unwrap_or_else(|| "unnamed device".into());
                    let services = props.services.clone();
                    tokio::spawn(async move {
                        let connected = connection(p, name, services, opts, tx).await;
                        let _ = done.send((id, connected));
                    });
                }
            }
        }
    }
}

/// One connection, from connect to disconnect. False when it never connected.
async fn connection(
    p: Peripheral,
    name: String,
    services: Vec<Uuid>,
    opts: Arc<Options>,
    tx: mpsc::Sender<Event>,
) -> bool {
    let key = device_key(&p.id());
    if !matches!(
        tokio::time::timeout(CONNECT_TIMEOUT, p.connect()).await,
        Ok(Ok(()))
    ) {
        return false;
    }
    if let Err(e) = session(&p, &key, &name, &services, &opts, &tx).await {
        let _ = tx
            .send(Event::Note {
                key: key.clone(),
                text: format!("connection lost ({})", redact(&e.to_string())),
            })
            .await;
    }
    let _ = p.disconnect().await;
    let _ = tx.send(Event::Disconnected { key }).await;
    true
}

async fn session(
    p: &Peripheral,
    key: &str,
    name: &str,
    services: &[Uuid],
    opts: &Options,
    tx: &mpsc::Sender<Event>,
) -> Result<(), btleplug::Error> {
    p.discover_services().await?;
    let chars = p.characteristics();
    let uuids: Vec<Uuid> = chars.iter().map(|c| c.uuid).collect();
    let peer = Peer {
        name: Some(name),
        services,
    };
    let ctx = Context {
        upload: opts.upload,
        utc_offset: Some(Local::now().offset().local_minus_utc()),
    };
    let mut driver = vendor::detect(peer, &uuids, &opts.vendor, ctx);
    let find = |c: u16| chars.iter().find(|ch| short(&ch.uuid) == Some(c));
    let can = |ch: &&Characteristic, flags: CharPropFlags| ch.properties.intersects(flags);

    // Device Information: once per connection.
    let mut dev = Device {
        name: line::public_name(name),
        ..Default::default()
    };
    for (c, field) in [
        (gatt::MANUFACTURER_NAME, &mut dev.manufacturer),
        (gatt::MODEL_NUMBER, &mut dev.model),
        (gatt::FIRMWARE_REVISION, &mut dev.firmware),
    ] {
        if let Some(ch) = find(c).filter(|ch| can(ch, CharPropFlags::READ)) {
            if let Ok(v) = p.read(ch).await {
                *field = line::text(&v);
            }
        }
    }

    // Before subscribing: a cuff sends its memory as soon as it may.
    let mut stream = p.notifications().await?;

    // Set the device clock, when it lets us, so stored readings carry the
    // right time. A device that refuses keeps its own.
    if let Some(ch) = find(gatt::CURRENT_TIME).filter(|ch| can(ch, CharPropFlags::WRITE)) {
        let _ = p
            .write(
                ch,
                &gatt::current_time(&Local::now()),
                WriteType::WithResponse,
            )
            .await;
    }
    let _ = tx
        .send(Event::Connected {
            key: key.into(),
            name: dev.name.clone(),
        })
        .await;

    if let Some(ch) = find(gatt::BATTERY_LEVEL) {
        if can(&ch, CharPropFlags::READ) {
            if let Ok(value) = p.read(ch).await {
                let _ = tx
                    .send(Event::Value {
                        key: key.into(),
                        char: gatt::BATTERY_LEVEL,
                        value,
                        line: None,
                    })
                    .await;
            }
        }
        if can(&ch, CharPropFlags::NOTIFY) {
            let _ = p.subscribe(ch).await;
        }
    }
    let notifiable = CharPropFlags::NOTIFY | CharPropFlags::INDICATE;
    for c in gatt::MEASUREMENTS {
        if let Some(ch) = find(c).filter(|ch| can(ch, notifiable)) {
            if let Err(e) = p.subscribe(ch).await {
                let text = format!(
                    "cannot subscribe to {} ({}): pair the device with the system first",
                    gatt::char_name(c),
                    redact(&e.to_string())
                );
                let _ = tx
                    .send(Event::Note {
                        key: key.into(),
                        text,
                    })
                    .await;
            }
        }
    }
    // Glucose meters answer the Record Access Control Point: ask for the
    // records after the last one stored, all of them the first time.
    if find(gatt::GLUCOSE_MEASUREMENT).is_some() {
        if let Some(racp) = find(gatt::RECORD_ACCESS_CONTROL_POINT) {
            let last = opts
                .glucose
                .lock()
                .unwrap_or_else(|e| e.into_inner())
                .get(key)
                .copied();
            if p.subscribe(racp).await.is_ok() {
                let _ = p
                    .write(racp, &gatt::racp_report(last), WriteType::WithResponse)
                    .await;
            }
        }
    }

    // A vendor protocol: its requests now, its answers in the loop below.
    let session = line::new_session();
    let out = Out {
        tx,
        key,
        session: &session,
        dev: &dev,
    };
    let mut waiting_since = Instant::now();
    if let Some(d) = driver.as_deref_mut() {
        let actions = d.start(SystemTime::now());
        if !act(p, &chars, d, actions, &out).await {
            return Ok(());
        }
    }

    let mut check = tokio::time::interval(Duration::from_secs(1));
    let mut ticks = 0u32;
    loop {
        tokio::select! {
            n = stream.next() => {
                let Some(n) = n else { break };
                let at = Local::now().fixed_offset();
                if let Some(d) = driver.as_deref_mut().filter(|d| d.owns(&n.uuid)) {
                    let mut step = d.input(&n.uuid, &n.value, false, SystemTime::now());
                    // A new request: its answer gets the full wait.
                    if !step.actions.is_empty() {
                        waiting_since = Instant::now();
                    }
                    // What the step decoded is queued before its actions run:
                    // an acknowledgement may let the device drop its copy.
                    let actions = std::mem::take(&mut step.actions);
                    if !out.step(&at, &n.uuid, &n.value, step).await || !act(p, &chars, d, actions, &out).await {
                        break;
                    }
                    continue;
                }
                let Some(char) = short(&n.uuid) else { continue };
                let line = gatt::MEASUREMENTS
                    .contains(&char)
                    .then(|| line::encode(&at, char, &n.value, &session, &dev, opts.device_user))
                    .flatten();
                if tx.send(Event::Value { key: key.into(), char, value: n.value, line }).await.is_err() {
                    break;
                }
            }
            _ = check.tick() => {
                if let Some(d) = driver.as_deref_mut().filter(|d| d.waiting()) {
                    if waiting_since.elapsed() >= ANSWER_TIMEOUT {
                        let mut step = d.timeout(SystemTime::now());
                        waiting_since = Instant::now();
                        let actions = std::mem::take(&mut step.actions);
                        let at = Local::now().fixed_offset();
                        if !out.step(&at, &Uuid::nil(), &[], step).await || !act(p, &chars, d, actions, &out).await {
                            break;
                        }
                    }
                }
                ticks += 1;
                if ticks.is_multiple_of(5) && !p.is_connected().await.unwrap_or(false) {
                    break;
                }
            }
        }
    }
    Ok(())
}

/// Where a driver's output goes: the events of one connection.
struct Out<'a> {
    tx: &'a mpsc::Sender<Event>,
    key: &'a str,
    session: &'a str,
    dev: &'a Device,
}

impl Out<'_> {
    /// Sends what a step produced (its note, its upload lines, its live
    /// view), its actions aside. False when the receiver is gone.
    async fn step(
        &self,
        at: &chrono::DateTime<chrono::FixedOffset>,
        uuid: &Uuid,
        value: &[u8],
        step: Step,
    ) -> bool {
        if let Some(text) = step.note {
            self.note(text).await;
        }
        let mut lines: Vec<String> = Vec::new();
        if let Some(ctx) = step.upload {
            lines.extend(line::encode_vendor(
                at,
                uuid,
                value,
                &ctx,
                self.session,
                self.dev,
            ));
        }
        for s in &step.samples {
            lines.extend(line::encode_sample(at, uuid, s, self.session, self.dev));
        }
        if lines.is_empty() && step.live.is_none() {
            return true;
        }
        self.tx
            .send(Event::Vendor {
                key: self.key.into(),
                lines,
                live: step.live,
            })
            .await
            .is_ok()
    }

    async fn note(&self, text: String) {
        let _ = self
            .tx
            .send(Event::Note {
                key: self.key.into(),
                text,
            })
            .await;
    }
}

/// Runs a driver's actions in order. A read's value goes back to the driver:
/// what it makes of it is sent, then its own actions follow. False when the
/// receiver is gone.
async fn act(
    p: &Peripheral,
    chars: &BTreeSet<Characteristic>,
    driver: &mut dyn Driver,
    actions: Vec<Action>,
    out: &Out<'_>,
) -> bool {
    let mut queue: VecDeque<Action> = actions.into();
    while let Some(a) = queue.pop_front() {
        let uuid = a.char();
        let Some(ch) = chars.iter().find(|c| c.uuid == uuid) else {
            out.note(format!("the device has no characteristic {uuid}"))
                .await;
            continue;
        };
        let res = match a {
            Action::Subscribe(_) => p.subscribe(ch).await.map_err(|e| {
                format!(
                    "cannot subscribe to {uuid} ({}): pair the device with the system first",
                    redact(&e.to_string())
                )
            }),
            Action::Read(_) => match p.read(ch).await {
                Ok(v) => {
                    let at = Local::now().fixed_offset();
                    let mut step = driver.input(&uuid, &v, true, SystemTime::now());
                    queue.extend(std::mem::take(&mut step.actions));
                    if !out.step(&at, &uuid, &v, step).await {
                        return false;
                    }
                    Ok(())
                }
                Err(e) => Err(format!("cannot read {uuid} ({})", redact(&e.to_string()))),
            },
            Action::Write(_, v) => p
                .write(ch, &v, WriteType::WithResponse)
                .await
                .map_err(|e| format!("cannot write to {uuid} ({})", redact(&e.to_string()))),
            Action::WriteCommand(_, v) => p
                .write(ch, &v, WriteType::WithoutResponse)
                .await
                .map_err(|e| format!("cannot write to {uuid} ({})", redact(&e.to_string()))),
        };
        if let Err(text) = res {
            out.note(text).await;
        }
    }
    true
}

/// `ohs-bridge scan`: nearby devices that advertise a supported service (all
/// named devices with `all`), strongest signal first.
pub async fn scan(seconds: u64, all: bool) -> Result<String, String> {
    let a = adapter().await?;
    a.start_scan(ScanFilter::default())
        .await
        .map_err(|e| format!("cannot scan: {e}"))?;
    tokio::time::sleep(Duration::from_secs(seconds)).await;
    let _ = a.stop_scan().await;
    let mut rows = Vec::new();
    for p in a
        .peripherals()
        .await
        .map_err(|e| format!("cannot scan: {e}"))?
    {
        let Ok(Some(props)) = p.properties().await else {
            continue;
        };
        let name = props.local_name.or(props.advertisement_name);
        let mut services: Vec<&str> = supported(&props.services)
            .filter_map(gatt::service_name)
            .collect();
        services.extend(vendor::recognized(Peer {
            name: name.as_deref(),
            services: &props.services,
        }));
        if services.is_empty() && !(all && name.is_some()) {
            continue;
        }
        rows.push((
            props.rssi.unwrap_or(i16::MIN),
            name.unwrap_or_else(|| "(no name)".into()),
            services.join(", "),
        ));
    }
    if rows.is_empty() {
        return Ok("No device advertising a supported service. Wake the device (press its button, or start a \
                   measurement) and scan again; --all lists every named device."
            .into());
    }
    rows.sort_by(|x, y| y.0.cmp(&x.0).then_with(|| x.1.cmp(&y.1)));
    let width = rows
        .iter()
        .map(|r| r.1.chars().count())
        .max()
        .unwrap_or(0)
        .max(4);
    let mut out = format!("{:<width$}  {:<7}  services\n", "name", "signal");
    for (rssi, name, services) in rows {
        let signal = if rssi == i16::MIN {
            "?".to_string()
        } else {
            format!("{rssi} dBm")
        };
        out.push_str(&format!(
            "{name:<width$}  {signal:<7}  {}\n",
            if services.is_empty() { "-" } else { &services }
        ));
    }
    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::gatt::uuid16;

    #[test]
    fn addresses_left_out_of_errors() {
        assert_eq!(
            redact("org.bluez.Error.Failed on /org/bluez/hci0/dev_C8_4F_1A_2B_3C_4D"),
            "org.bluez.Error.Failed on /org/bluez/hci0/dev_<address>"
        );
        assert_eq!(
            redact("device C8:4F:1A:2B:3C:4D gone"),
            "device <address> gone"
        );
        assert_eq!(redact("c8-4f-1a-2b-3c-4d"), "<address>");
        assert_eq!(redact("Not connected"), "Not connected");
        assert_eq!(redact("12:30:45 timeout"), "12:30:45 timeout");
        assert_eq!(redact("é C8:4F:1A:2B:3C:4D"), "é <address>");
    }

    #[test]
    fn device_matching() {
        let hr = [uuid16(gatt::HEART_RATE)];
        let other = [uuid16(0xfe95)];
        let named = Target::new(&["h10".into()], false);
        assert!(named.matches(Some("Polar H10 0A1B2C3D"), &[]));
        assert!(!named.matches(Some("TICKR 7F21"), &hr));
        assert!(!named.matches(None, &hr));
        let any = Target::new(&[], true);
        assert!(any.matches(Some("TICKR 7F21"), &hr));
        assert!(any.matches(None, &hr));
        assert!(!any.matches(Some("Mi Band"), &other));
        let both = Target::new(&["BLEsmart".into()], true);
        assert!(both.matches(Some("BLEsmart_00000397"), &other));
        assert!(both.matches(None, &hr));
    }

    #[test]
    fn supported_services() {
        let s = [
            uuid16(gatt::BLOOD_PRESSURE),
            uuid16(0x180a),
            uuid16(gatt::GLUCOSE),
        ];
        assert_eq!(
            supported(&s).collect::<Vec<_>>(),
            vec![gatt::BLOOD_PRESSURE, gatt::GLUCOSE]
        );
    }
}
