# Health Data Vault Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Seal every health value at rest under a per-user key, so a copy of the database, the
raw store or a backup reveals no health value, while every feature keeps working.

**Architecture:** A new `internal/vault` package owns the key hierarchy (root key behind a
`KeyProvider`, tenant key, user keys), row sealing (one XChaCha20-Poly1305 blob per row), object
sealing (hybrid ML-KEM-1024 + X25519, streamed) and keyed hashes. The ETL writer seals rows and
writes sealed hourly buckets; the read layer opens rows and aggregates in Go; collect seals raw
objects with public keys only. Release N writes sealed and reads both forms; `ohs vault seal`
converts old data; release N+1 drops the clear columns.

**Tech Stack:** Go 1.26 (`crypto/mlkem`, `crypto/ecdh`, `crypto/sha3`, `crypto/hkdf`,
`crypto/hmac`), `golang.org/x/crypto/chacha20poly1305`, Postgres + TimescaleDB, pgx v5. Behind
build tags: Azure Key Vault (`azkeys`, `azidentity`), AWS KMS (`aws-sdk-go-v2/service/kms`).

**Spec:** [specs/vault.md](../specs/vault.md). Read it first; this plan does not repeat its
reasons.

## Global Constraints

- License header `// SPDX-License-Identifier: AGPL-3.0-only` on every new Go and SQL file.
- Never log key material (wrapped or not) or the plaintext of a sealed field.
- Never write a health value in clear once a user has keys; never serve an empty answer for a row
  that failed to open.
- Row format: `0x02 || user key version (uint32 BE) || nonce (24) || ciphertext || tag (16)`.
- Row AAD: `"ohs.vault.row.v1" || tenant_id (16) || user_id (16) || table name || 0x00 || row identity`.
- Object format: `"OHSV" || 0x01 || user key version (uint32) || digest (32) || ct_m (1568) || ct_x (32) || chunks`; chunk = `nonce (24) || ciphertext (<= 64 KiB) || tag (16)`; chunk AAD = header || chunk index (uint64 BE) || final flag (1 byte).
- KEM combiner: `k = SHA3-256("ohs.vault.kem.v1" || ss_m || ss_x || ct_x || x25519_pub)`.
- Local root key default: `HKDF-SHA-256(OHS_ENCRYPTION_KEY, info "ohs.vault.root.v1")`, 32 bytes.
- Key cache defaults: TTL 10 minutes, 1024 users; low-memory profile: 128 users.
- Performance targets (bench, demo data set): stats 30 days at most 2x p50 and p95; ETL throughput at most 20% lower; at most one KEM operation per raw object; `ohs_data` at most 25% larger.
- No dependency added outside the build-tagged KMS providers and `x/crypto` (already in the tree).
- No product or codename terms in code, docs or commits (`make guard` with the terms file).
- No em dash in docs or comments.
- Migration numbers in this plan (`0044`, `0045`) are placeholders: take the next free numbers when the branch is cut, and again at merge.

## Review Focus

1. **Stats in a zone with a non-whole-hour offset** (Asia/Kolkata +05:30, Australia/Adelaide): the Go fold from samples must give the same totals as today's SQL path. Test in Task 14.
2. **Rows sealed under two user key versions** after `ohs vault rotate user`: reads, stats and exports must open both. Test in Task 18.
3. **User deleted while an ETL job runs for them**: the job must fail or finish cleanly, never write rows after the keys are gone. Test in Task 9.
4. **The same raw upload retried after a timeout**: a sealed object differs on every write, the retry must still be recognized as the same upload and answer as today. Test in Task 10.
5. **KMS unavailable with a cold cache**: reads answer 503 with `Retry-After`, `/readyz` goes 503, nothing answers 500 or empty. Test in Task 8.

---

## File Structure

```
internal/vault/
  doc.go          package doc: the hierarchy and what each key does
  codec.go        Table/Column descriptors, Encode/Decode of a row's sealed fields
  schema.go       the descriptor of every sealed table (single source of truth)
  row.go          sealRow/openRow (XChaCha20-Poly1305, AAD)
  kem.go          PublicKey, Encapsulate, Decapsulate (hybrid)
  object.go       SealObject, OpenObject, ObjectHeader (streamed)
  keys.go         UserKeys: generate, marshal, zero, MAC, SourceKey
  provider.go     KeyProvider interface, Open(cfg) factory
  provider_local.go
  provider_hashicorp.go
  provider_azurekv.go        //go:build azurekv
  provider_awskms.go         //go:build awskms
  store.go        Vault: vault_keys rows, wrap/unwrap, EnsureUser, Shred, ForUser
  cache.go        LRU with TTL, zero on evict
  notify.go       LISTEN/NOTIFY eviction across processes
  user.go         UserVault: per-user handle used by writer and reader
  seal.go         `ohs vault seal` / `status` conversion engine
  rotate.go       rotate root / tenant / user, reseal
internal/db/migrations/0044_vault.up.sql / .down.sql
internal/db/migrations/0045_vault_drop_clear.up.sql / .down.sql   (release N+1, Task 21)
cmd/ohs/vault.go  the `ohs vault` command group
test/vault/       golden fixture, leak test, equivalence test
```

Rows: the writer and the reader never list sealed columns themselves; they ask
`vault.Schema[table]`. Adding a column to a sealed table means adding it to the descriptor.

---

### Task 1: Golden fixture from today's outputs

Captured before any vault code, so equivalence (Task 19) compares against real `main` behavior.

