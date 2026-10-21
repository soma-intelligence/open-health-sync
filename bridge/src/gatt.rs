// SPDX-License-Identifier: AGPL-3.0-only

//! The Bluetooth SIG numbers the bridge uses, and the few bytes it writes to a
//! device. Values the device sends are not decoded here: the server does it
//! (connectors/bluetooth), except for the live view (`live`).

use chrono::{DateTime, Datelike, TimeZone, Timelike};
use uuid::Uuid;

/// The full UUID of a 16-bit Bluetooth SIG number (the Bluetooth base UUID).
#[cfg(test)]
pub const fn uuid16(short: u16) -> Uuid {
    Uuid::from_u128(((short as u128) << 96) | 0x0000_0000_0000_1000_8000_0080_5f9b_34fb)
}

/// The 16-bit number of a Bluetooth base UUID, None for any other UUID.
pub fn short(uuid: &Uuid) -> Option<u16> {
    let v = uuid.as_u128();
    if v & 0xffff_0000_ffff_ffff_ffff_ffff_ffff_ffff == 0x0000_0000_0000_1000_8000_0080_5f9b_34fb {
        Some((v >> 96) as u16)
    } else {
        None
    }
}

// Services.
pub const HEART_RATE: u16 = 0x180d;
pub const BLOOD_PRESSURE: u16 = 0x1810;
pub const WEIGHT_SCALE: u16 = 0x181d;
pub const BODY_COMPOSITION: u16 = 0x181b;
pub const GLUCOSE: u16 = 0x1808;
pub const HEALTH_THERMOMETER: u16 = 0x1809;
pub const PULSE_OXIMETER: u16 = 0x1822;

/// The health services the bridge looks for.
pub const SERVICES: [u16; 7] = [
    HEART_RATE,
    BLOOD_PRESSURE,
    WEIGHT_SCALE,
    BODY_COMPOSITION,
    GLUCOSE,
    HEALTH_THERMOMETER,
    PULSE_OXIMETER,
];

/// A service's name, for `scan`.
pub fn service_name(service: u16) -> Option<&'static str> {
    Some(match service {
        HEART_RATE => "heart rate",
        BLOOD_PRESSURE => "blood pressure",
        WEIGHT_SCALE => "weight scale",
        BODY_COMPOSITION => "body composition",
        GLUCOSE => "glucose",
        HEALTH_THERMOMETER => "thermometer",
        PULSE_OXIMETER => "pulse oximeter",
        _ => return None,
    })
}

// Characteristics.
pub const HEART_RATE_MEASUREMENT: u16 = 0x2a37;
pub const BLOOD_PRESSURE_MEASUREMENT: u16 = 0x2a35;
pub const WEIGHT_MEASUREMENT: u16 = 0x2a9d;
pub const BODY_COMPOSITION_MEASUREMENT: u16 = 0x2a9c;
pub const GLUCOSE_MEASUREMENT: u16 = 0x2a18;
pub const TEMPERATURE_MEASUREMENT: u16 = 0x2a1c;
pub const PLX_SPOT_CHECK: u16 = 0x2a5e;
pub const PLX_CONTINUOUS: u16 = 0x2a5f;
pub const RECORD_ACCESS_CONTROL_POINT: u16 = 0x2a52;
pub const BATTERY_LEVEL: u16 = 0x2a19;
pub const CURRENT_TIME: u16 = 0x2a2b;
pub const MANUFACTURER_NAME: u16 = 0x2a29;
pub const MODEL_NUMBER: u16 = 0x2a24;
pub const FIRMWARE_REVISION: u16 = 0x2a26;

/// A characteristic's name, for `watch` and messages.
pub fn char_name(char: u16) -> &'static str {
    match char {
        HEART_RATE_MEASUREMENT => "heart rate",
        BLOOD_PRESSURE_MEASUREMENT => "blood pressure",
        WEIGHT_MEASUREMENT => "weight",
        BODY_COMPOSITION_MEASUREMENT => "body composition",
        GLUCOSE_MEASUREMENT => "glucose",
        TEMPERATURE_MEASUREMENT => "temperature",
        PLX_SPOT_CHECK => "SpO2 spot check",
        PLX_CONTINUOUS => "SpO2",
        RECORD_ACCESS_CONTROL_POINT => "record access",
        BATTERY_LEVEL => "battery",
        _ => "value",
    }
}

