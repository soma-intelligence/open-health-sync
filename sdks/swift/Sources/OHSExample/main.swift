// SPDX-License-Identifier: MIT

// Uploads one heart rate reading for a new user and reads it back.
//
//   OHS_URL=http://localhost:8080 OHS_KEY=ohs_sk_... swift run OHSExample
//
// The tenant key does what your server does (create the user, mint a token, read); the
// upload goes with the user token, as an app would send it. Prints no token and no value.

import Foundation
import OpenHealthSync

func fail(_ message: String) -> Never {
    FileHandle.standardError.write(Data(("error: " + message + "\n").utf8))
    exit(1)
}

func run(baseURL: URL, tenantKey: String) async throws -> Int {
    let tenant = OHSClient(baseURL: baseURL, token: tenantKey)

    let user = try await tenant.createUser(externalRef: "swift-example-" + UUID().uuidString.lowercased())
    print("created user \(user.id)")

    let token = try await tenant.mintUserToken(userId: user.id, scopes: [.upload], ttlSeconds: 600)
    let userToken = token.token
    let app = OHSClient(baseURL: baseURL, tokenProvider: { userToken })

    let utc = TimeZone(identifier: "UTC") ?? TimeZone(secondsFromGMT: 0)!
    let now = Date()
    let line = QuantityLine(
        quantityType: "HKQuantityTypeIdentifierHeartRate",
        startDate: now,
        endDate: now,
        value: 72,
        unit: "count/min",
        metadata: ["HKTimeZone": "UTC"])
    var builder = try AppleHealthPartBuilder(batch: AppleHealthPartBuilder.newBatchId(prefix: "swift-example"))
    try builder.add(line, defaultTimeZone: utc)
    let parts = builder.build()
    let results = try await app.uploadParts(parts)
    print("uploaded \(results.count) part(s)")

    let coverage = try await tenant.waitUntilProcessed(userId: user.id, timeout: 120, pollInterval: 1)
    print("processed, rejected records: \(coverage.rejectedRecords)")

    let userId = user.id
    let samples = try await OHSClient.allItems { cursor in
        try await tenant.listSamples(userId: userId, type: "heart_rate", cursor: cursor)
    }
    print("heart_rate samples: \(samples.count)")
    return samples.count
}

let environment = ProcessInfo.processInfo.environment
guard let urlString = environment["OHS_URL"], let baseURL = URL(string: urlString) else {
    fail("set OHS_URL to the instance, for example http://localhost:8080")
}
guard let tenantKey = environment["OHS_KEY"], !tenantKey.isEmpty else {
    fail("set OHS_KEY to a tenant key")
}

do {
    let count = try await run(baseURL: baseURL, tenantKey: tenantKey)
    if count != 1 {
        fail("expected 1 heart_rate sample, got \(count)")
    }
    print("ok")
} catch {
    fail("\(error)")
}
