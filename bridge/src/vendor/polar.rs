// SPDX-License-Identifier: AGPL-3.0-only

//! Polar Measurement Data (PMD): how Polar straps and optical sensors (H10,
//! H9, Verity Sense, OH1, 360) stream raw ECG, accelerometer, PPG, PP
//! intervals, gyroscope and magnetometer frames. Written from the documents
//! Polar publishes next to its BLE SDK, not from the SDK's code: "Polar
//! Measurement Data" (technical_documentation/online_measurement.pdf, 2024:
//! UUIDs, op codes, measurement types, settings, frame layouts and the
//! example exchanges the tests replay), the offline recording document (the
//! width of each setting type), and documentation/ "Device Error Codes" (the
//! status codes) and "PPI Data".
//!
//! One connection: read the control point for the measurement types the
//! device offers; for each one asked for, request its settings, start it with
//! the highest value of each (what Polar's SDK proposes as its maximum
//! settings), and forward the data frames as they come. A stream the device
//! refuses is skipped, with its reason.

use std::collections::{BTreeMap, HashMap, VecDeque};
use std::time::SystemTime;

use uuid::Uuid;

use super::{Action, Driver, Live, Step};

// The service is FB005C80-02E7-F387-1CAD-8ACD2D8DF0C8; its two
// characteristics are what the driver needs.
pub const CONTROL: Uuid = Uuid::from_u128(0xfb005c81_02e7_f387_1cad_8acd2d8df0c8);
pub const DATA: Uuid = Uuid::from_u128(0xfb005c82_02e7_f387_1cad_8acd2d8df0c8);

const FEATURES: u8 = 0x0f;
const RESPONSE: u8 = 0xf0;
const GET_SETTINGS: u8 = 0x01;
const START: u8 = 0x02;

const SUCCESS: u8 = 0;
const ALREADY_IN_STATE: u8 = 6;

// Setting types, and the width of one value of each, as Polar's table
// lists them. A type outside the table ends a settings field.
const SAMPLE_RATE: u8 = 0;
const RESOLUTION: u8 = 1;
const RANGE: u8 = 2;
const RANGE_MILLIUNIT: u8 = 3;
const CHANNELS: u8 = 4;
const FACTOR: u8 = 5;

fn width(setting: u8) -> Option<usize> {
    Some(match setting {
        SAMPLE_RATE | RESOLUTION | RANGE => 2,
        RANGE_MILLIUNIT | FACTOR => 4,
        CHANNELS => 1,
        _ => return None,
    })
}

/// A measurement type the bridge can stream.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum Kind {
    Ecg,
    Ppg,
    Acc,
    Ppi,
    Gyro,
    Mag,
}

impl Kind {
    pub const ALL: [Kind; 6] = [
        Kind::Ecg,
        Kind::Ppg,
        Kind::Acc,
        Kind::Ppi,
        Kind::Gyro,
        Kind::Mag,
    ];

    /// The measurement type number, as frames and commands carry it.
    pub fn code(self) -> u8 {
        match self {
            Kind::Ecg => 0,
            Kind::Ppg => 1,
            Kind::Acc => 2,
            Kind::Ppi => 3,
            Kind::Gyro => 5,
            Kind::Mag => 6,
        }
    }

    fn from_code(code: u8) -> Option<Kind> {
        Kind::ALL.into_iter().find(|k| k.code() == code)
    }

    /// Its bit in the feature bitmask (the second byte of the feature read).
    fn feature_bit(self) -> u8 {
        1 << match self {
            Kind::Ecg => 0,
            Kind::Ppg => 1,
            Kind::Acc => 2,
            Kind::Ppi => 3,
            Kind::Gyro => 5,
            Kind::Mag => 6,
        }
    }

