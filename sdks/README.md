# SDKs

Client libraries for the Open Health Sync API. Each directory is a self-contained project with
its own build, tests and example.

| Directory | Guide |
|---|---|
| [kotlin](kotlin) | [docs/sdks/kotlin.md](../docs/sdks/kotlin.md) |
| [swift](swift) | [docs/sdks/swift.md](../docs/sdks/swift.md) |
| [react-native](react-native) | [docs/sdks/react-native.md](../docs/sdks/react-native.md) |
| [flutter](flutter) | [docs/sdks/flutter.md](../docs/sdks/flutter.md) |

Overview and shared conventions: [docs/sdks/README.md](../docs/sdks/README.md). CI runs every
SDK's unit tests and its quickstart example against a live server (`.github/workflows/sdks.yml`,
`test/e2e/sdks.sh`).
