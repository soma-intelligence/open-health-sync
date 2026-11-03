// SPDX-License-Identifier: AGPL-3.0-only

//! ohs-bridge: sends what standard Bluetooth health devices (heart rate
//! straps, blood pressure cuffs, scales, glucose meters, thermometers, pulse
//! oximeters) and USB-mounted watches record to an Open Health Sync server.
//! Values go up as the bytes the device sent; the server decodes them.
//! See docs/bridge.md.
//!
//! As a library it runs the same commands with more vendor protocols: a
//! crate implements [`driver::Protocol`] for a family of devices and calls
//! [`main_with`] from its own binary.

mod ble;
mod cli;
mod codec;
mod config;
mod daemon;
mod files;
mod gatt;
mod line;
mod live;
mod queue;
mod spool;
mod status;
mod upload;
mod vendor;

use std::process::ExitCode;

use cli::Command;

/// The driver interface, as this bridge runs it.
pub use ohs_driver as driver;

/// Runs the command line, with these vendor protocols besides the bridge's
/// own. `program` names the binary in messages.
pub fn main_with(program: &str, protocols: Vec<Box<dyn driver::Protocol>>) -> ExitCode {
    vendor::register(protocols);
    let args: Vec<String> = std::env::args_os()
        .skip(1)
        .map(|a| a.to_string_lossy().into_owned())
        .collect();
    let cmd = match cli::parse(&args) {
        Ok(c) => c,
        Err(e) => {
            eprintln!("{program}: {e}");
            return ExitCode::from(2);
        }
    };
    let res = match cmd {
        Command::Help => {
            print!("{}", cli::USAGE.replace("ohs-bridge", program));
            Ok(())
        }
        Command::Version => {
            println!("{program} {}", env!("CARGO_PKG_VERSION"));
            Ok(())
        }
        Command::Scan { seconds, all } => daemon::scan(seconds, all),
        Command::Watch { devices, vendor } => daemon::watch(devices, vendor),
        Command::Run(r) => daemon::run(r),
        Command::Files {
            dirs,
            watch,
            allow_http,
        } => daemon::files(dirs, watch, allow_http),
        Command::Flush { allow_http } => daemon::flush(allow_http),
        Command::Status => daemon::status(),
    };
    match res {
        Ok(()) => ExitCode::SUCCESS,
        Err(e) => {
            eprintln!("{program}: {e}");
            ExitCode::FAILURE
        }
    }
}
