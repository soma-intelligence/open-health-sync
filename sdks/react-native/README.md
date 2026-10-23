# Open Health Sync for React Native

A typed TypeScript client for Open Health Sync, the open source, self-hosted health data
aggregator: data from Apple Health, Health Connect, Oura, WHOOP, Withings and more through one
REST API. It runs in React Native 0.72+,
Expo, Node 18+ and browsers, uses the global `fetch`, and has one runtime dependency
(`fflate`, for gzip). It does not depend on React.

The guide is [docs/sdks/react-native.md](https://github.com/soma-intelligence/open-health-sync/blob/main/docs/sdks/react-native.md): install, client,
paging, connections, uploads, errors, tests and the example.

```sh
npm install
npm run lint
npm test
npm run build
OHS_URL=http://localhost:8080 OHS_KEY=ohs_sk_... npm run example
```

Version 0.1.0, not on npm yet: the guide shows how to install it from this folder. AGPL-3.0-only, like the server (see [LICENSE](https://github.com/soma-intelligence/open-health-sync/blob/main/LICENSE)).
