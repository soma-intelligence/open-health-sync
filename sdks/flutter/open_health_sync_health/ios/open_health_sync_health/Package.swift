// swift-tools-version: 5.9
// SPDX-License-Identifier: AGPL-3.0-only

import Foundation
import PackageDescription

// The iOS side of the Flutter plugin. It is a thin layer over the Swift SDK of the same
// repository (OpenHealthSyncHealthKit), which reads HealthKit, maps the samples and uploads.
//
// Where the Swift SDK comes from:
// - by default, from the repository by URL, from the first tag that has the Package.swift at
//   its root (v0.3.0, see docs/sdks/swift.md);
// - before that tag, or to work on the SDK, from a checkout: set OHS_SWIFT_SDK_PATH to the
//   absolute path of the repository root in the environment of the build
//   (`OHS_SWIFT_SDK_PATH=/path/to/open-health-sync flutter run`).
let sdkPackage = "open-health-sync"
let sdk: Package.Dependency
if let path = ProcessInfo.processInfo.environment["OHS_SWIFT_SDK_PATH"], !path.isEmpty {
    sdk = .package(name: sdkPackage, path: path)
} else {
    sdk = .package(url: "https://github.com/soma-intelligence/open-health-sync", from: "0.3.0")
}

let package = Package(
    name: "open_health_sync_health",
    platforms: [
        .iOS("15.0")
    ],
    products: [
        .library(name: "open-health-sync-health", targets: ["open_health_sync_health"])
    ],
    dependencies: [
        .package(name: "FlutterFramework", path: "../FlutterFramework"),
        sdk,
    ],
    targets: [
        .target(
            name: "open_health_sync_health",
            dependencies: [
                .product(name: "FlutterFramework", package: "FlutterFramework"),
                .product(name: "OpenHealthSyncHealthKit", package: sdkPackage),
            ]
        )
    ]
)
