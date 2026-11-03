// SPDX-License-Identifier: AGPL-3.0-only

//! Vendor protocols: devices that send their richest data on a service of
//! their maker's rather than on a Bluetooth SIG profile (a Polar strap's raw
//! ECG, for one). Each driver is a state machine without I/O, defined by the
//! `ohs-driver` crate: it says what to subscribe to, read and write, and is
//! fed what the device answers. `ble` runs it over btleplug.
//!
//! The bridge has one protocol of its own, Polar Measurement Data, whose
//! values go up as the bytes the device sent with the context the server
//! needs to decode them. A binary built on this crate adds more through
//! `main_with`; theirs decode on the client and send canonical samples.

pub mod polar;

use std::sync::OnceLock;

use uuid::Uuid;

pub use ohs_driver::{Action, Context, Driver, Live, Peer, Protocol, Step};

/// Which of the bridge's own protocols to run, and what to ask of them.
#[derive(Clone, Debug, PartialEq)]
pub struct Config {
    /// Polar measurement types to stream; empty turns the Polar driver off.
    pub polar: Vec<polar::Kind>,
}

impl Default for Config {
    fn default() -> Config {
        Config {
            polar: polar::DEFAULT_STREAMS.to_vec(),
        }
    }
}

static REGISTERED: OnceLock<Vec<Box<dyn Protocol>>> = OnceLock::new();

/// Adds protocols for the life of the process. The first call wins.
pub fn register(protocols: Vec<Box<dyn Protocol>>) {
    let _ = REGISTERED.set(protocols);
}

fn registered() -> &'static [Box<dyn Protocol>] {
    REGISTERED.get().map(Vec::as_slice).unwrap_or(&[])
}

/// The protocols that recognize an advertising device, by name.
pub fn recognized(peer: Peer) -> Vec<&'static str> {
    registered()
        .iter()
        .filter(|p| p.recognizes(peer))
        .map(|p| p.name())
        .collect()
}

/// The driver for a connected device, if a protocol applies: Polar first,
/// then the registered ones in their order.
pub fn detect(peer: Peer, chars: &[Uuid], cfg: &Config, ctx: Context) -> Option<Box<dyn Driver>> {
    if !cfg.polar.is_empty() && polar::present(chars) {
        return Some(Box::new(polar::Polar::new(&cfg.polar)));
    }
    registered().iter().find_map(|p| p.driver(peer, chars, ctx))
}

#[cfg(test)]
mod tests {
    use std::time::SystemTime;

    use super::*;
    use crate::ble::Target;

    struct Band;

    struct BandDriver;

    const BAND: Uuid = ohs_driver::uuid(0x0000_aaaa_0000_1000_8000_0080_5f9b_34fb);

    impl Driver for BandDriver {
        fn start(&mut self, _now: SystemTime) -> Vec<Action> {
            vec![Action::Subscribe(BAND)]
        }
        fn owns(&self, char: &Uuid) -> bool {
            *char == BAND
        }
        fn input(&mut self, _: &Uuid, _: &[u8], _: bool, _: SystemTime) -> Step {
            Step::default()
        }
        fn waiting(&self) -> bool {
            false
        }
        fn timeout(&mut self, _: SystemTime) -> Step {
            Step::default()
        }
    }

    impl Protocol for Band {
        fn name(&self) -> &'static str {
            "band"
        }
        fn recognizes(&self, peer: Peer) -> bool {
            peer.name.is_some_and(|n| n.starts_with("Band "))
        }
        fn driver(&self, peer: Peer, chars: &[Uuid], ctx: Context) -> Option<Box<dyn Driver>> {
            (self.recognizes(peer) && chars.contains(&BAND) && ctx.upload)
                .then(|| Box::new(BandDriver) as Box<dyn Driver>)
        }
    }

    // The only test that registers: the registry lives as long as the process.
    #[test]
    fn registered_protocols() {
        register(vec![Box::new(Band)]);
        let band = Peer {
            name: Some("Band 1A2B"),
            services: &[],
        };
        let other = Peer {
            name: Some("Speaker"),
            services: &[],
        };
        assert_eq!(recognized(band), vec!["band"]);
        assert!(recognized(other).is_empty());
        let up = Context {
            upload: true,
            ..Default::default()
        };
        let off = Config { polar: vec![] };
        assert!(detect(band, &[BAND], &off, up).is_some());
        assert!(detect(band, &[BAND], &off, Context::default()).is_none());
        assert!(detect(other, &[BAND], &off, up).is_none());
        // Polar comes first when a device serves both.
        let both = [polar::CONTROL, polar::DATA, BAND];
        let d = detect(band, &both, &Config::default(), up).unwrap();
        assert!(d.owns(&polar::CONTROL));
        // `--any` connects to a device a protocol recognizes.
        let any = Target::new(&[], true);
        assert!(any.matches(Some("Band 1A2B"), &[]));
        assert!(!any.matches(Some("Speaker"), &[]));
    }
}
