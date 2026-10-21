// SPDX-License-Identifier: AGPL-3.0-only

//! The upload line: one characteristic value as the device sent it, with the
//! time it arrived. The format is the upload contract of
//! `POST /v1/ingest/bluetooth` (docs/connectors/bluetooth.md).

use chrono::{DateTime, FixedOffset, SecondsFormat};
use serde::{Deserialize, Serialize};

use crate::codec;

/// The longest value a line may carry, the ATT maximum attribute length.
pub const MAX_VALUE_BYTES: usize = 512;

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
    if value.is_empty() || value.len() > MAX_VALUE_BYTES {
        return None;
    }
    let l = Line {
        at: at.to_rfc3339_opts(SecondsFormat::Millis, false),
        char: format!("{char:04x}"),
        value: codec::hex(value),
        session,
        device: (!device.is_empty()).then_some(device),
        device_user,
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
