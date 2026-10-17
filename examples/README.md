# Example apps

Small apps that upload health data to an Open Health Sync server through the client SDKs. Each
holds only an upload-scope user token, as a real app would; you read the data back with the
tenant key (each README shows the curl calls).

| App | SDK | Data source |
|---|---|---|
| [ios](ios) | [Swift](../docs/sdks/swift.md) | HealthKit, with background delivery |
| [android](android) | [Kotlin](../docs/sdks/kotlin.md) | a heart rate you type (Health Connect reading is planned) |
| [react-native](react-native) | [React Native](../docs/sdks/react-native.md) | a heart rate you type |

CI builds all three on every change to `examples/` or `sdks/` (`.github/workflows/examples.yml`),
then runs each one end to end: the Android and React Native apps on an Android emulator, the iOS
app on an iOS simulator, each against a server built from the same commit. The app uploads through
its own screens and [`test/e2e/apps.sh`](../test/e2e/apps.sh) reads the samples back with the tenant
key. Run it yourself with `test/e2e/apps.sh android|react-native|ios` (each app's README says what
it needs).
Licence: AGPL-3.0-only, like the rest of the repository.
