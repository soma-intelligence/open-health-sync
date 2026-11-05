// swift-tools-version:5.9
// SPDX-License-Identifier: AGPL-3.0-only
//
// The Swift SDK, at the repository root so Swift Package Manager and the Swift Package Index
// can use the repository URL:
//
//   .package(url: "https://github.com/soma-intelligence/open-health-sync.git", branch: "main")
//
// The code lives in sdks/swift, whose own Package.swift (with the example) is the one to work
// in. Keep the products and targets here in step with it.

import PackageDescription

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
        .target(name: "OpenHealthSync", path: "sdks/swift/Sources/OpenHealthSync"),
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
        .testTarget(
            name: "OpenHealthSyncTests",
            dependencies: ["OpenHealthSync", "OpenHealthSyncBluetooth"],
            path: "sdks/swift/Tests/OpenHealthSyncTests"
        ),
    ]
)
