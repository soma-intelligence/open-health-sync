# Writing a connector

A connector teaches Open Health Sync one provider. It lives in `connectors/<slug>/`, implements
the contract in `internal/connector`, and registers itself in `init()`. The core is not touched.

## The contract

```go
// Every connector.
type Connector interface {
    Slug() string            // "withings", "apple_health"
    Enabled(cfg Config) bool // true when its keys are set
    // Map turns one raw payload into canonical records. Pure, no I/O.
    Map(raw RawRecord) (model.MapResult, error)
}
```

On top of it, implement the interfaces that match how the data arrives. A connector can be
several at once; the registry checks which ones it implements.

| Interface | Methods | For |
|---|---|---|
| `OAuthConnector` | `AuthorizeURL`, `Exchange`, `Refresh`, `Fetch` | providers the server pulls from |
| `PushConnector` | `HandleInbound` | providers that send notifications |
| `StateExchanger` | `ExchangeState` | providers that require PKCE: the callback hands the OAuth state to the exchange, so the code verifier can be derived from it (an HMAC under the client secret) instead of stored |
| `NotifiedFetcher` | `FetchNotified` | pull only what a notification names (its `InboundEvent.DataType`), to spare the provider's rate limit |
| `DeviceConnector` | `ParseUpload` | on-device stores uploaded by an SDK |
| `KeyedConnector` | `RequiredKeys` | operator keys: the connector is off until each is set |
| `OptionalKeyed` | `OptionalKeys` | keys it reads but runs without (a webhook secret, a region), listed in the [console](console.md) |
| `Configurable` | `Configure` | receives its keys at startup, and again when the console saves new ones: validate before changing anything, and be safe to call while the connector runs |
| `PostConnectHook` | `AfterConnect` | work after a user connects, such as subscribing to notifications |
| `DataTypes` | `DataTypes` | only when `Map` builds its quantity names at run time (self reports): what it delivers. Every other connector's list is read from its source |

Rules that matter:

- **`Map` is pure and deterministic.** The ETL may map the same raw object again after a fix, and
  content hashes must come out the same, or duplicates appear.
- **Raw first.** `Fetch` and `ParseUpload` hand provider payloads to a `RawSink` (or, for
  uploads, the handler stores the original bytes). They never write canonical tables. The ETL
  calls `Map` later on each stored line. One raw object holds one data type; `RawRecord.DataType`
  tells `Map` which.
- **Set `Metadata["timezone"]`** (an IANA name) on every record when the provider gives one. The
  ETL derives the user's local day from it and falls back to UTC.
- **Use canonical names and units.** Quantity names come from [data-model.md](data-model.md).
  Unknown names still land, unconverted. Give a known name a known unit, or the record is parked
  as a unit error.
- **Content hash** each record from the fields that identify it (user, provider, type, time,
  value), never from a field that changes between re-sends. `model.ComputeContentHash` and
  `model.CanonicalFloat` do the work.
- **Return `connector.ErrNeedsReauth`** from `Refresh` or `Fetch` when the provider rejects the
  grant, and `connector.RateLimited(headers, now)` on HTTP 429, so the scheduler backs off
  without counting a failed attempt.
- **Never log tokens or health values.**

## Sync policy

A pulled connector is backstop-pulled every 6 hours while its notifications arrive and every hour
otherwise; each pull re-reads the hour before the previous window, and a new connection is
backfilled over 30 days in 7 day jobs. When the provider needs other values, implement
`connector.SyncPolicied`:

```go
func (c *Connector) SyncPolicy() connector.SyncPolicy {
	return connector.SyncPolicy{
		PullEveryWithoutPush: 3 * time.Hour,  // the API is rate limited
		Lookback:             72 * time.Hour, // readings reach the provider days late
		Backfill:             90 * 24 * time.Hour,
		Reconcile:            14 * 24 * time.Hour, // the 00:00 UTC pull reaches back 14 days
	}
}
```

A zero field keeps its default. Intervals must divide a day (15 minutes to 24 hours). A value out
of bounds falls back to the default, and `connectors/all` has a test that fails for such a value.
Say why in the method's comment: rate limits, how late data arrives, how much history the provider
keeps. [Background sync](background-sync.md#sync-policy-per-provider) lists every provider's.

## Deleted records

When the provider says one of its records is gone (a sleep the user removed, a deleted
workout), `Map` returns a `model.Deletion{ExternalID: <provider record id>}` in
`MapResult.Deletions` instead of nothing. The ETL then, in the job's transaction and for the
job's tenant and user only:

- removes the sleep sessions (with their phases), workouts (with route points and track
  summaries) and nutrition logs stored with that provider and `ExternalID`;
- removes the quantity and category samples of the record: `ExternalID` equal to the id or
  `<id>_<suffix>` (one record giving several samples), whenever they start, and whether or not
  a sleep or workout of that id is stored (a record that only gave samples is deleted the same
  way). The lookup uses an index on provider and `ExternalID`, so it does not scan the table;
- when the deletion names `ClearSummaryFields` (daily summary columns such as
  `recovery_score`, `resting_hr`), sets those figures to NULL in the provider's daily summaries
  of the days its removed samples name in `Metadata["summary_date"]`
  (`model.MetadataSummaryDate`, `YYYY-MM-DD`), and deletes a summary left without any figure.
  Only summary figure names are accepted; any other name is ignored;
- records a tombstone in `ohs_data.deleted_records`. A record whose id is tombstoned is never
  written again, so a re-pull, an old raw object processed again, or a deletion that arrives
  before the record cannot bring it back. Applying a deletion twice is harmless.

So give every record you may later delete a stable `ExternalID`, and its derived samples the id
or `<id>_<suffix>`. A deletion is final for that id: do not emit one for a record that can come
back. When a record also fills figures of your daily summary, stamp the summary's day on its
samples (`Metadata["summary_date"]`), set `DailySummary.RecordID` to the record id when the
summary carries that record's figures only (a tombstoned record's summary is then not written
again), and list the figures in the deletion's `ClearSummaryFields`.

