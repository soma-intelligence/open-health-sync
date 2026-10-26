// SPDX-License-Identifier: AGPL-3.0-only

//! The interface between `ohs-bridge` and a vendor Bluetooth protocol: devices
//! that send their richest data on a service of their maker's rather than on
//! a Bluetooth SIG profile (a Polar strap's raw ECG, a band's stored night).
//!
//! A driver is a state machine without I/O: it says what to subscribe to,
//! read and write, and is fed what the device answers and the time it
//! arrived. `ohs-bridge` runs it over btleplug on a computer; a phone's
//! Bluetooth stack can run the same driver, since nothing in it touches a
//! radio, a clock or a file.
//!
//! A driver hands values up in one of two forms:
//!
//! - raw (`Step::upload`): the bytes the device sent, with context bytes, for
//!   a protocol the server decodes (Polar Measurement Data);
//! - decoded (`Step::samples`): canonical samples (a heart rate in bpm, an RR
//!   interval in ms, a skin temperature in celsius) for a protocol the server does
//!   not know. The server checks the type and the unit and stores them as
//!   they are.
//!
//! A [`Protocol`] recognizes the devices of one family and builds a driver
//! for each connection. `ohs-bridge` has its own; another crate registers
//! more through `ohs_bridge::main_with`.

use std::time::SystemTime;

use uuid::Uuid;

/// What a driver asks the radio to do, in order.
#[derive(Clone, Debug, PartialEq)]
pub enum Action {
    /// Turn on notifications or indications.
    Subscribe(Uuid),
    /// Read the value; it comes back through [`Driver::input`] with `read`
    /// set.
    Read(Uuid),
    /// Write the bytes, with response.
    Write(Uuid, Vec<u8>),
    /// Write the bytes without response (a write command).
    WriteCommand(Uuid, Vec<u8>),
}

impl Action {
    /// The characteristic the action is on.
    pub fn char(&self) -> Uuid {
        match self {
            Action::Subscribe(u)
            | Action::Read(u)
            | Action::Write(u, _)
            | Action::WriteCommand(u, _) => *u,
        }
    }
}

/// What the live view shows for a value.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Live {
    pub text: String,
    /// A heart rate the value carries, for the status line.
    pub heart_rate: Option<u16>,
}

/// The type of an RR interval sample: the time between two beats, in ms.
/// Stored as a beat series, not as a quantity.
pub const RR_INTERVAL: &str = "rr_interval";

/// One decoded value, in the server's canonical vocabulary.
#[derive(Clone, Debug, PartialEq)]
pub struct Sample {
    /// A canonical quantity type (`heart_rate`, `oxygen_saturation`,
    /// `skin_temperature`, `step_count`...), or [`RR_INTERVAL`].
    pub kind: &'static str,
    pub value: f64,
    /// The unit of `value`, best the server's storage unit for the type:
    /// `bpm`, `ms`, `percent`, `celsius`, `count`, `kcal`.
    pub unit: &'static str,
    /// When it was measured, on the device's clock once that clock is known
    /// to be right, otherwise the time it arrived. For an RR interval, the
    /// beat that ends it.
    pub start: SystemTime,
    /// The end of a value that covers a span (steps over a minute).
    pub end: Option<SystemTime>,
}

impl Sample {
    pub fn at(kind: &'static str, value: f64, unit: &'static str, start: SystemTime) -> Sample {
        Sample {
            kind,
            value,
            unit,
            start,
            end: None,
        }
    }
}

/// What a driver answers to an input.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Step {
    pub actions: Vec<Action>,
    /// Upload the value as received, with these context bytes (empty for
    /// none).
    pub upload: Option<Vec<u8>>,
    /// Decoded values to upload.
    pub samples: Vec<Sample>,
    pub live: Option<Live>,
    /// Something the person should know: a stream the device refused, a key
    /// it did not accept. Never a health value.
    pub note: Option<String>,
}

impl Step {
    pub fn act(actions: Vec<Action>) -> Step {
        Step {
            actions,
            ..Default::default()
        }
    }

    pub fn note(mut self, text: impl Into<String>) -> Step {
        self.note = Some(text.into());
        self
    }
}

/// A vendor protocol for one connection.
pub trait Driver: Send {
    /// The first actions, once the services are discovered.
    fn start(&mut self, now: SystemTime) -> Vec<Action>;
    /// Whether the values of this characteristic are the driver's.
    fn owns(&self, char: &Uuid) -> bool;
    /// A value of one of its characteristics, read (`read`) or notified, and
    /// when it arrived.
    fn input(&mut self, char: &Uuid, value: &[u8], read: bool, now: SystemTime) -> Step;
    /// Whether an answer is awaited: the radio calls `timeout` when it does
    /// not come within a few seconds.
    fn waiting(&self) -> bool;
    fn timeout(&mut self, now: SystemTime) -> Step;
}

/// A device as the radio sees it.
#[derive(Clone, Copy, Debug)]
pub struct Peer<'a> {
    /// The advertised name, when there is one.
    pub name: Option<&'a str>,
    /// The advertised services.
    pub services: &'a [Uuid],
}

/// Why a connection is made.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Context {
    /// Values are queued and uploaded (`run`). When false (`watch`), a driver
    /// shows live values only and never takes a step that loses data on the
    /// device, such as acknowledging stored records.
    pub upload: bool,
    /// The local offset from UTC, in seconds, for a device whose clock shows
    /// wall time (a watch asking for the time). `None` when unknown: a driver
    /// then leaves the device's clock alone rather than set it to UTC.
    pub utc_offset: Option<i32>,
}

/// A family of devices that speak one protocol.
pub trait Protocol: Send + Sync {
    /// A short name for logs and `scan` (`polar`, `whoop`).
    fn name(&self) -> &'static str;
    /// Whether an advertising device is one of this family: `run --any`
    /// connects to it, `scan` lists it.
    fn recognizes(&self, peer: Peer) -> bool;
    /// The driver for a connected device with these characteristics, when it
    /// is one of this family.
    fn driver(&self, peer: Peer, chars: &[Uuid], ctx: Context) -> Option<Box<dyn Driver>>;
}

/// A 128-bit UUID from its 32 hex digits, for constants.
pub const fn uuid(n: u128) -> Uuid {
    Uuid::from_u128(n)
}

/// The Bluetooth SIG UUID of a 16-bit number.
pub const fn uuid16(n: u16) -> Uuid {
    Uuid::from_u128(((n as u128) << 96) | 0x0000_0000_0000_1000_8000_0080_5f9b_34fb)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sig_uuids() {
        assert_eq!(
            uuid16(0x2a37).to_string(),
            "00002a37-0000-1000-8000-00805f9b34fb"
        );
        assert_eq!(
            Action::WriteCommand(uuid16(0x2a39), vec![1]).char(),
            uuid16(0x2a39)
        );
    }
}
