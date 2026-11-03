// SPDX-License-Identifier: AGPL-3.0-only

//! The `ohs-bridge` binary: the library's commands with its own protocols.

use std::process::ExitCode;

fn main() -> ExitCode {
    ohs_bridge::main_with("ohs-bridge", Vec::new())
}