/// The characteristics whose values are uploaded: the server decodes these.
pub const MEASUREMENTS: [u16; 8] = [
    HEART_RATE_MEASUREMENT,
    BLOOD_PRESSURE_MEASUREMENT,
    WEIGHT_MEASUREMENT,
    BODY_COMPOSITION_MEASUREMENT,
    GLUCOSE_MEASUREMENT,
    TEMPERATURE_MEASUREMENT,
    PLX_SPOT_CHECK,
    PLX_CONTINUOUS,
];

/// Record Access Control Point request: report the stored records, all of them
/// (`after` None) or those whose sequence number is at least `after` + 1.
pub fn racp_report(after: Option<u16>) -> Vec<u8> {
    const REPORT_STORED_RECORDS: u8 = 0x01;
    const ALL_RECORDS: u8 = 0x01;
    const GREATER_OR_EQUAL: u8 = 0x03;
    const FILTER_SEQUENCE_NUMBER: u8 = 0x01;
    match after.and_then(|n| n.checked_add(1)) {
        None => vec![REPORT_STORED_RECORDS, ALL_RECORDS],
        Some(from) => {
            let b = from.to_le_bytes();
            vec![
                REPORT_STORED_RECORDS,
                GREATER_OR_EQUAL,
                FILTER_SEQUENCE_NUMBER,
                b[0],
                b[1],
            ]
        }
    }
}

/// The sequence number of a Glucose Measurement value (after its flags).
pub fn glucose_sequence(value: &[u8]) -> Option<u16> {
    (value.len() >= 3).then(|| u16::from_le_bytes([value[1], value[2]]))
}

/// The Current Time value (Exact Time 256 and an adjust reason) for `now`, in
/// the time zone it carries: a device clock has no zone, so it is set to the
/// local wall time, as the device's own app does.
pub fn current_time<Tz: TimeZone>(now: &DateTime<Tz>) -> [u8; 10] {
    const MANUAL_TIME_UPDATE: u8 = 0x01;
    let y = (now.year() as u16).to_le_bytes();
    [
        y[0],
        y[1],
        now.month() as u8,
        now.day() as u8,
        now.hour() as u8,
        now.minute() as u8,
        now.second() as u8,
        now.weekday().number_from_monday() as u8,
        (now.nanosecond().min(999_999_999) as u64 * 256 / 1_000_000_000) as u8,
        MANUAL_TIME_UPDATE,
    ]
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::FixedOffset;

    #[test]
    fn uuid16_is_the_bluetooth_base_uuid() {
        assert_eq!(
            uuid16(0x2a37).to_string(),
            "00002a37-0000-1000-8000-00805f9b34fb"
        );
        assert_eq!(short(&uuid16(0x180d)), Some(0x180d));
        let vendor: Uuid = "6a4e2401-667b-11e3-949a-0800200c9a66".parse().unwrap();
        assert_eq!(short(&vendor), None);
    }

    #[test]
    fn racp_requests() {
        assert_eq!(racp_report(None), vec![0x01, 0x01]);
        assert_eq!(racp_report(Some(41)), vec![0x01, 0x03, 0x01, 42, 0]);
        assert_eq!(
            racp_report(Some(0x01ff)),
            vec![0x01, 0x03, 0x01, 0x00, 0x02]
        );
        assert_eq!(
            racp_report(Some(u16::MAX)),
            vec![0x01, 0x01],
            "wrapped: ask for all"
        );
    }

    #[test]
    fn glucose_sequence_number() {
        assert_eq!(glucose_sequence(&[0x02, 0x2a, 0x00, 0xea, 0x07]), Some(42));
        assert_eq!(glucose_sequence(&[0x02, 0x2a]), None);
    }

    #[test]
    fn current_time_bytes() {
        let tz = FixedOffset::east_opt(2 * 3600).unwrap();
        let now = tz.with_ymd_and_hms(2026, 10, 16, 21, 4, 5).unwrap()
            + chrono::Duration::milliseconds(500);
        assert_eq!(
            current_time(&now),
            [0xea, 0x07, 10, 16, 21, 4, 5, 5, 128, 0x01]
        );
    }
}
