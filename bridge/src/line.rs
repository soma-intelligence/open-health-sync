// SPDX-License-Identifier: AGPL-3.0-only

//! The upload line: one characteristic value as the device sent it, with the
//! time it arrived, or one value a vendor driver decoded. The format is the
//! upload contract of `POST /v1/ingest/bluetooth`
//! (docs/connectors/bluetooth.md).

use chrono::{DateTime, FixedOffset, SecondsFormat};
use serde::{Deserialize, Serialize};
use uuid::Uuid;

use crate::codec;

/// The longest value a line may carry, the ATT maximum attribute length.
pub const MAX_VALUE_BYTES: usize = 512;

/// The longest context a line may carry: the bytes a vendor protocol adds so
/// the server can read a value on its own (a Polar stream's settings).
pub const MAX_CTX_BYTES: usize = 64;

/// The longest device string, in characters.
const MAX_TEXT: usize = 128;

/// What the device says about itself. Never an address or a serial number.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct Device {
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub name: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub manufacturer: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub model: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub firmware: String,
}

impl Device {
    fn is_empty(&self) -> bool {
        self.name.is_empty()
            && self.manufacturer.is_empty()
            && self.model.is_empty()
            && self.firmware.is_empty()
    }
}

/// A device string as a device sends it (UTF-8, often padded with NULs),
/// cleaned: no control characters, trimmed, at most 128 characters.
pub fn text(raw: &[u8]) -> String {
    let s = String::from_utf8_lossy(raw);
    let s: String = s.chars().filter(|c| !c.is_control()).collect();
    s.trim()
        .chars()
        .take(MAX_TEXT)
        .collect::<String>()
        .trim_end()
        .to_string()
}

#[derive(Serialize)]
struct Line<'a> {
    at: String,
    char: String,
    value: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    ctx: String,
    session: &'a str,
    #[serde(skip_serializing_if = "Option::is_none")]
    device: Option<&'a Device>,
    #[serde(skip_serializing_if = "Option::is_none")]
    device_user: Option<u8>,
}

/// The line of one value, newline included. None for an empty value or one
/// longer than the ATT maximum, which the server would refuse.
pub fn encode(
    at: &DateTime<FixedOffset>,
    char: u16,
    value: &[u8],
    session: &str,
    device: &Device,
    device_user: Option<u8>,
) -> Option<String> {
    encode_line(
        at,
        format!("{char:04x}"),
        value,
        &[],
        session,
        device,
        device_user,
    )
}

/// The line of a value on a vendor characteristic: its full UUID, and the
/// context bytes its driver gave. None when the value or the context is
/// longer than the server takes.
pub fn encode_vendor(
    at: &DateTime<FixedOffset>,
    char: &Uuid,
    value: &[u8],
    ctx: &[u8],
    session: &str,
    device: &Device,
) -> Option<String> {
    if ctx.len() > MAX_CTX_BYTES {
        return None;
    }
    encode_line(at, char.to_string(), value, ctx, session, device, None)
}

fn encode_line(
    at: &DateTime<FixedOffset>,
    char: String,
    value: &[u8],
    ctx: &[u8],
    session: &str,
    device: &Device,
    device_user: Option<u8>,
) -> Option<String> {
    if value.is_empty() || value.len() > MAX_VALUE_BYTES {
        return None;
    }
    let l = Line {
        at: at.to_rfc3339_opts(SecondsFormat::Millis, false),
        char,
        value: codec::hex(value),
        ctx: codec::hex(ctx),
        session,
        device: (!device.is_empty()).then_some(device),
        device_user,
    };
    let mut s = serde_json::to_string(&l).ok()?;
    s.push('\n');
    Some(s)
}

#[derive(Serialize)]
struct SampleLine<'a> {
    at: String,
    char: String,
    session: &'a str,
    #[serde(skip_serializing_if = "Option::is_none")]
    device: Option<&'a Device>,
    sample: SampleField,
}

#[derive(Serialize)]
struct SampleField {
    #[serde(rename = "type")]
    kind: &'static str,
    value: f64,
    unit: &'static str,
    start: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    end: Option<String>,
}

/// The line of a value a vendor driver decoded: `char` is the characteristic
/// it came from. None for a value that is not a finite number, or a time
/// before 1970 or that chrono cannot hold.
pub fn encode_sample(
    at: &DateTime<FixedOffset>,
    char: &Uuid,
    sample: &ohs_driver::Sample,
    session: &str,
    device: &Device,
) -> Option<String> {
    if !sample.value.is_finite() {
        return None;
    }
    let time = |t: std::time::SystemTime| -> Option<String> {
        let d = t.duration_since(std::time::UNIX_EPOCH).ok()?;
        let t = DateTime::from_timestamp(i64::try_from(d.as_secs()).ok()?, d.subsec_nanos())?;
        Some(t.to_rfc3339_opts(SecondsFormat::Millis, true))
    };
    let end = match sample.end {
        Some(e) => Some(time(e)?),
        None => None,
    };
    let l = SampleLine {
        at: at.to_rfc3339_opts(SecondsFormat::Millis, false),
        char: char.to_string(),
        session,
        device: (!device.is_empty()).then_some(device),
        sample: SampleField {
            kind: sample.kind,
            value: sample.value,
            unit: sample.unit,
            start: time(sample.start)?,
            end,
        },
    };
    let mut s = serde_json::to_string(&l).ok()?;
    s.push('\n');
    Some(s)
}

