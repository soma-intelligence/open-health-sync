// swift-tools-version:5.9
// SPDX-License-Identifier: AGPL-3.0-only

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
    ],
    targets: [
        .target(name: "OpenHealthSync"),
        .target(
            name: "OpenHealthSyncHealthKit",
            dependencies: ["OpenHealthSync"]
        ),
        .executableTarget(
            name: "OHSExample",
            dependencies: ["OpenHealthSync"]
        ),
        .testTarget(
            name: "OpenHealthSyncTests",
            dependencies: ["OpenHealthSync"]
        ),
    ]
)
