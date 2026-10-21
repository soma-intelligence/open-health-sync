# Example apps

Small apps built on the SDKs. The phone apps upload health data through the client SDKs and
hold only an upload-scope user token, as a real app would; you read the data back with the
tenant key (each README shows the curl calls). The web app is the backend side: it holds the
tenant key on its server and goes from the first user to webhooks.

| App | SDK | Data source |
|---|---|---|
| [ios](ios) | [Swift](../docs/sdks/swift.md) | HealthKit, with background delivery |
| [android](android) | [Kotlin](../docs/sdks/kotlin.md) | a heart rate you type, and standard Bluetooth health devices (Health Connect reading is planned) |
| [react-native](react-native) | [React Native](../docs/sdks/react-native.md) | a heart rate you type |
| [web](web) | [server SDK, TypeScript](../docs/server-sdks.md) | synthetic sample data, and the hosted connection page for cloud sources |

CI builds all four on every change to `examples/` or `sdks/` (`.github/workflows/examples.yml`),
and runs the web app's tests.
Licence: AGPL-3.0-only, like the rest of the repository.