**Files:**
- Create: `test/vault/golden_integration_test.go`, `test/vault/testdata/golden/*.json`

**Interfaces:**
- Produces: `func captureOutputs(t *testing.T, srv *httptest.Server, key string, user uuid.UUID) map[string][]byte` (name to response body) and the golden files, one per endpoint.

- [ ] **Step 1:** Write `TestGoldenCapture` (tag `integration`, runs only with `OHS_VAULT_GOLDEN_WRITE=1`): seed the demo data set with its fixed seed (`internal/demo`), then call and store bodies of: samples list (each quantity type of the demo), `samples/stats` hourly and daily in `UTC`, `America/Los_Angeles`, `Asia/Kolkata`, sleep sessions, workouts, daily summaries, cycles, ECG, heartbeat, FHIR `$everything` bundle, OMH export, CSV export, the MCP tools `daily_summary` and `sleep_summary`. Strip `ingested_at`, request ids and any generated id with a helper `normalize(body []byte) []byte`.
- [ ] **Step 2:** Run on the branch point: `OHS_VAULT_GOLDEN_WRITE=1 go test -tags integration ./test/vault/ -run TestGoldenCapture`. Expected: files written under `testdata/golden/`.
- [ ] **Step 3:** Write `TestGoldenStable`: run capture twice in one test and compare, so the normalizer is complete. Run without the env var. Expected: PASS.
- [ ] **Step 4:** Commit: `test: capture golden read outputs before the vault`.

### Task 2: Row codec and schema descriptors

**Files:**
- Create: `internal/vault/doc.go`, `internal/vault/codec.go`, `internal/vault/schema.go`, `internal/vault/codec_test.go`

**Interfaces:**
- Produces:
```go
type Kind uint8 // KindFloat64, KindInt64, KindBool, KindText, KindJSON, KindTime, KindBytes
type Column struct { Name string; Tag uint8; Kind Kind }
type Table struct { Name string; Sealed []Column; Identity []string; MAC bool }
func (t Table) Encode(vals []any) ([]byte, error)   // vals in Sealed order; nil = SQL NULL
func (t Table) Decode(b []byte) ([]any, error)
var Schema map[string]Table // keyed by table name without schema prefix
```
Encoding: format byte `0x01`, then per non-null field `tag (1) || length (uvarint) || value`; float64 as IEEE 754 bits BE, int64 as zigzag varint, time as UnixMicro varint, JSON as its bytes after `etl.normalizeMetadata` order. Unknown tags on decode are an error. Tags are never reused.

- [ ] **Step 1:** Tests: `TestCodecRoundTripEveryKind` (NaN bits, -0.0, `math.MaxFloat64`, empty string vs NULL, empty JSON object, a zero time vs NULL all come back exact); `TestCodecUnknownTag` (error); `TestCodecTruncated` (every prefix of a valid encoding fails); `TestSchemaCoversEveryDataTable` (lists `ohs_data` tables from the migrations directory and requires each to be in `Schema` or in `vault.Unsealed = []string{"quantity_codes", "deleted_records", "record_aliases", "device_aliases"}`); `TestSchemaTagsUnique`.
- [ ] **Step 2:** Run `go test ./internal/vault/`. Expected: FAIL (undefined).
- [ ] **Step 3:** Implement. Fill `Schema` by reading each `CREATE TABLE ohs_data.*` and later `ALTER TABLE` in the migrations: sealed = every column not in the clear list of spec section "What stays in clear" plus the promoted columns of Task 7 (`source_key`, `worn`, `tz`, `derived`, `synthesized`, `summary_date`). For `deleted_records`, `record_aliases`, `device_aliases`: confirm each column carries no value and say so in a comment beside `Unsealed`; move the table to `Schema` if one does.
- [ ] **Step 4:** Run tests. Expected: PASS.
- [ ] **Step 5:** Commit: `feat(vault): row codec and sealed table descriptors`.

### Task 3: User keys, row AEAD, hybrid KEM

**Files:**
- Create: `internal/vault/keys.go`, `internal/vault/row.go`, `internal/vault/kem.go`, tests beside each, `internal/vault/testdata/kem_vectors.json`

**Interfaces:**
- Produces:
```go
type UserKeys struct { Version uint32; Data, Index [32]byte; MLKEMSeed [64]byte; X25519 [32]byte }
func GenerateUserKeys(version uint32) (*UserKeys, error)
func (k *UserKeys) Marshal() []byte; func UnmarshalUserKeys([]byte) (*UserKeys, error)
func (k *UserKeys) Zero()
func (k *UserKeys) Public() (PublicKey, error)
func (k *UserKeys) MAC(label string, parts ...[]byte) [32]byte // HMAC-SHA-256(Index, label || len-prefixed parts)
type PublicKey struct { MLKEM []byte /*1568*/; X25519 []byte /*32*/ }
func (p PublicKey) Marshal() []byte; func UnmarshalPublicKey([]byte) (PublicKey, error)
func Encapsulate(p PublicKey) (k [32]byte, ctM, ctX []byte, err error)
func (k *UserKeys) Decapsulate(ctM, ctX []byte) ([32]byte, error)
func rowAAD(tenant, user uuid.UUID, table string, identity []byte) []byte
func sealRow(key *[32]byte, version uint32, aad, plain []byte) ([]byte, error)
func openRow(keyFor func(version uint32) (*[32]byte, error), aad, sealed []byte) ([]byte, error)
var ErrOpen = errors.New("vault: sealed value does not open")
```

