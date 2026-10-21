// SPDX-License-Identifier: AGPL-3.0-only

//! The values `watch` and the status line show while a device is connected:
//! heart rate, RR intervals and battery level. Uploads carry the raw bytes;
//! only the server decodes them for storage.

/// A Heart Rate Measurement, as far as the live view needs it.
#[derive(Debug, PartialEq)]
pub struct HeartRate {
    pub bpm: u16,
    /// None when the sensor does not report skin contact.
    pub contact: Option<bool>,
    /// Beat-to-beat intervals, in milliseconds.
    pub rr_ms: Vec<f64>,
}

/// Decodes a Heart Rate Measurement (0x2A37) value. None when it is shorter
/// than its flags say.
pub fn heart_rate(v: &[u8]) -> Option<HeartRate> {
    let flags = *v.first()?;
    let mut i = 1;
    let bpm = if flags & 0x01 != 0 {
        let b = u16::from_le_bytes([*v.get(1)?, *v.get(2)?]);
        i += 2;
        b
    } else {
        i += 1;
        *v.get(1)? as u16
    };
    let contact = (flags & 0x04 != 0).then_some(flags & 0x02 != 0);
    if flags & 0x08 != 0 {
        i += 2; // energy expended
        if v.len() < i {
            return None;
        }
    }
    let mut rr_ms = Vec::new();
    if flags & 0x10 != 0 {
        for pair in v.get(i..)?.as_chunks::<2>().0 {
            rr_ms.push(u16::from_le_bytes(*pair) as f64 * 1000.0 / 1024.0);
        }
    }
    Some(HeartRate {
        bpm,
        contact,
        rr_ms,
    })
}

/// Decodes a Battery Level (0x2A19) value, in percent.
pub fn battery(v: &[u8]) -> Option<u8> {
    v.first().copied().filter(|p| *p <= 100)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn heart_rate_8_bit_with_rr() {
        let hr = heart_rate(&[0x10, 72, 0x00, 0x04, 0x33, 0x03]).unwrap();
        assert_eq!(hr.bpm, 72);
        assert_eq!(hr.contact, None);
        assert_eq!(hr.rr_ms.len(), 2);
        assert!((hr.rr_ms[0] - 1000.0).abs() < 1e-9);
        assert!((hr.rr_ms[1] - 799.8046875).abs() < 1e-9);
    }

    #[test]
    fn heart_rate_16_bit_with_energy_and_contact() {
        let hr = heart_rate(&[0x0f, 0x2c, 0x01, 0x10, 0x00]).unwrap();
        assert_eq!(hr.bpm, 300);
        assert_eq!(hr.contact, Some(true));
        assert!(hr.rr_ms.is_empty());
        assert_eq!(heart_rate(&[0x04, 60]).unwrap().contact, Some(false));
    }

    #[test]
    fn heart_rate_truncated() {
        assert_eq!(heart_rate(&[]), None);
        assert_eq!(heart_rate(&[0x00]), None);
        assert_eq!(heart_rate(&[0x01, 0x48]), None);
        assert_eq!(heart_rate(&[0x08, 0x48, 0x00]), None);
        // An odd trailing byte in the RR list is ignored.
        assert_eq!(
            heart_rate(&[0x10, 0x48, 0x00, 0x04, 0x01])
                .unwrap()
                .rr_ms
                .len(),
            1
        );
    }

    #[test]
    fn battery_level() {
        assert_eq!(battery(&[87]), Some(87));
        assert_eq!(battery(&[101]), None);
        assert_eq!(battery(&[]), None);
    }
}
