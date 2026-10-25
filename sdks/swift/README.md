# Open Health Sync for Swift

Swift client for the Open Health Sync REST API, for iOS 15+, macOS 12+, watchOS 8+ and Linux
(client only), with an optional HealthKit sync for apps that uploads new samples and the
deletions of removed or edited ones.

```sh
swift test                                   # unit tests, no server (HealthKit sync through fakes)
OHS_URL=http://localhost:8080 OHS_KEY=ohs_sk_... swift run OHSExample
```

Guide: [docs/sdks/swift.md](../../docs/sdks/swift.md). Licence: AGPL-3.0-only, like the server ([LICENSE](../../LICENSE)).
