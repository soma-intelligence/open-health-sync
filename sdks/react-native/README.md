# Open Health Sync for React Native

A typed TypeScript client for the Open Health Sync REST API. It runs in React Native 0.72+,
Expo, Node 18+ and browsers, uses the global `fetch`, and has one runtime dependency
(`fflate`, for gzip). It does not depend on React.

The guide is [docs/sdks/react-native.md](../../docs/sdks/react-native.md): install, client,
paging, connections, uploads, errors, tests and the example.

```sh
npm install
npm run lint
npm test
npm run build
OHS_URL=http://localhost:8080 OHS_KEY=ohs_sk_... npm run example
```

Version 0.1.0, not published to any registry. MIT licensed (see [../LICENSE](../LICENSE)).
