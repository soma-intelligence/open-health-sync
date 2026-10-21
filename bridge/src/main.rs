// SPDX-License-Identifier: AGPL-3.0-only

//! ohs-bridge: sends what standard Bluetooth health devices (heart rate
//! straps, blood pressure cuffs, scales, glucose meters, thermometers, pulse
//! oximeters) and USB-mounted watches record to an Open Health Sync server.
//! Values go up as the bytes the device sent; the server decodes them.
//! See docs/bridge.md.

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

use std::process::ExitCode;

use cli::Command;

fn main() -> ExitCode {
    let args: Vec<String> = std::env::args_os()
        .skip(1)
        .map(|a| a.to_string_lossy().into_owned())
        .collect();
    let cmd = match cli::parse(&args) {
        Ok(c) => c,
        Err(e) => {
            eprintln!("ohs-bridge: {e}");
            return ExitCode::from(2);
        }
    };
    let res = match cmd {
        Command::Help => {
            print!("{}", cli::USAGE);
            Ok(())
        }
        Command::Version => {
            println!("ohs-bridge {}", env!("CARGO_PKG_VERSION"));
            Ok(())
        }
        Command::Scan { seconds, all } => daemon::scan(seconds, all),
        Command::Watch { devices } => daemon::watch(devices),
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
            eprintln!("ohs-bridge: {e}");
            ExitCode::FAILURE
        }
    }
}
