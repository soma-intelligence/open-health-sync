// SPDX-License-Identifier: AGPL-3.0-only

//! Command line arguments, parsed by hand.

use std::path::PathBuf;

pub const USAGE: &str = "\
ohs-bridge: sends what standard Bluetooth health devices and USB-mounted watches
record to an Open Health Sync server.

Usage:
  ohs-bridge scan [--seconds N] [--all]
      List nearby devices that advertise a supported health service.
  ohs-bridge watch [--device NAME]...
      Connect and print heart rate, RR intervals and battery as they arrive.
      Uploads nothing.
  ohs-bridge run [--device NAME]... [--any] [--device-user N] [--files DIR]...
                 [--status-file PATH] [--allow-http]
      Connect (and reconnect) to the named devices, or to any supported
      device with --any, queue every value and upload it. With --files, also
      upload new .fit files from those folders (a watch mounted over USB).
  ohs-bridge files DIR... [--watch] [--allow-http]
      Upload new .fit files from folders, once or (--watch) as they appear.
  ohs-bridge flush [--allow-http]
      Upload the queued parts now.
  ohs-bridge status
      Queue, last upload, devices and their last values.

--device NAME matches the advertised name, ignoring case, anywhere in it.
--device-user N is your user index (0 to 254) on a shared scale or cuff.

Environment:
  OHS_URL            base URL of the server, for example https://ohs.example.org
  OHS_TOKEN          a user token (ohs_ut_) with the upload scope
  OHS_BRIDGE_STATE   state directory (queue, status), default per platform
";

#[derive(Debug, PartialEq)]
pub enum Command {
    Scan {
        seconds: u64,
        all: bool,
    },
    Watch {
        devices: Vec<String>,
    },
    Run(Run),
    Files {
        dirs: Vec<PathBuf>,
        watch: bool,
        allow_http: bool,
    },
    Flush {
        allow_http: bool,
    },
    Status,
    Help,
    Version,
}

#[derive(Debug, Default, PartialEq)]
pub struct Run {
    pub devices: Vec<String>,
    pub any: bool,
    pub device_user: Option<u8>,
    pub files: Vec<PathBuf>,
    pub status_file: Option<PathBuf>,
    pub allow_http: bool,
}

struct Args<'a> {
    rest: std::slice::Iter<'a, String>,
    command: &'static str,
}

impl<'a> Args<'a> {
    fn value(&mut self, flag: &str) -> Result<&'a String, String> {
        match self.rest.next() {
            Some(v) if !v.starts_with("--") => Ok(v),
            _ => Err(format!("{flag} needs a value")),
        }
    }

    fn unknown(&self, arg: &str) -> String {
        format!(
            "{}: unknown argument {arg} (see ohs-bridge --help)",
            self.command
        )
    }
}