    pub fn name(self) -> &'static str {
        match self {
            Kind::Ecg => "ECG",
            Kind::Ppg => "PPG",
            Kind::Acc => "accelerometer",
            Kind::Ppi => "PP intervals",
            Kind::Gyro => "gyroscope",
            Kind::Mag => "magnetometer",
        }
    }

    fn word(self) -> &'static str {
        match self {
            Kind::Ecg => "ecg",
            Kind::Ppg => "ppg",
            Kind::Acc => "acc",
            Kind::Ppi => "ppi",
            Kind::Gyro => "gyro",
            Kind::Mag => "mag",
        }
    }
}

/// What `run` and `watch` stream without `--polar`: the ECG of a strap and
/// the PP intervals of an optical sensor. Motion streams are many times the
/// volume and asked for by name.
pub const DEFAULT_STREAMS: [Kind; 2] = [Kind::Ecg, Kind::Ppi];

/// The `--polar` list: `ecg,acc,ppi`, `all`, or `off`.
pub fn parse_streams(s: &str) -> Result<Vec<Kind>, String> {
    match s {
        "off" | "none" => return Ok(Vec::new()),
        "all" => return Ok(Kind::ALL.to_vec()),
        _ => {}
    }
    let mut out = Vec::new();
    for w in s.split(',').map(str::trim).filter(|w| !w.is_empty()) {
        let k = Kind::ALL
            .into_iter()
            .find(|k| k.word() == w.to_ascii_lowercase())
            .ok_or_else(|| {
                format!("--polar: unknown stream {w} (ecg, ppg, acc, ppi, gyro, mag, all or off)")
            })?;
        if !out.contains(&k) {
            out.push(k);
        }
    }
    if out.is_empty() {
        return Err("--polar takes ecg, ppg, acc, ppi, gyro, mag, all or off".into());
    }
    Ok(out)
}

/// Whether a device serves PMD.
pub fn present(chars: &[Uuid]) -> bool {
    chars.contains(&CONTROL) && chars.contains(&DATA)
}

/// The settings in a PMD settings field: per setting type, its values. A
/// type this parser does not know ends the field: its width is unknown.
pub fn parse_settings(b: &[u8]) -> BTreeMap<u8, Vec<u32>> {
    let mut out: BTreeMap<u8, Vec<u32>> = BTreeMap::new();
    let mut i = 0;
    while i + 2 <= b.len() {
        let (setting, count) = (b[i], b[i + 1] as usize);
        let Some(w) = width(setting) else { break };
        let end = i + 2 + count * w;
        if end > b.len() {
            break;
        }
        let values = out.entry(setting).or_default();
        for v in b[i + 2..end].chunks(w) {
            let n = v.iter().rev().fold(0u32, |n, b| (n << 8) | *b as u32);
            values.push(n);
        }
        i = end;
    }
    out
}

/// The settings to start a stream with: the highest offered sample rate,
/// resolution, range and channel count, in setting order. Other settings
/// (security, derived measurements) are not asked for.
pub fn choose(offered: &BTreeMap<u8, Vec<u32>>) -> Vec<u8> {
    let mut out = Vec::new();
    for setting in [SAMPLE_RATE, RESOLUTION, RANGE, RANGE_MILLIUNIT, CHANNELS] {
        let Some(max) = offered.get(&setting).and_then(|v| v.iter().max()) else {
            continue;
        };
        let w = width(setting).unwrap_or(2);
        out.extend([setting, 1]);
        out.extend(&max.to_le_bytes()[..w]);
    }
    out
}

/// A status code's meaning, as Polar's error code table names it.
fn status_text(status: u8) -> &'static str {
    match status {
        1 => "invalid op code",
        2 => "invalid measurement type",
        3 => "not supported",
        4 => "invalid length",
        5 => "invalid parameter",
        7 => "invalid resolution",
        8 => "invalid sample rate",
        9 => "invalid range",
        10 => "MTU too small",
        11 => "invalid number of channels",
        12 => "invalid state",
        13 => "the device is on its charger",
        14 => "disk full",
        _ => "error",
    }
}

