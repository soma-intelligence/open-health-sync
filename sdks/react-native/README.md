# Open Health Sync for React Native

Two things in one package:

- A typed TypeScript client for the Open Health Sync REST API. It runs in React Native 0.72+,
  Expo, Node 18+ and browsers, uses the global `fetch`, and has one runtime dependency
  (`fflate`, for gzip). It does not depend on React.
- A native module, `open-health-sync-react-native/health`, that reads HealthKit (iOS) and Health
  Connect (Android) and uploads what changed, in the foreground and in the background, with an
  offline queue on Android. It wraps the Swift SDK (`sdks/swift`) and the Kotlin SDK's
  health-connect module (`sdks/kotlin/health-connect`). It needs React Native 0.80+ with the New
  Architecture and a native build (not Expo Go).

```ts
import { healthSync } from "open-health-sync-react-native/health";

await healthSync.configure({ baseUrl: "https://ohs.example.org", userToken });
await healthSync.requestAuthorization(["steps", "heartRate", "sleep"], { background: true });
const report = await healthSync.sync(["steps", "heartRate", "sleep"]);
await healthSync.enableBackgroundSync({ types: ["steps", "heartRate", "sleep"] });
```

The guide is [docs/sdks/react-native.md](../../docs/sdks/react-native.md): install, client,
paging, connections, uploads, health store sync (setup for Expo and by hand, data types, the
sync report, background sync), errors, tests and the example.

Layout:

| Path | |
|---|---|
| `src/` | The client; `src/health/` the health sync API and the codegen spec |
| `ios/`, `OpenHealthSyncReactNative.podspec` | The iOS module (Swift over `HealthKitSync`) |
| `android/` | The Android module (Kotlin over `HealthConnectSync`), and `local-maven.init.gradle` |
| `app.plugin.js`, `plugin/` | The Expo config plugin |
| `types/` | The few React Native names `src/health` uses, so the package builds without React Native |

Until the native SDKs are on a registry, iOS takes the Swift package from the checkout this
folder sits in (or `OHS_SWIFT_SDK_PATH`), and Android takes the Kotlin SDK from a Maven
repository on disk:

```sh
cd ../kotlin
./gradlew -I ../react-native/android/local-maven.init.gradle publishAllPublicationsToOhsLocalRepository
```

```sh
npm install
npm run lint
npm test
npm run build
OHS_URL=http://localhost:8080 OHS_KEY=ohs_sk_... npm run example
```

Version 0.1.0, not published to any registry. AGPL-3.0-only, like the server (see [LICENSE](../../LICENSE)).