/// The UTC day (YYYY-MM-DD) of a line's `at`.
pub fn utc_day(line: &str) -> Option<String> {
    #[derive(Deserialize)]
    struct At {
        at: String,
    }
    let a: At = serde_json::from_str(line).ok()?;
    let t = DateTime::parse_from_rfc3339(&a.at).ok()?;
    Some(t.naive_utc().date().format("%Y-%m-%d").to_string())
}

/// An advertised name without a last word that looks like a serial number:
/// hex digits after a space or an underscore, at least six, or at least four
/// with a letter among them. "Polar H10 0A1B2C3D" is sent as "Polar H10";
/// "Edge 1040" is kept. The server applies the same rule.
pub fn public_name(name: &str) -> String {
    let name = name.trim();
    if let Some(i) = name.rfind([' ', '_']).filter(|i| *i > 0) {
        let tail = &name[i + 1..];
        let letter = tail.bytes().any(|c| c.is_ascii_alphabetic());
        if tail.bytes().all(|c| c.is_ascii_hexdigit())
            && (tail.len() >= 6 || tail.len() >= 4 && letter)
        {
            return name[..i].trim().to_string();
        }
    }
    name.to_string()
}

/// A new session id: one per connection to one device. It names the RR series.
pub fn new_session() -> String {
    codec::random_id()
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::TimeZone;

    fn at() -> DateTime<FixedOffset> {
        FixedOffset::east_opt(2 * 3600)
            .unwrap()
            .with_ymd_and_hms(2026, 10, 16, 21, 4, 5)
            .unwrap()
            + chrono::Duration::milliseconds(120)
    }

    #[test]
    fn line_matches_the_upload_contract() {
        let dev = Device {
            name: "Polar H10 0A1B2C3D".into(),
            manufacturer: "Polar Electro Oy".into(),
            model: "H10".into(),
            firmware: "5.0.0".into(),
        };
        let l = encode(
            &at(),
            0x2a37,
            &[0x10, 0x48, 0x00, 0x04, 0x33, 0x03, 0x00, 0x04],
            "9f2c4e1a7b3d5c60",
            &dev,
            None,
        )
        .unwrap();
        assert_eq!(
            l,
            r#"{"at":"2026-10-16T21:04:05.120+02:00","char":"2a37","value":"1048000433030004","session":"9f2c4e1a7b3d5c60","device":{"name":"Polar H10 0A1B2C3D","manufacturer":"Polar Electro Oy","model":"H10","firmware":"5.0.0"}}"#
                .to_string()
                + "\n"
        );
    }

    #[test]
    fn optional_fields() {
        let l = encode(
            &at(),
            0x2a35,
            &[0x00],
            "abcdefgh",
            &Device::default(),
            Some(1),
        )
        .unwrap();
        assert_eq!(
            l,
            "{\"at\":\"2026-10-16T21:04:05.120+02:00\",\"char\":\"2a35\",\"value\":\"00\",\"session\":\"abcdefgh\",\"device_user\":1}\n"
        );
        let utc = Utc
            .with_ymd_and_hms(2026, 1, 2, 3, 4, 5)
            .unwrap()
            .fixed_offset();
        assert!(
            encode(&utc, 0x2a19, &[50], "abcdefgh", &Device::default(), None)
                .unwrap()
                .contains("\"at\":\"2026-01-02T03:04:05.000+00:00\"")
        );
    }

    use chrono::Utc;

    #[test]
    fn values_the_server_refuses_are_not_encoded() {
        assert!(encode(&at(), 0x2a37, &[], "abcdefgh", &Device::default(), None).is_none());
        assert!(encode(
            &at(),
            0x2a37,
            &[0; 513],
            "abcdefgh",
            &Device::default(),
            None
        )
        .is_none());
        assert!(encode(
            &at(),
            0x2a37,
            &[0; 512],
            "abcdefgh",
            &Device::default(),
            None
        )
        .is_some());
    }

    #[test]
    fn vendor_values_carry_their_uuid_and_context() {
        let pmd: Uuid = "fb005c82-02e7-f387-1cad-8acd2d8df0c8".parse().unwrap();
        let l = encode_vendor(
            &at(),
            &pmd,
            &[0x00, 0x01],
            &[0x00, 0x00, 0x01, 0x82, 0x00],
            "abcdefgh",
            &Device::default(),
        )
        .unwrap();
        assert_eq!(
            l,
            "{\"at\":\"2026-10-16T21:04:05.120+02:00\",\"char\":\"fb005c82-02e7-f387-1cad-8acd2d8df0c8\",\"value\":\"0001\",\"ctx\":\"0000018200\",\"session\":\"abcdefgh\"}\n"
        );
        let plain = encode_vendor(&at(), &pmd, &[0x01], &[], "abcdefgh", &Device::default());
        assert!(!plain.unwrap().contains("ctx"));
        assert!(
            encode_vendor(&at(), &pmd, &[1], &[0; 65], "abcdefgh", &Device::default()).is_none()
        );
        assert!(encode_vendor(&at(), &pmd, &[], &[], "abcdefgh", &Device::default()).is_none());
    }

    #[test]
    fn decoded_samples() {
        use std::time::{Duration, UNIX_EPOCH};
        let ch: Uuid = "61080005-8d6d-82b8-614a-1c8cb0f8dcc6".parse().unwrap();
        let start = UNIX_EPOCH + Duration::from_millis(1_792_177_445_120);
        let hr = ohs_driver::Sample::at("heart_rate", 62.0, "bpm", start);
        let dev = Device {
            name: "Band".into(),
            ..Default::default()
        };
        assert_eq!(
            encode_sample(&at(), &ch, &hr, "abcdefgh", &dev).unwrap(),
            "{\"at\":\"2026-10-16T21:04:05.120+02:00\",\"char\":\"61080005-8d6d-82b8-614a-1c8cb0f8dcc6\",\"session\":\"abcdefgh\",\"device\":{\"name\":\"Band\"},\"sample\":{\"type\":\"heart_rate\",\"value\":62.0,\"unit\":\"bpm\",\"start\":\"2026-10-16T19:04:05.120Z\"}}\n"
        );
        let steps = ohs_driver::Sample {
            end: Some(start + Duration::from_secs(60)),
            ..ohs_driver::Sample::at("step_count_intraday", 84.0, "count", start)
        };
        let l = encode_sample(&at(), &ch, &steps, "abcdefgh", &Device::default()).unwrap();
        assert!(l.contains("\"end\":\"2026-10-16T19:05:05.120Z\""), "{l}");
        let nan = ohs_driver::Sample::at("heart_rate", f64::NAN, "bpm", start);
        assert!(encode_sample(&at(), &ch, &nan, "abcdefgh", &dev).is_none());
        let before = ohs_driver::Sample::at(
            "heart_rate",
            60.0,
            "bpm",
            UNIX_EPOCH - Duration::from_secs(1),
        );
        assert!(encode_sample(&at(), &ch, &before, "abcdefgh", &dev).is_none());
    }

    #[test]
    fn device_text_is_cleaned() {
        assert_eq!(text(b"OMRON HEALTHCARE\0\0\0"), "OMRON HEALTHCARE");
        assert_eq!(text(b"  H10\r\n"), "H10");
        assert_eq!(text(&[0xff, b'A']), "\u{fffd}A");
        assert_eq!(text("é".repeat(200).as_bytes()).chars().count(), 128);
    }

    #[test]
    fn serial_like_last_words_are_dropped() {
        assert_eq!(public_name("Polar H10 0A1B2C3D"), "Polar H10");
        assert_eq!(public_name("BLEsmart_00000397"), "BLEsmart");
        assert_eq!(public_name("TICKR 1A2B"), "TICKR");
        assert_eq!(public_name("Forerunner 965"), "Forerunner 965");
        assert_eq!(public_name("Edge 1040"), "Edge 1040");
        assert_eq!(public_name("Thermo 123456"), "Thermo");
        assert_eq!(public_name("HRM-Pro"), "HRM-Pro");
        assert_eq!(public_name("Accu-Chek Guide"), "Accu-Chek Guide");
        assert_eq!(public_name(" 0A1B2C3D"), "0A1B2C3D");
    }

    #[test]
    fn day_of_a_line_is_utc() {
        let l = encode(
            &at(),
            0x2a37,
            &[0x00, 0x48],
            "abcdefgh",
            &Device::default(),
            None,
        )
        .unwrap();
        assert_eq!(utc_day(&l).as_deref(), Some("2026-10-16"));
        let late = FixedOffset::west_opt(5 * 3600)
            .unwrap()
            .with_ymd_and_hms(2026, 10, 16, 22, 0, 0)
            .unwrap();
        let l = encode(
            &late,
            0x2a37,
            &[0x00, 0x48],
            "abcdefgh",
            &Device::default(),
            None,
        )
        .unwrap();
        assert_eq!(utc_day(&l).as_deref(), Some("2026-10-17"));
        assert_eq!(utc_day("not json"), None);
    }

    #[test]
    fn sessions_fit_the_contract() {
        let s = new_session();
        assert!((8..=64).contains(&s.len()));
        assert!(s.bytes().all(|c| c.is_ascii_alphanumeric()));
    }
}