#[derive(Debug, PartialEq)]
enum State {
    /// The feature read is awaited.
    Features,
    /// The settings of `kind` are awaited, gathered over `more` packets.
    Settings {
        kind: Kind,
        params: Vec<u8>,
    },
    /// The answer to a start request is awaited.
    Start {
        kind: Kind,
        request: Vec<u8>,
    },
    Idle,
}

/// The PMD driver of one connection.
pub struct Polar {
    want: Vec<Kind>,
    queue: VecDeque<Kind>,
    state: State,
    /// Per measurement type started: the type, the settings it was started
    /// with, and the factor the device answered, as settings fields. Sent as
    /// the `ctx` of each of its frames.
    ctx: HashMap<u8, Vec<u8>>,
}

impl Polar {
    pub fn new(want: &[Kind]) -> Polar {
        Polar {
            want: want.to_vec(),
            queue: VecDeque::new(),
            state: State::Idle,
            ctx: HashMap::new(),
        }
    }

    /// Asks for the next stream's settings, or starts it straight away
    /// when it has none (PP intervals).
    fn next(&mut self) -> Step {
        match self.queue.pop_front() {
            None => {
                self.state = State::Idle;
                Step::default()
            }
            Some(Kind::Ppi) => self.start_stream(Kind::Ppi, &BTreeMap::new()),
            Some(kind) => {
                self.state = State::Settings {
                    kind,
                    params: Vec::new(),
                };
                Step::act(vec![Action::Write(
                    CONTROL,
                    vec![GET_SETTINGS, kind.code()],
                )])
            }
        }
    }

    fn start_stream(&mut self, kind: Kind, offered: &BTreeMap<u8, Vec<u32>>) -> Step {
        let mut request = vec![START, kind.code()];
        request.extend(choose(offered));
        self.state = State::Start {
            kind,
            request: request.clone(),
        };
        Step::act(vec![Action::Write(CONTROL, request)])
    }

    fn features(&mut self, v: &[u8]) -> Step {
        let offered = |k: Kind| v.get(1).is_some_and(|b| b & k.feature_bit() != 0);
        self.queue = self.want.iter().copied().filter(|k| offered(*k)).collect();
        if self.queue.is_empty() {
            self.state = State::Idle;
            return Step::default().note("offers none of the Polar streams asked for");
        }
        self.next()
    }

    /// An indication on the control point: the answer to a request.
    fn response(&mut self, v: &[u8]) -> Step {
        if v.len() < 4 || v[0] != RESPONSE {
            return Step::default();
        }
        let (op, code, status) = (v[1], v[2], v[3]);
        let more = status == SUCCESS && v.get(4).is_some_and(|m| *m != 0);
        let params = v.get(5..).unwrap_or(&[]);
        match &mut self.state {
            State::Settings { kind, params: got } if op == GET_SETTINGS && code == kind.code() => {
                let kind = *kind;
                if status != SUCCESS {
                    let step = self.next();
                    return step.note(format!(
                        "{}: settings refused ({})",
                        kind.name(),
                        status_text(status)
                    ));
                }
                got.extend_from_slice(params);
                if more {
                    return Step::default();
                }
                let offered = parse_settings(got);
                self.start_stream(kind, &offered)
            }
            State::Start { kind, request } if op == START && code == kind.code() => {
                let kind = *kind;
                if status != SUCCESS && status != ALREADY_IN_STATE {
                    let step = self.next();
                    return step.note(format!(
                        "{} not started ({})",
                        kind.name(),
                        status_text(status)
                    ));
                }
                let mut ctx = vec![kind.code()];
                ctx.extend_from_slice(&request[2..]);
                if let Some(f) = parse_settings(params).get(&FACTOR).and_then(|f| f.first()) {
                    ctx.extend([FACTOR, 1]);
                    ctx.extend(f.to_le_bytes());
                }
                let rate = parse_settings(&request[2..])
                    .get(&SAMPLE_RATE)
                    .and_then(|r| r.first().copied());
                self.ctx.insert(kind.code(), ctx);
                let step = self.next();
                step.note(match rate {
                    Some(hz) => format!("streaming {} at {hz} Hz", kind.name()),
                    None => format!("streaming {}", kind.name()),
                })
            }
            _ => Step::default(),
        }
    }
}