pub fn parse(args: &[String]) -> Result<Command, String> {
    let Some((cmd, rest)) = args.split_first() else {
        return Ok(Command::Help);
    };
    let name = match cmd.as_str() {
        "scan" => "scan",
        "watch" => "watch",
        "run" => "run",
        "files" => "files",
        "flush" => "flush",
        "status" => "status",
        "help" | "-h" | "--help" => return Ok(Command::Help),
        "version" | "-V" | "--version" => return Ok(Command::Version),
        other => return Err(format!("unknown command {other} (see ohs-bridge --help)")),
    };
    let mut a = Args {
        rest: rest.iter(),
        command: name,
    };
    match name {
        "scan" => {
            let (mut seconds, mut all) = (10, false);
            while let Some(arg) = a.rest.next() {
                match arg.as_str() {
                    "--seconds" => {
                        seconds = a
                            .value("--seconds")?
                            .parse()
                            .ok()
                            .filter(|s| (1..=600).contains(s))
                            .ok_or("--seconds takes 1 to 600")?
                    }
                    "--all" => all = true,
                    other => return Err(a.unknown(other)),
                }
            }
            Ok(Command::Scan { seconds, all })
        }
        "watch" => {
            let mut devices = Vec::new();
            while let Some(arg) = a.rest.next() {
                match arg.as_str() {
                    "--device" => devices.push(a.value("--device")?.clone()),
                    other => return Err(a.unknown(other)),
                }
            }
            Ok(Command::Watch { devices })
        }
        "run" => {
            let mut r = Run::default();
            while let Some(arg) = a.rest.next() {
                match arg.as_str() {
                    "--device" => r.devices.push(a.value("--device")?.clone()),
                    "--any" => r.any = true,
                    "--device-user" => {
                        r.device_user = Some(
                            a.value("--device-user")?
                                .parse::<u8>()
                                .ok()
                                .filter(|u| *u < 255)
                                .ok_or("--device-user takes 0 to 254")?,
                        )
                    }
                    "--files" => r.files.push(PathBuf::from(a.value("--files")?)),
                    "--status-file" => {
                        r.status_file = Some(PathBuf::from(a.value("--status-file")?))
                    }
                    "--allow-http" => r.allow_http = true,
                    other => return Err(a.unknown(other)),
                }
            }
            if r.devices.is_empty() && !r.any && r.files.is_empty() {
                return Err("run: name a device (--device NAME), pass --any, or watch a folder (--files DIR)".into());
            }
            Ok(Command::Run(r))
        }
        "files" => {
            let (mut dirs, mut watch, mut allow_http) = (Vec::new(), false, false);
            for arg in a.rest.by_ref() {
                match arg.as_str() {
                    "--watch" => watch = true,
                    "--allow-http" => allow_http = true,
                    s if s.starts_with("--") => return Err(a.unknown(s)),
                    s => dirs.push(PathBuf::from(s)),
                }
            }
            if dirs.is_empty() {
                return Err("files: name at least one folder".into());
            }
            Ok(Command::Files {
                dirs,
                watch,
                allow_http,
            })
        }
        "flush" => {
            let mut allow_http = false;
            for arg in a.rest.by_ref() {
                match arg.as_str() {
                    "--allow-http" => allow_http = true,
                    other => return Err(a.unknown(other)),
                }
            }
            Ok(Command::Flush { allow_http })
        }
        _ => match a.rest.next() {
            Some(other) => Err(a.unknown(other)),
            None => Ok(Command::Status),
        },
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn p(s: &str) -> Result<Command, String> {
        parse(&s.split_whitespace().map(String::from).collect::<Vec<_>>())
    }

    #[test]
    fn commands() {
        assert_eq!(p(""), Ok(Command::Help));
        assert_eq!(p("--help"), Ok(Command::Help));
        assert_eq!(p("--version"), Ok(Command::Version));
        assert_eq!(
            p("scan"),
            Ok(Command::Scan {
                seconds: 10,
                all: false
            })
        );
        assert_eq!(
            p("scan --seconds 30 --all"),
            Ok(Command::Scan {
                seconds: 30,
                all: true
            })
        );
        assert_eq!(p("watch"), Ok(Command::Watch { devices: vec![] }));
        assert_eq!(
            p("watch --device h10"),
            Ok(Command::Watch {
                devices: vec!["h10".into()]
            })
        );
        assert_eq!(
            p("run --device H10 --device BLEsmart --device-user 1 --files /Volumes/GARMIN --status-file /tmp/ohs.txt"),
            Ok(Command::Run(Run {
                devices: vec!["H10".into(), "BLEsmart".into()],
                device_user: Some(1),
                files: vec![PathBuf::from("/Volumes/GARMIN")],
                status_file: Some(PathBuf::from("/tmp/ohs.txt")),
                ..Default::default()
            }))
        );
        assert_eq!(
            p("run --any --allow-http"),
            Ok(Command::Run(Run {
                any: true,
                allow_http: true,
                ..Default::default()
            }))
        );
        assert_eq!(
            p("files /a /b --watch"),
            Ok(Command::Files {
                dirs: vec![PathBuf::from("/a"), PathBuf::from("/b")],
                watch: true,
                allow_http: false
            })
        );
        assert_eq!(p("flush"), Ok(Command::Flush { allow_http: false }));
        assert_eq!(p("status"), Ok(Command::Status));
    }

    #[test]
    fn mistakes() {
        for bad in [
            "sync",
            "scan --seconds 0",
            "scan --seconds",
            "scan --nope",
            "watch --device",
            "watch --device --any",
            "run",
            "run --device-user 255",
            "run --device-user x --any",
            "files",
            "files --watch",
            "flush now",
            "status --json",
        ] {
            assert!(p(bad).is_err(), "{bad}");
        }
    }
}
