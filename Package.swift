// swift-tools-version:5.9
// SPDX-License-Identifier: AGPL-3.0-only

import PackageDescription

// The Swift SDK as a package at the root of the repository, so Swift Package Manager can
// take it by URL: a package URL is a repository, and its manifest must be at the root.
// The sources stay in sdks/swift, whose own Package.swift is the one to develop and test
// the SDK with (`cd sdks/swift && swift test`); this manifest only exposes its libraries.
// Keep the platforms, products and targets of the two files the same.
let package = Package(
    name: "OpenHealthSync",
    platforms: [
        .iOS(.v15),
        .macOS(.v12),
        .watchOS(.v8),
    ],
    products: [
        .library(name: "OpenHealthSync", targets: ["OpenHealthSync"]),
        .library(name: "OpenHealthSyncHealthKit", targets: ["OpenHealthSyncHealthKit"]),
        .library(name: "OpenHealthSyncBluetooth", targets: ["OpenHealthSyncBluetooth"]),
    ],
    targets: [
        .target(
            name: "OpenHealthSync",
            path: "sdks/swift/Sources/OpenHealthSync"
        ),
        .target(
            name: "OpenHealthSyncHealthKit",
            dependencies: ["OpenHealthSync"],
            path: "sdks/swift/Sources/OpenHealthSyncHealthKit"
        ),
        .target(
            name: "OpenHealthSyncBluetooth",
            dependencies: ["OpenHealthSync"],
            path: "sdks/swift/Sources/OpenHealthSyncBluetooth"
        ),
    ]
)