### Records whose start moves

A Replaceable record is updated in place when its copy comes back under the same content hash
and start. A sleep session or workout whose start moved supersedes the stored copy of the same
`ExternalID` automatically. A quantity sample does so only when it sets `SupersedeMoved`: the
writer then deletes the stored samples of the same provider, `ExternalID` and quantity type
under another start before writing it. Set it only when the `ExternalID` names one figure of
one record (`<record id>_<quantity>`), never on samples that share an `ExternalID` across starts
(a day's heart rate series under one id), or all but one of them would be deleted.

A deletion that arrives as a notification follows the raw-first rule like everything else:
`HandleInbound` returns an `InboundEvent` with `Lines` set (usually the notification body,
verbatim) and a data type of your choosing. The core stores those lines as a raw object for each
connection of the provider user and queues it for the ETL, and `Map` turns the stored line into
the `Deletion`. `Map` stays pure, and the raw store keeps the evidence.

## A minimal device connector

`connectors/example/` holds a complete, compiling connector for a Bluetooth scale whose SDK
uploads one weigh-in per line. It sits behind the `docs` build tag so it never ships; check it
with:

```sh
go vet -tags docs ./connectors/example/
go test -tags docs ./connectors/example/
```

The heart of it:

```go
const Slug = "example_scale"

func init() { connector.Register(New()) }

type Connector struct{}

func (c *Connector) Slug() string                  { return Slug }
func (c *Connector) Enabled(connector.Config) bool { return true } // no keys needed

// ParseUpload rejects what the ETL could never read. The upload handler keeps the
// original bytes, so nothing is emitted.
func (c *Connector) ParseUpload(_ connector.UploadMeta, body io.Reader, _ connector.RawSink) error {
    // every non-blank line must be a JSON object; at least one line
}

// Map: one stored line in, canonical records out.
func (c *Connector) Map(raw connector.RawRecord) (model.MapResult, error) {
    // decode raw.Fields into a weighIn, then:
    return model.MapResult{QuantitySamples: []model.QuantitySample{{
        UserID: raw.UserID, Provider: Slug, QuantityType: "body_mass",
        Value: w.Kilograms, Unit: "kg", StartDate: at, EndDate: at,
        Metadata:    map[string]any{"timezone": w.Timezone},
        ContentHash: model.ComputeContentHash(/* user, provider, type, time, value */),
    }}}, nil
}
```

Read `connectors/example/example.go` for the full version.

## Register it

1. Put the package under `connectors/<slug>/` with a package comment.
2. Add a blank import to `connectors/all/all.go` so the binary links it:
   `_ "github.com/soma-intelligence/open-health-sync/connectors/<slug>"`.
3. For a keyed connector, implement `RequiredKeys` (for example `CLIENT_ID`, `CLIENT_SECRET`);
   the operator sets `OHS_<SLUG>_CLIENT_ID` and so on. Without them the connector stays off
   and `ohs doctor` says why. For an OAuth connector `ohs doctor` also prints the callback URL
   to register with the provider.
4. Document it in `docs/connectors/<slug>.md`: where to get keys, the callback URL, scopes.
5. Run `go run ./internal/connector/cmd/gendatatypes`. It reads which record kinds and
   quantity names your mapper writes, for the `data_types` of `GET /v1/providers` and the table
   in [connectors/README.md](connectors/README.md#what-each-connector-delivers). A test fails
   until the generated files are current. A canonical quantity name your code mentions without
   writing it as a quantity (a metadata key) goes in the generator's `notEmitted` list.

## Test it with fixtures

Every connector ships fixture tests: raw lines in, expected canonical records out.

1. Record real provider responses (or upload lines) into `connectors/<slug>/testdata/`, with
   personal values replaced by invented ones.
2. Feed each line to `Map` and compare the result with the records you expect, written by hand
   or as golden JSON. Check the quantity name, the unit, the timestamps, the `timezone` metadata
   and that the content hash is stable across two runs. `connectors/example/example_test.go`
   shows the shape; `connectors/applehealth/` and `connectors/withings/` are full examples.
3. For a pulling connector, test `AuthorizeURL`, `Exchange`, `Refresh` and `Fetch` against an
   `httptest` fake of the provider API, as `connectors/withings/withingsfake` does. No test may
   call a real provider.
4. Add the unit cases: an unknown type gives an empty result and no error, a malformed line
   gives an error, a missing value is skipped.
5. Add the connector to the `Providers` list in `test/recorded/recorded.go`. When you have an
   account, [record it](connectors/recording.md): CI then replays the real answers on every
   change.

Run `go test ./connectors/<slug>/` and `make guard` before opening a pull request.