- [ ] **Step 1:** Tests: row round trip; flipping any one bit of the sealed bytes fails with `ErrOpen`; same plaintext sealed twice differs; AAD changed in tenant, user, table or identity fails; unknown version fails; `Encapsulate`/`Decapsulate` agree; decapsulating with another user's keys yields a different key (and the object open fails in Task 4); `TestKEMVectors` checks ML-KEM-1024 from a fixed seed against the encapsulation key bytes in `kem_vectors.json` (generated once from `crypto/mlkem` and cross-checked against the NIST ACVP ML-KEM-1024 keyGen vector for that seed) and a fixed X25519 RFC 7748 vector; `TestMACLabelSeparation` (`MAC("a", x) != MAC("b", x)`, and `MAC("a", "xy") != MAC("a", "x", "y")`); `Zero` leaves every field zero.
- [ ] **Step 2:** Run, expect FAIL.
- [ ] **Step 3:** Implement with `chacha20poly1305.NewX`, `mlkem.NewDecapsulationKey1024(seed)`, `ecdh.X25519()`, `sha3.Sum256` for the combiner of Global Constraints.
- [ ] **Step 4:** Run, expect PASS.
- [ ] **Step 5:** Commit: `feat(vault): user keys, row sealing and hybrid KEM`.

### Task 4: Sealed objects (streamed)

**Files:**
- Create: `internal/vault/object.go`, `internal/vault/object_test.go`, `test/fuzz/vault_object_fuzz_test.go`

**Interfaces:**
- Consumes: `Encapsulate`, `Decapsulate` (Task 3).
- Produces:
```go
type ObjectHeader struct { Version uint32; Digest [32]byte }
func SealObject(dst io.Writer, pub PublicKey, version uint32, digest [32]byte, src io.Reader) error
func OpenObject(src io.Reader, keysFor func(version uint32) (*UserKeys, error)) (ObjectHeader, io.Reader, error)
func ReadObjectHeader(src io.Reader) (ObjectHeader, bool, error) // bool false: not a sealed object (legacy gzip)
func RawDigest(pub PublicKey, body []byte) [32]byte // SHA-256("ohs.vault.raw.v1" || pub.X25519 || pub.MLKEM || body)
```

- [ ] **Step 1:** Tests: round trip for sizes 0, 1, 65535, 65536, 65537, 5 MiB; truncating after any chunk fails (final flag); swapping two chunks fails; dropping the last chunk fails; flipping a header byte fails; `ReadObjectHeader` on a gzip stream returns `false` with no error; `ReadObjectHeader` reads the digest without keys; memory: opening a 64 MiB object allocates under 1 MiB beyond the chunk buffer (`testing.AllocsPerRun` is not enough: use `runtime.ReadMemStats` around a streamed copy to `io.Discard`). Fuzz `FuzzOpenObject` never panics.
- [ ] **Step 2:** Run, expect FAIL.
- [ ] **Step 3:** Implement STREAM: chunk nonce random, AAD per Global Constraints; reader returns `ErrOpen` on the first bad chunk and never yields bytes from an unauthenticated chunk.
- [ ] **Step 4:** Run `go test ./internal/vault/` and `go test ./test/fuzz/ -run FuzzOpenObject`. Expected: PASS.
- [ ] **Step 5:** Commit: `feat(vault): streamed sealed objects`.

### Task 5: KeyProvider, local and HashiCorp transit

**Files:**
- Create: `internal/vault/provider.go`, `provider_local.go`, `provider_hashicorp.go`, tests
- Modify: `internal/config/config.go` (new fields and env names), `internal/config/config_test.go`

**Interfaces:**
- Produces:
```go
type KeyProvider interface {
    Name() string
    Wrap(ctx context.Context, plain, aad []byte) (wrapped []byte, version string, err error)
    Unwrap(ctx context.Context, wrapped, aad []byte, version string) ([]byte, error)
}
func OpenProvider(ctx context.Context, c config.Vault) (KeyProvider, error)
var ErrProviderUnavailable = errors.New("vault: key provider unavailable")
// config:
type Vault struct {
    Provider string // OHS_VAULT_KEY_PROVIDER: local (default), hashicorp, azurekv, awskms
    RootKey []byte; RootKeyPrevious [][]byte // OHS_VAULT_ROOT_KEY(_FILE), OHS_VAULT_ROOT_KEY_PREVIOUS (comma separated base64)
    RootDerived bool // true when derived from OHS_ENCRYPTION_KEY
    HashiCorpAddr, HashiCorpMount, HashiCorpKey, HashiCorpTokenFile string
    AzureKeyID, AWSKeyARN string
    CacheUsers int; CacheTTL time.Duration // OHS_VAULT_CACHE_USERS, OHS_VAULT_CACHE_TTL
}
```
Local `version` = hex of `crypto.KeyID(key)` (reuse `internal/crypto`). HashiCorp: `POST {addr}/v1/{mount}/encrypt/{key}` and `/decrypt/{key}` with `context` = base64(aad) (key must be created with `derived=true`; `OpenProvider` checks with `GET /v1/{mount}/keys/{key}` and refuses otherwise); version = the `vault:vN` prefix. Network or 5xx errors wrap `ErrProviderUnavailable`.