impl Driver for Polar {
    fn start(&mut self, _now: SystemTime) -> Vec<Action> {
        self.state = State::Features;
        vec![
            Action::Subscribe(CONTROL),
            Action::Subscribe(DATA),
            Action::Read(CONTROL),
        ]
    }

    fn owns(&self, char: &Uuid) -> bool {
        *char == CONTROL || *char == DATA
    }

    fn input(&mut self, char: &Uuid, value: &[u8], _read: bool, _now: SystemTime) -> Step {
        if *char == DATA {
            let Some(code) = value.first().map(|b| b & 0x3f) else {
                return Step::default();
            };
            return Step {
                upload: Some(self.ctx.get(&code).cloned().unwrap_or_default()),
                live: describe(value),
                ..Default::default()
            };
        }
        if *char != CONTROL {
            return Step::default();
        }
        // Some stacks hand a read back as a notification too: the first
        // byte tells the feature read (0x0F) from a response (0xF0).
        match value.first() {
            Some(&FEATURES) if self.state == State::Features => self.features(value),
            Some(&RESPONSE) => self.response(value),
            _ => Step::default(),
        }
    }

    fn waiting(&self) -> bool {
        self.state != State::Idle
    }

    fn timeout(&mut self, _now: SystemTime) -> Step {
        let what = match &self.state {
            State::Features => {
                self.state = State::Idle;
                return Step::default().note("no answer from the Polar measurement service");
            }
            State::Settings { kind, .. } | State::Start { kind, .. } => kind.name(),
            State::Idle => return Step::default(),
        };
        self.next().note(format!("{what}: no answer, skipped"))
    }
}

