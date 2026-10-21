// SPDX-License-Identifier: AGPL-3.0-only

//! On macOS, puts macos/Info.plist in the binary's __TEXT,__info_plist
//! section, as Xcode does for a command line tool. CoreBluetooth needs a
//! Bluetooth usage description, and a bare binary has no bundle to hold one:
//! with the section, macOS asks for ohs-bridge itself when it runs under
//! launchd rather than from a terminal.

fn main() {
    println!("cargo:rerun-if-changed=macos/Info.plist");
    if std::env::var("CARGO_CFG_TARGET_OS").as_deref() == Ok("macos") {
        let dir = std::env::var("CARGO_MANIFEST_DIR").expect("set by cargo");
        println!(
            "cargo:rustc-link-arg-bins=-Wl,-sectcreate,__TEXT,__info_plist,{dir}/macos/Info.plist"
        );
    }
}