- [ ] **Step 1:** Tests: local wrap/unwrap; previous key unwraps, active wraps; wrong AAD fails; `RootDerived` path derives the HKDF key of Global Constraints (fixed vector); config refuses an unknown provider and a root key not 32 bytes, and sets `RootDerived` when neither root env is set; HashiCorp against an `httptest.Server` fake (encrypt, decrypt, `derived=false` refused, 503 maps to `ErrProviderUnavailable`); `ohs doctor` warns `vault root key is derived from OHS_ENCRYPTION_KEY: one secret guards both` (test in `cmd/ohs` doctor tests).
- [ ] **Step 2:** Run, expect FAIL. **Step 3:** Implement. **Step 4:** Run, expect PASS. Add the variables to `docs/self-hosting.md` env table (the docscheck test lists env names).
- [ ] **Step 5:** Commit: `feat(vault): key providers (local, HashiCorp transit)`.

### Task 6: Azure Key Vault and AWS KMS providers

**Files:**
- Create: `internal/vault/provider_azurekv.go` (`//go:build azurekv`), `provider_awskms.go` (`//go:build awskms`), `provider_stub.go` (`//go:build !azurekv || !awskms`, returns `vault: provider X not built in, rebuild with -tags X`), tests with the same tags
- Modify: `go.mod`, `.goreleaser.yaml` (or the release workflow's build flags) and `Dockerfile`: release builds pass `-tags azurekv,awskms`

**Interfaces:**
- Consumes: `KeyProvider` (Task 5).
- Azure: `azkeys.Client.WrapKey`/`UnwrapKey`, algorithm `RSA-OAEP-256` for RSA keys, `A256KW` on Managed HSM `oct-HSM` keys; AAD is not supported by wrap, so the plaintext wrapped is `SHA-256(aad) || key` and unwrap checks the prefix. Version = the key version from the key id. Credentials: `azidentity.NewDefaultAzureCredential`.
- AWS: `kms.Encrypt`/`Decrypt` with `EncryptionContext{"ohs": base64(aad)}`; version = `KeyId` returned.

- [ ] **Step 1:** Tests behind tags against fakes of the two HTTP APIs (`httptest.Server` speaking the REST shapes; no cloud account in CI). Prefix check: unwrap with another AAD fails.
- [ ] **Step 2-4:** FAIL, implement, PASS: `go test -tags azurekv,awskms ./internal/vault/`. Also `go build ./...` without tags succeeds and the default binary size grows by under 1 MiB versus the branch point (`ls -l` both builds, note it in the commit body).
- [ ] **Step 5:** Commit: `feat(vault): Azure Key Vault and AWS KMS providers behind build tags`.

### Task 7: Migration 0044 (release N schema)

**Files:**
- Create: `internal/db/migrations/0044_vault.up.sql`, `0044_vault.down.sql`
- Modify: the migration test (`internal/db/migrate_integration_test.go`), the provider erasure classification (it must learn the new tables)

Content:
- `ohs_core.vault_keys` as in spec section "Wrapped keys", plus `ohs_core.vault_root (version text PRIMARY KEY, provider text, created_at, retired_at)`; unique `(tenant_id, user_id, version)` with `user_id` coalesced for the tenant row (partial unique indexes: one `WHERE user_id IS NULL`, one `WHERE user_id IS NOT NULL`); FK to `end_users` with `ON DELETE CASCADE` for user rows.
- On every table in `vault.Schema`: `sealed bytea` (nullable in N), `content_mac bytea` where the table has an upsert (`Table.MAC`), and the promoted clear columns: `source_key bytea` and `worn boolean` on sample, workout and sleep tables used by the merge; `tz text`, `derived boolean`, `synthesized boolean`, `summary_date date` where the code reads them from `metadata` today (`writer_aliases.go:48`, `writer_deletions.go:69,279`, `sleep_sessionize.go:145`, `cycles.go:137`, `merge.go:240-246`). Backfill the promoted columns from the clear columns in the migration itself (pure SQL, before any sealing).
- `ohs_data.quantity_hourly_sealed (tenant_id, user_id, quantity_type, provider, source_key bytea, slug text, worn boolean, unit text, bucket timestamptz, sealed bytea NOT NULL, PRIMARY KEY (tenant_id, user_id, quantity_type, provider, source_key, slug, worn, unit, bucket))`, a hypertable on `bucket` when TimescaleDB is present.
- The NOT NULL constraints of sensitive columns are dropped (they become NULL once sealed).
- The continuous aggregate stays in N (reads of unconverted users use it).

- [ ] **Step 1:** Extend the migration test: up then down then up leaves the schema identical; the backfilled `tz`/`summary_date`/`worn` equal the metadata-derived expressions on a seeded row.
- [ ] **Step 2-4:** FAIL, write SQL, PASS: `go test -tags integration ./internal/db/`.
- [ ] **Step 5:** Commit: `feat(db): vault schema (release N)`.

### Task 8: Vault store, cache, provider failures

**Files:**
- Create: `internal/vault/store.go`, `cache.go`, `notify.go`, `user.go`, `store_integration_test.go`, `cache_test.go`
- Modify: `internal/api/router.go` (readyz) and the error mapper for `ErrProviderUnavailable` to 503 `Retry-After: 5`

**Interfaces:**
- Consumes: Tasks 3, 5, 7.
- Produces:
```go
type Options struct { CanOpen bool; CacheUsers int; CacheTTL time.Duration; Metrics *Metrics }
func New(pool *pgxpool.Pool, p KeyProvider, o Options) *Vault
func (v *Vault) EnsureUser(ctx context.Context, tx pgx.Tx, tenant, user uuid.UUID) error // creates tenant key if missing
func (v *Vault) PublicKey(ctx context.Context, db Querier, tenant, user uuid.UUID) (PublicKey, uint32, error) // no provider call
func (v *Vault) ForUser(ctx context.Context, tenant, user uuid.UUID) (*UserVault, error) // ErrNoKeys, ErrCannotOpen when !CanOpen
func (v *Vault) Shred(ctx context.Context, tx pgx.Tx, tenant, user uuid.UUID) error // deletes rows, NOTIFY ohs_vault
func (v *Vault) Listen(ctx context.Context) error // evicts on NOTIFY, run as a role goroutine
func (v *Vault) Ready(ctx context.Context) error
type UserVault struct { /* tenant, user, keys by version */ }
func (u *UserVault) SealRow(t Table, identity []byte, vals []any) ([]byte, error)
func (u *UserVault) OpenRow(t Table, identity []byte, sealed []byte) ([]any, error)
func (u *UserVault) ContentHash(canonical []byte) string   // hex HMAC, label "content"
func (u *UserVault) ContentMAC(t Table, vals []any) []byte // label "row:"+t.Name over Encode(vals)
func (u *UserVault) SourceKey(source string) []byte        // label "source"
func (u *UserVault) Keys(version uint32) (*UserKeys, error)
var ErrNoKeys, ErrCannotOpen error
type Querier interface { QueryRow(ctx context.Context, sql string, args ...any) pgx.Row } // pgx.Tx and *pgxpool.Pool both fit
```
Cache size: `Options.CacheUsers` from config, 128 when the low-memory profile is on (`internal/memlimit`).
Audit `vault.key_created` on creation. Metrics `ohs_vault_seals_total{result}`, `ohs_vault_opens_total{result}`, `ohs_vault_open_failures_total{kind}`, `ohs_vault_cache_total{result}`, `ohs_vault_provider_seconds`. `Ready` unwraps the newest tenant key once (cached) and fails when the provider fails.

- [ ] **Step 1:** Tests: `EnsureUser` is idempotent and concurrent-safe (two goroutines, one row); `ForUser` hits the provider once per tenant per TTL (counting fake provider); TTL expiry and LRU eviction call `Zero` (fake keys observe it); a NOTIFY from another `Vault` evicts within 1 s; `CanOpen=false` makes `ForUser` return `ErrCannotOpen` and the counting provider sees zero calls even after `PublicKey`; Review Focus 5: provider fails, cold cache: a read handler test gets 503 with `Retry-After`, and `/readyz` 503; warm cache keeps serving.
- [ ] **Step 2-4:** FAIL, implement, PASS: `go test ./internal/vault/` and `go test -tags integration ./internal/vault/ ./internal/api/`.
- [ ] **Step 5:** Commit: `feat(vault): key store, cache and readiness`.

### Task 9: Key lifecycle in the app (creation, wiring, erasure)

**Files:**
- Modify: `internal/tenant/store.go:294` (`CreateUser` calls `EnsureUser` in its transaction), `internal/app/app.go` (build the Vault from config; `CanOpen = Roles["api"] || Roles["etl"]`; start `Listen` and a startup pass that creates missing keys for existing users in batches of 500 when `CanOpen`), `internal/deletion/worker.go` (`Shred` in its own committed transaction right after the user row is taken `FOR UPDATE`, before rows and raw objects), `internal/deletion/*_integration_test.go`
- Produces: audit `vault.user_shredded`.

- [ ] **Step 1:** Tests: a created user has keys; the startup pass gives keys to users created by SQL without them; deletion: after the shred commit, `ForUser` returns `ErrNoKeys` in another process (second `Vault`); crash simulation: shred committed, rows not yet removed, then `OpenRow` on a remaining row is impossible and a later deletion pass removes the rows; Review Focus 3: an ETL job holding the user `FOR SHARE` and a deletion started concurrently: the deletion waits for the job, the job's rows are then shredded and removed, and a job starting after the shred fails with `ErrNoKeys` and is not retried forever (marked failed like a deleted user today).
- [ ] **Step 2-4:** FAIL, implement, PASS: `go test -tags integration ./internal/deletion/ ./internal/tenant/ ./internal/app/`.
- [ ] **Step 5:** Commit: `feat(vault): create keys with users and shred them first on erasure`.

### Task 10: Sealed raw objects

**Files:**
- Modify: `internal/collect/sink.go:94`, `internal/collect/upload.go:404` (hash becomes `hex(RawDigest(pub, body))`), `internal/collect/rawobject.go` (`putRaw` writes `SealObject(gzip(body))`; `sameBytes` becomes `sameDigest`: `ReadObjectHeader` of the stored object vs the digest; a legacy unsealed object compares bytes as today), `internal/etl/reader.go` (open sealed objects through `UserVault.Keys`; legacy gzip still read), `internal/export/export.go` (any file kept at rest is sealed the same way; streamed answers unchanged)
- Test: `internal/collect/*_integration_test.go`, `internal/etl/reader_test.go`

**Interfaces:**
- Consumes: `PublicKey`, `RawDigest`, `SealObject`, `ReadObjectHeader`, `OpenObject`, `UserVault.Keys`.

- [ ] **Step 1:** Tests: an upload lands in the raw store as `OHSV...` and contains none of its plaintext bytes; the ETL processes it as before (existing ETL integration tests pass unchanged); a collect-only process (`CanOpen=false`) uploads and pulls fine; Review Focus 4: the same body uploaded twice (second after an injected registration failure, so the file exists unregistered) answers as today (`inserted=false`, same response code) and keeps one object; a different body under the same key still gives `errOtherBytes`.
- [ ] **Step 2-4:** FAIL, implement, PASS: `go test -tags integration ./internal/collect/ ./internal/etl/ ./internal/export/`.
- [ ] **Step 5:** Commit: `feat(vault): seal raw objects at ingestion`.

### Task 11: ETL writer seals rows

**Files:**
- Modify: `internal/etl/writer.go`, `writer_samples.go`, `writer_parents.go`, `writer_aliases.go`, `writer_deletions.go`, `daily.go`, `tracking.go`, `sleep_sessionize.go`, `processor.go` (obtain the `UserVault` once per job, pass it in `scope`)
- Test: existing `internal/etl/*_integration_test.go` plus `internal/etl/writer_vault_integration_test.go`

Rules for every insert path (`insertBatches`, `writeHighFrequencyCopy`, `insertParentWithFallback`, the upserts):
- `content_hash` = `uv.ContentHash(canonical)`, where `canonical` is exactly the bytes hashed today (move today's SHA-256 input builder behind one function `canonicalContent(...) []byte` per record kind; do not change its bytes).
- Clear columns are written as today; sealed columns are written as NULL; `sealed` = `uv.SealRow(Schema[table], identity, vals)`, where `identity` = the table's dedup key encoded as `content_hash || start_date (UnixMicro BE)` (or the table's own key listed in `Table.Identity`).
- `source_key`, `worn`, `tz`, `derived`, `synthesized`, `summary_date` filled from the record.
- Upserts compare `content_mac` instead of value columns: `latestWins` and `updateStored` build `WHERE t.content_mac IS DISTINCT FROM EXCLUDED.content_mac`.
- SQL that reads metadata keys (`writer_aliases.go:48`, `writer_deletions.go:69,279`, `sleep_sessionize.go:145`) reads the promoted columns.
- Daily sums (`daily.go:223`) and sleep totals (`sleep_sessionize.go:168-171`): SQL selects the rows (clear columns + `sealed`, plus the clear value columns of rows not yet converted), Go opens and sums, Go writes the sealed summary. Factor a helper `openRows(ctx, tx, uv, table, sql, args) iter.Seq2[Row, error]` in `internal/etl/vaultrows.go` that yields opened rows whether `sealed` is set or the row is still clear.

- [ ] **Step 1:** Test `TestWriterSealsEveryTable`: run the ETL fixtures of every provider in `internal/etl/testdata`, then for every table in `Schema`, assert every row has `sealed` set and every sealed column NULL, and that `content_hash` differs from the plain SHA-256 of the canonical bytes. `TestUpsertSkipsUnchanged`: the same daily summary written twice leaves `xmin` unchanged. All existing ETL integration tests must pass with their assertions moved to opened values (change their row readers to use `openRows`, never their expectations).
- [ ] **Step 2-4:** FAIL, implement, PASS: `go test -tags integration ./internal/etl/...`.
- [ ] **Step 5:** Commit: `feat(etl): seal every health row`.

### Task 12: Sealed hourly buckets

**Files:**
- Create: `internal/etl/hourly.go`, `internal/etl/hourly_integration_test.go`
- Modify: `processor.go` (call after the samples write, same transaction), `writer_deletions.go` (deletions recompute the hours they touch), `internal/deletion/provider_worker.go` (provider erasure recomputes or deletes buckets)

**Interfaces:**
- Produces: `func RecomputeHours(ctx context.Context, tx TxLike, uv *vault.UserVault, tenant, user uuid.UUID, hours []HourKey) error` with `type HourKey struct{ QuantityType string; Bucket time.Time }`; bucket sealed fields `{n int64, sum, min, max float64}` (descriptor `Schema["quantity_hourly_sealed"]`, identity = the primary key columns after `user_id`).

- [ ] **Step 1:** Tests: after an ETL job, for every touched hour, the opened bucket equals `count/sum/min/max` computed from the opened samples of that hour grouped as the continuous aggregate groups (source, slug, worn, unit); deleting one sample updates its hour; erasing a provider removes its buckets; an import of last year fills last year's hours.
- [ ] **Step 2-4:** FAIL, implement, PASS.
- [ ] **Step 5:** Commit: `feat(etl): sealed hourly buckets`.

### Task 13: Read layer opens rows

**Files:**
- Modify: every file of `internal/api/read/` listed by `grep -l ohs_data internal/api/read/*.go`, `internal/timetag/store.go`, `internal/consent/purge.go`, `internal/shl/store.go`, `internal/api/handlers_pipeline.go`, `internal/etl/automation/{drainer,reconciler,retention}.go` (only where they read sealed columns)
- Create: `internal/api/read/vaultrows.go` (the reader-side twin of Task 11's helper; share code through `internal/vault` if the two are identical)

Rules: queries select clear columns plus `sealed` (and the clear value columns while N reads both); scanning goes through one function per table that opens and fills today's structs, so FHIR, OMH, MCP, exports and share links (consumers of `read.*`) are unchanged. `merge.go:240-246` ranks with `source_key` and `worn` instead of the metadata expressions. Value filters, if `grep` finds any `WHERE` on a sealed column, move to Go after opening with the page size applied after filtering (loop until the page is full or rows run out). Every handler gets the `UserVault` from a request-scoped helper `readVault(r) (*vault.UserVault, error)` that maps `ErrNoKeys` to 404 and `ErrProviderUnavailable` to 503.

- [ ] **Step 1:** Run the whole read, FHIR, MCP, export and share link suites on the sealed data written by Task 11: `go test -tags integration ./internal/api/... ./internal/fhir/... ./internal/mcp/... ./internal/export/... ./internal/shl/... ./internal/timetag/... ./internal/consent/...`. Expected now: FAIL (values NULL).
- [ ] **Step 2:** Implement until they pass with no change to their expectations.
- [ ] **Step 3:** Test `TestOpenFailureIs500NotEmpty`: corrupt one `sealed` value; the list endpoint answers 500 with a request id, writes audit `vault.open_failed` with no value, increments the metric; it never returns the page without that row.
- [ ] **Step 4:** Run the contract tests: `make contract`. Expected: PASS.
- [ ] **Step 5:** Commit: `feat(read): open sealed rows in the read layer`.

### Task 14: Stats from sealed buckets

**Files:**
- Modify: `internal/api/read/stats.go`, `internal/api/read/stats_test.go`
- Create: `internal/api/read/stats_fold.go`, `internal/api/read/stats_integration_test.go`

**Interfaces:**
- Produces: `func foldHours(rows iter.Seq2[HourRow, error], interval time.Duration, zone *time.Location) ([]StatBucket, error)` and `func foldSamples(rows iter.Seq2[SampleRow, error], interval time.Duration, zone *time.Location) ([]StatBucket, error)`, with `StatBucket` the struct `stats.go` answers today.

Path choice stays as today: hourly rows for whole-hour offsets, samples otherwise. For users not yet converted, the continuous aggregate path stays until release N+1.

- [ ] **Step 1:** Tests: `foldHours` and `foldSamples` give today's answers on the golden data in `UTC`, `America/Los_Angeles` across the 2026-11-01 DST change, and Review Focus 1: `Asia/Kolkata` and `Australia/Adelaide` through `foldSamples`, each compared to the golden files of Task 1; an interval of one day, one week, one month; an empty range.
- [ ] **Step 2-4:** FAIL, implement, PASS.
- [ ] **Step 5:** Commit: `feat(read): compute stats in Go from sealed buckets`.

### Task 15: `ohs vault seal` and `ohs vault status`

**Files:**
- Create: `internal/vault/seal.go`, `internal/vault/seal_integration_test.go`, `cmd/ohs/vault.go`
- Modify: `cmd/ohs/help.go`, `cmd/ohs/main.go` (command registration)

**Interfaces:**
- Consumes: Tasks 8, 10, 11, 12.
- Produces: `func Seal(ctx context.Context, v *Vault, pool *pgxpool.Pool, raw rawstore.Store, o SealOptions) (SealReport, error)` with `SealOptions{Batch int /*default 1000*/; Users []uuid.UUID; DryRun bool}`; `func Status(ctx, pool, raw) (StatusReport, error)` counting clear rows per table and unsealed raw objects.

Per user, under the user's `FOR SHARE` lock, per table, batches: select rows with `sealed IS NULL`, compute the HMAC `content_hash` from the stored canonical content, fill promoted columns and `content_mac`, seal, null sensitive columns, one transaction per batch. A `content_hash` change that collides with an existing row means the same record stored twice: keep the older row, delete the newer, count it in the report. Raw objects: read, seal, `Put` under `<key>.v`, update `raw_objects.key` and `content_hash`, delete the old object after commit. Then rebuild the user's hourly buckets with `RecomputeHours` over the user's whole range. Audit `vault.seal_progress` every 10000 rows and at the end. Gauge `ohs_vault_clear_rows{table}`, set from `Status` at startup and after each `Seal` run (spec: rows left in clear).

- [ ] **Step 1:** Tests: on a database filled by the branch point's ETL (load the golden demo data with a build of Task 1's commit, or seed with SQL fixtures in clear), `Seal` leaves `Status` at zero everywhere; killed at random batch boundaries (context cancel after N batches, N random, 20 runs) and restarted, it converges to the same opened data; reads before, during and after are equal to the golden files; a collision case removes one duplicate and reports it.
- [ ] **Step 2-4:** FAIL, implement, PASS: `go test -tags integration ./internal/vault/ ./cmd/ohs/`.
- [ ] **Step 5:** Commit: `feat(vault): ohs vault seal and status`.

### Task 16: Leak test

**Files:**
- Create: `test/vault/leak_integration_test.go`

- [ ] **Step 1:** Test `TestDumpHoldsNoHealthValue`: ingest the demo data set plus planted canaries (a heart rate of `173.4567`, a note `canary-note-5f1c`, a device name `canary-device-9b2e`, a workout route point at `12.3456789,-98.7654321`), run `Seal`, then run `pg_dump --data-only` of the test database (skip with a clear message if `pg_dump` is absent) and walk the raw store directory. Assert neither contains any canary (as text, and the heart rate also as its float64 BE bytes) nor the plain hex SHA-256 of any canonical sample content.
- [ ] **Step 2:** Run: `go test -tags integration ./test/vault/ -run TestDumpHoldsNoHealthValue`. Expected: PASS (if FAIL, the failing table or object is a bug in Tasks 2, 10 or 11; fix it there).
- [ ] **Step 3:** Commit: `test(vault): a dump and the raw store hold no health value`.

### Task 17: Roles boundary

**Files:**
- Create: `internal/app/roles_vault_integration_test.go`

- [ ] **Step 1:** Test: start an app with `OHS_ROLES=collect` and a counting provider: it accepts an inbound notification and a pull, writes sealed raw objects, makes zero provider calls, and an attempt to read through `ForUser` fails with `ErrCannotOpen`. Start `api,etl`: the ETL processes the objects.
- [ ] **Step 2:** Run, expect PASS (fix wiring in `app.go` if not). **Step 3:** Commit: `test(vault): a collect-only process cannot open stored data`.

### Task 18: Rotation

**Files:**
- Create: `internal/vault/rotate.go`, `internal/vault/rotate_integration_test.go`
- Modify: `cmd/ohs/vault.go`

**Interfaces:**
- Produces: `RotateRoot(ctx, v, pool, newProvider KeyProvider) error` (rewrap every tenant key; record `vault_root`), `RotateTenant(ctx, v, pool, tenant) error`, `RotateUser(ctx, v, pool, tenant, user) error` (new version, old kept), `Reseal(ctx, v, pool, raw, o SealOptions) (SealReport, error)` (rows and objects under old user versions rewritten; old version retired when none is left). Commands `ohs vault rotate root|tenant <id>|user <tenant> <user>` and `ohs vault reseal`. Audit `vault.key_rotated`.

- [ ] **Step 1:** Tests: after `RotateRoot` with a new local key and the old one in previous keys, everything opens; with the old root removed from the provider, a dump taken before the rotation does not open (unwrap fails) while the live database does; Review Focus 2: after `RotateUser`, new rows carry version 2, old rows version 1, and samples list, stats, FHIR export and a share link built across both return the golden answers; `Reseal` leaves only version 2 and retires version 1.
- [ ] **Step 2-4:** FAIL, implement, PASS.
- [ ] **Step 5:** Commit: `feat(vault): key rotation and reseal`.

### Task 19: Equivalence and fuzz

**Files:**
- Create: `test/vault/equivalence_integration_test.go`, `test/fuzz/vault_row_fuzz_test.go`

- [ ] **Step 1:** `TestOutputsMatchGolden`: seed the demo data set on this branch (sealed from the start), call `captureOutputs` (Task 1), compare byte for byte with `testdata/golden/`. A second case seeds through the clear path, runs `Seal`, and compares again.
- [ ] **Step 2:** `FuzzOpenRow` and `FuzzUnmarshalUserKeys`: arbitrary bytes never panic.
- [ ] **Step 3:** Run: `go test -tags integration ./test/vault/` and `go test ./test/fuzz/ -run 'FuzzOpenRow|FuzzUnmarshalUserKeys'`. Expected: PASS. Add both fuzz targets to the CI fuzz matrix.
- [ ] **Step 4:** Commit: `test(vault): outputs unchanged by the vault`.

### Task 20: Benchmarks and docs

**Files:**
- Modify: `bench/` (add `bench/vault/` cases: stats 30 days, ETL throughput, raw write, table sizes), `docs/benchmarks/` (results), `docs/security-model.md`, `docs/hardening.md`, `docs/key-rotation.md`, `docs/backup-and-restore.md`, `docs/self-hosting.md`, `docs/specs/vault.md` (status line: built, link to this plan), `CHANGELOG.md`, `SECURITY.md` (the at-rest line)

- [ ] **Step 1:** Run the bench on the branch point and on this branch on the same machine; record p50, p95, rows per second, sizes. Compare to the Global Constraints targets; a miss goes in the review notes with the measured numbers (it is not a reason to unseal a column).
- [ ] **Step 2:** Docs: the asset table rows for health records and raw payloads become "Sealed per user (vault)"; a "Vault" section copies the spec's threat model table, the roles limit in the spec's words, the backup limit ("a backup taken before an erasure still holds that user's data for whoever has the root key: expire backups, or rotate the root key and destroy the previous version"); backup docs: losing the root key loses all data, back it up apart from the database; self-hosting: variables of Task 5 and the build tags of Task 6.
- [ ] **Step 3:** Run `make guard` (terms file), `go test ./internal/docscheck/`, `make lint`. Expected: PASS.
- [ ] **Step 4:** Commit: `docs: vault security model, operations and benchmarks`.

### Task 21: Release N+1 migration (separate PR, after a release with Task 7)

**Files:**
- Create: `internal/db/migrations/0045_vault_drop_clear.up.sql`, `.down.sql` (down restores the columns empty; data is not recoverable from SQL, say so in a comment)
- Modify: remove the clear-row branches of `openRows` (Tasks 11, 13), the continuous aggregate path of `stats.go`, legacy raw reading in `reader.go` and `sameBytes`

- [ ] **Step 1:** Test: the migration fails with `vault: N rows are still in clear, run "ohs vault seal" first` when a clear row exists (a `DO $$` block counting `sealed IS NULL` per table and raising), and succeeds after `Seal`; then `sealed` becomes NOT NULL, sensitive columns and `ohs_data.quantity_hourly` (view and policy) are dropped.
- [ ] **Step 2:** Run the full suite: `make test integration contract e2e`. Expected: PASS; any query still naming a dropped column fails here, which is the point.
- [ ] **Step 3:** Commit: `feat(db): drop clear health columns (release N+1)`. Open as its own PR, merged only after a release that shipped Task 7 and a note in the CHANGELOG telling operators to run `ohs vault seal` before upgrading.

---

## Spec amendments made with this plan

Found while mapping the code; written into the spec in the same commit:

1. The sealed object header carries a 32 byte digest, `RawDigest(pub, body)`, also stored as `raw_objects.content_hash`. `collect` has no private key, so it recognizes a retried upload by this digest instead of comparing bytes. Guessing it needs the user's public key and the whole payload byte for byte.
2. Columns promoted from `metadata` and stored in clear because SQL filters or ranks on them: `tz`, `summary_date`, `derived`, `synthesized`, plus `source_key` (keyed HMAC) and `worn`.