/// The live view of a data frame: how many samples it holds, and for PP
/// intervals the intervals and heart rate. Frames start with the measurement
/// type, an 8-byte timestamp and the frame type (compressed when its top bit
/// is set); samples follow from byte 10.
pub fn describe(frame: &[u8]) -> Option<Live> {
    let kind = Kind::from_code(*frame.first()? & 0x3f)?;
    let frame_type = *frame.get(9)?;
    let content = &frame[10..];
    if frame_type & 0x80 != 0 {
        return Some(Live {
            text: format!("{} frame, {} bytes", kind.name(), content.len()),
            heart_rate: None,
        });
    }
    match (kind, frame_type) {
        // Signed 24-bit microvolts.
        (Kind::Ecg, 0) => Some(Live {
            text: format!("ECG {} samples", content.len() / 3),
            heart_rate: None,
        }),
        // Heart rate, PP interval (ms), error estimate (ms), flags: bit 0
        // set when the interval is not valid.
        (Kind::Ppi, 0) => {
            let samples = content.as_chunks::<6>().0;
            let pp: Vec<String> = samples
                .iter()
                .filter(|s| s[5] & 0x01 == 0)
                .map(|s| u16::from_le_bytes([s[1], s[2]]).to_string())
                .collect();
            let hr = samples.last().map(|s| s[0] as u16).filter(|h| *h > 0);
            let mut text = match hr {
                Some(h) => format!("{h} bpm"),
                None => "PP intervals".to_string(),
            };
            if !pp.is_empty() {
                text.push_str(&format!("  PP {} ms", pp.join(" ")));
            }
            Some(Live {
                text,
                heart_rate: hr,
            })
        }
        _ => Some(Live {
            text: format!("{} frame, {} bytes", kind.name(), content.len()),
            heart_rate: None,
        }),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const NOW: SystemTime = SystemTime::UNIX_EPOCH;

    fn writes(step: &Step) -> Vec<Vec<u8>> {
        step.actions
            .iter()
            .filter_map(|a| match a {
                Action::Write(c, v) if *c == CONTROL => Some(v.clone()),
                _ => None,
            })
            .collect()
    }

    // Answers laid out as the PMD document shows them for an H10: the
    // response marker, the op code answered, the measurement type, the
    // status, the "more" flag, then the settings.
    const H10_FEATURES: [u8; 3] = [0x0f, 0x05, 0x00]; // ECG and accelerometer
    const H10_ECG_SETTINGS: [u8; 13] = [
        0xf0, 0x01, 0x00, 0x00, 0x00, // header
        0x00, 0x01, 0x82, 0x00, // sample rate: 130
        0x01, 0x01, 0x0e, 0x00, // resolution: 14
    ];
    const H10_ACC_SETTINGS: [u8; 27] = [
        0xf0, 0x01, 0x02, 0x00, 0x00, // header
        0x00, 0x04, 0x19, 0x00, 0x32, 0x00, 0x64, 0x00, 0xc8, 0x00, // 25 50 100 200 Hz
        0x01, 0x01, 0x10, 0x00, // 16 bits
        0x02, 0x03, 0x02, 0x00, 0x04, 0x00, 0x08, 0x00, // 2 4 8 G
    ];

    #[test]
    fn streams_by_name() {
        assert_eq!(parse_streams("ecg,acc"), Ok(vec![Kind::Ecg, Kind::Acc]));
        assert_eq!(
            parse_streams("ECG, ppi,ecg"),
            Ok(vec![Kind::Ecg, Kind::Ppi])
        );
        assert_eq!(parse_streams("all").unwrap().len(), 6);
        assert_eq!(parse_streams("off"), Ok(vec![]));
        assert!(parse_streams("ekg").is_err());
        assert!(parse_streams(",").is_err());
    }

    #[test]
    fn settings_parse_and_choice() {
        let s = parse_settings(&H10_ACC_SETTINGS[5..]);
        assert_eq!(s[&SAMPLE_RATE], vec![25, 50, 100, 200]);
        assert_eq!(s[&RESOLUTION], vec![16]);
        assert_eq!(s[&RANGE], vec![2, 4, 8]);
        assert_eq!(
            choose(&s),
            vec![0x00, 0x01, 0xc8, 0x00, 0x01, 0x01, 0x10, 0x00, 0x02, 0x01, 0x08, 0x00]
        );
        // Truncated, or a setting type of unknown width: what came before
        // is kept.
        assert_eq!(parse_settings(&[0x00, 0x02, 0x82, 0x00, 0x41]).len(), 0);
        let s = parse_settings(&[0x00, 0x01, 0x82, 0x00, 0x7f, 0x01, 0x00]);
        assert_eq!(s[&SAMPLE_RATE], vec![130]);
        assert_eq!(s.len(), 1);
        // Channels (one byte) and a range in milliunits (four).
        let s = parse_settings(&[0x04, 0x01, 0x04, 0x03, 0x01, 0xa0, 0x86, 0x01, 0x00]);
        assert_eq!(s[&CHANNELS], vec![4]);
        assert_eq!(s[&RANGE_MILLIUNIT], vec![100_000]);
        assert_eq!(
            choose(&s),
            vec![0x03, 0x01, 0xa0, 0x86, 0x01, 0x00, 0x04, 0x01, 0x04]
        );
    }

    #[test]
    fn h10_starts_ecg_then_accelerometer() {
        let mut p = Polar::new(&[Kind::Ecg, Kind::Acc, Kind::Ppg]);
        assert_eq!(
            p.start(NOW),
            vec![
                Action::Subscribe(CONTROL),
                Action::Subscribe(DATA),
                Action::Read(CONTROL)
            ]
        );
        assert!(p.waiting());
        // PPG is not offered: it is never asked for.
        let s = p.input(&CONTROL, &H10_FEATURES, true, NOW);
        assert_eq!(writes(&s), vec![vec![0x01, 0x00]]);
        let s = p.input(&CONTROL, &H10_ECG_SETTINGS, false, NOW);
        // Byte for byte the start request of Polar's H10 diagram.
        assert_eq!(
            writes(&s),
            vec![vec![
                0x02, 0x00, 0x00, 0x01, 0x82, 0x00, 0x01, 0x01, 0x0e, 0x00
            ]]
        );
        let s = p.input(&CONTROL, &[0xf0, 0x02, 0x00, 0x00, 0x00], false, NOW);
        assert_eq!(s.note.as_deref(), Some("streaming ECG at 130 Hz"));
        assert_eq!(writes(&s), vec![vec![0x01, 0x02]]);
        let s = p.input(&CONTROL, &H10_ACC_SETTINGS, false, NOW);
        assert_eq!(
            writes(&s),
            vec![vec![
                0x02, 0x02, 0x00, 0x01, 0xc8, 0x00, 0x01, 0x01, 0x10, 0x00, 0x02, 0x01, 0x08, 0x00
            ]]
        );
        // Already streaming from an earlier connection counts as started.
        let s = p.input(&CONTROL, &[0xf0, 0x02, 0x02, 0x06], false, NOW);
        assert_eq!(s.note.as_deref(), Some("streaming accelerometer at 200 Hz"));
        assert!(s.actions.is_empty());
        assert!(!p.waiting());

        // Frames go up with the settings their stream was started with.
        let mut ecg = vec![0x00, 1, 2, 3, 4, 5, 6, 7, 8, 0x00];
        ecg.extend([0xe8, 0x03, 0x00, 0x18, 0xfc, 0xff]); // 1000 µV, -1000 µV
        let s = p.input(&DATA, &ecg, false, NOW);
        assert_eq!(
            s.upload,
            Some(vec![0x00, 0x00, 0x01, 0x82, 0x00, 0x01, 0x01, 0x0e, 0x00])
        );
        assert_eq!(s.live.unwrap().text, "ECG 2 samples");
        let s = p.input(
            &DATA,
            &[0x02, 0, 0, 0, 0, 0, 0, 0, 0, 0x01, 1, 0, 2, 0, 3, 0],
            false,
            NOW,
        );
        assert_eq!(s.upload.unwrap()[0], 0x02);
        // A frame of a type never started still goes up, without context.
        let s = p.input(&DATA, &[0x05, 0, 0, 0, 0, 0, 0, 0, 0, 0x00], false, NOW);
        assert_eq!(s.upload, Some(vec![]));
    }

    #[test]
    fn settings_over_several_packets() {
        let mut p = Polar::new(&[Kind::Acc]);
        p.start(NOW);
        p.input(&CONTROL, &H10_FEATURES, true, NOW);
        let first = [0xf0, 0x01, 0x02, 0x00, 0x01, 0x00, 0x01, 0xc8, 0x00];
        assert!(p.input(&CONTROL, &first, false, NOW).actions.is_empty());
        let last = [0xf0, 0x01, 0x02, 0x00, 0x00, 0x02, 0x01, 0x08, 0x00];
        assert_eq!(
            writes(&p.input(&CONTROL, &last, false, NOW)),
            vec![vec![
                0x02, 0x02, 0x00, 0x01, 0xc8, 0x00, 0x02, 0x01, 0x08, 0x00
            ]]
        );
    }

    #[test]
    fn verity_sense_accelerometer_as_published() {
        // The exchange of Polar's diagrams for a Verity Sense, byte for byte:
        // features, accelerometer settings, start, and the answer that
        // carries the factor 0x397FDA40.
        let mut p = Polar::new(&[Kind::Acc]);
        p.start(NOW);
        let mut features = vec![0x0f, 0x6e];
        features.extend([0; 15]);
        let s = p.input(&CONTROL, &features, true, NOW);
        assert_eq!(writes(&s), vec![vec![0x01, 0x02]]);
        let settings = [
            0xf0, 0x01, 0x02, 0x00, 0x00, 0x00, 0x01, 0x34, 0x00, 0x01, 0x01, 0x10, 0x00, 0x02,
            0x01, 0x08, 0x00, 0x04, 0x01, 0x03,
        ];
        let s = p.input(&CONTROL, &settings, false, NOW);
        assert_eq!(
            writes(&s),
            vec![vec![
                0x02, 0x02, 0x00, 0x01, 0x34, 0x00, 0x01, 0x01, 0x10, 0x00, 0x02, 0x01, 0x08, 0x00,
                0x04, 0x01, 0x03
            ]]
        );
        let answer = [
            0xf0, 0x02, 0x02, 0x00, 0x00, 0x05, 0x01, 0x40, 0xda, 0x7f, 0x39,
        ];
        let s = p.input(&CONTROL, &answer, false, NOW);
        assert_eq!(s.note.as_deref(), Some("streaming accelerometer at 52 Hz"));
        let frame = [0x02, 0, 0, 0, 0, 0, 0, 0, 0, 0x81];
        let ctx = p.input(&DATA, &frame, false, NOW).upload.unwrap();
        assert_eq!(&ctx[ctx.len() - 6..], &[0x05, 0x01, 0x40, 0xda, 0x7f, 0x39]);

        // The H10 answers a start with one reserved byte, not a factor.
        let mut p = Polar::new(&[Kind::Acc]);
        p.start(NOW);
        p.input(&CONTROL, &H10_FEATURES, true, NOW);
        p.input(&CONTROL, &H10_ACC_SETTINGS, false, NOW);
        p.input(&CONTROL, &[0xf0, 0x02, 0x02, 0x00, 0x00, 0x01], false, NOW);
        let ctx = p.input(&DATA, &frame, false, NOW).upload.unwrap();
        assert_eq!(
            ctx,
            vec![0x02, 0x00, 0x01, 0xc8, 0x00, 0x01, 0x01, 0x10, 0x00, 0x02, 0x01, 0x08, 0x00]
        );
    }

    #[test]
    fn optical_sensor_pp_intervals_and_factor() {
        // An optical sensor offering PPG, accelerometer, PPI, gyroscope and
        // magnetometer.
        let mut p = Polar::new(&[Kind::Ppi, Kind::Ppg]);
        p.start(NOW);
        let s = p.input(&CONTROL, &[0x0f, 0x6e, 0x00], true, NOW);
        // PP intervals have no settings: started at once.
        assert_eq!(writes(&s), vec![vec![0x02, 0x03]]);
        let s = p.input(&CONTROL, &[0xf0, 0x02, 0x03, 0x00, 0x00], false, NOW);
        assert_eq!(s.note.as_deref(), Some("streaming PP intervals"));
        assert_eq!(writes(&s), vec![vec![0x01, 0x01]]);
        let ppg = [
            0xf0, 0x01, 0x01, 0x00, 0x00, 0x00, 0x01, 0x37, 0x00, 0x01, 0x01, 0x16, 0x00, 0x04,
            0x01, 0x04,
        ];
        let s = p.input(&CONTROL, &ppg, false, NOW);
        assert_eq!(
            writes(&s),
            vec![vec![
                0x02, 0x01, 0x00, 0x01, 0x37, 0x00, 0x01, 0x01, 0x16, 0x00, 0x04, 0x01, 0x04
            ]]
        );
        // The start answer carries the factor of the compressed frames.
        let s = p.input(
            &CONTROL,
            &[
                0xf0, 0x02, 0x01, 0x00, 0x00, 0x05, 0x01, 0x00, 0x00, 0x80, 0x3f,
            ],
            false,
            NOW,
        );
        assert_eq!(s.note.as_deref(), Some("streaming PPG at 55 Hz"));
        let s = p.input(&DATA, &[0x01, 0, 0, 0, 0, 0, 0, 0, 0, 0x80, 9], false, NOW);
        assert_eq!(
            s.upload,
            Some(vec![
                0x01, 0x00, 0x01, 0x37, 0x00, 0x01, 0x01, 0x16, 0x00, 0x04, 0x01, 0x04, 0x05, 0x01,
                0x00, 0x00, 0x80, 0x3f
            ])
        );
        // Two intervals, the second one flagged as not valid.
        let mut ppi = vec![0x03, 0, 0, 0, 0, 0, 0, 0, 0, 0x00];
        ppi.extend([72, 0x2c, 0x03, 0x0a, 0x00, 0x06]); // 812 ms
        ppi.extend([74, 0x25, 0x03, 0x0a, 0x00, 0x07]); // 805 ms, not valid
        let s = p.input(&DATA, &ppi, false, NOW);
        assert_eq!(s.upload, Some(vec![0x03]));
        assert_eq!(
            s.live,
            Some(Live {
                text: "74 bpm  PP 812 ms".into(),
                heart_rate: Some(74)
            })
        );
    }

    #[test]
    fn refusals_and_silence_skip_a_stream() {
        let mut p = Polar::new(&[Kind::Ecg, Kind::Acc]);
        p.start(NOW);
        p.input(&CONTROL, &H10_FEATURES, true, NOW);
        // The ECG settings refused: on to the accelerometer.
        let s = p.input(&CONTROL, &[0xf0, 0x01, 0x00, 0x0d], false, NOW);
        assert_eq!(
            s.note.as_deref(),
            Some("ECG: settings refused (the device is on its charger)")
        );
        assert_eq!(writes(&s), vec![vec![0x01, 0x02]]);
        let s = p.timeout(NOW);
        assert_eq!(s.note.as_deref(), Some("accelerometer: no answer, skipped"));
        assert!(!p.waiting());
        // An answer to something else is ignored.
        assert_eq!(
            p.input(&CONTROL, &[0xf0, 0x02, 0x00, 0x00], false, NOW),
            Step::default()
        );

        let mut p = Polar::new(&[Kind::Ecg]);
        p.start(NOW);
        p.input(&CONTROL, &H10_FEATURES, true, NOW);
        p.input(&CONTROL, &H10_ECG_SETTINGS, false, NOW);
        let s = p.input(&CONTROL, &[0xf0, 0x02, 0x00, 0x0a], false, NOW);
        assert_eq!(s.note.as_deref(), Some("ECG not started (MTU too small)"));
        assert!(!p.waiting());

        let mut p = Polar::new(&[Kind::Gyro]);
        p.start(NOW);
        let s = p.input(&CONTROL, &H10_FEATURES, true, NOW);
        assert_eq!(
            s.note.as_deref(),
            Some("offers none of the Polar streams asked for")
        );
        let mut p = Polar::new(&[Kind::Ecg]);
        p.start(NOW);
        assert_eq!(
            p.timeout(NOW).note.as_deref(),
            Some("no answer from the Polar measurement service")
        );
    }

    #[test]
    fn frames_described() {
        assert_eq!(describe(&[]), None);
        assert_eq!(describe(&[0x00, 1, 2]), None);
        assert_eq!(describe(&[0x3f, 0, 0, 0, 0, 0, 0, 0, 0, 0]), None);
        assert_eq!(
            describe(&[0x02, 0, 0, 0, 0, 0, 0, 0, 0, 0x81, 1, 2])
                .unwrap()
                .text,
            "accelerometer frame, 2 bytes"
        );
        let mut ppi = vec![0x03, 0, 0, 0, 0, 0, 0, 0, 0, 0x00];
        ppi.extend([0, 0x2c, 0x03, 0x0a, 0x00, 0x00]);
        assert_eq!(
            describe(&ppi),
            Some(Live {
                text: "PP intervals  PP 812 ms".into(),
                heart_rate: None
            })
        );
    }

    #[test]
    fn detection() {
        assert!(present(&[CONTROL, DATA]));
        assert!(!present(&[CONTROL]));
        let peer = super::super::Peer {
            name: Some("Polar H10 0A1B2C3D"),
            services: &[],
        };
        let ctx = super::super::Context::default();
        let cfg = super::super::Config::default();
        assert!(super::super::detect(peer, &[CONTROL, DATA], &cfg, ctx).is_some());
        let off = super::super::Config { polar: vec![] };
        assert!(super::super::detect(peer, &[CONTROL, DATA], &off, ctx).is_none());
    }
}
