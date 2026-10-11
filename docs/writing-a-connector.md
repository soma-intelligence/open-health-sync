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
| `DeviceConnector` | `ParseUpload` | on-device stores uploaded by an SDK |
| `KeyedConnector` | `RequiredKeys` | operator keys: the connector is off until each is set |
| `Configurable` | `Configure` | receives its keys once at startup |
| `PostConnectHook` | `AfterConnect` | work after a user connects, such as subscribing to notifications |

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

## Deleted records

When the provider says one of its records is gone (a sleep the user removed, a deleted
workout), `Map` returns a `model.Deletion{ExternalID: <provider record id>}` in
`MapResult.Deletions` instead of nothing. The ETL then, in the job's transaction and for the
job's tenant and user only:

- removes the sleep sessions (with their phases), workouts (with route points and track
  summaries), category samples and nutrition logs stored with that provider and `ExternalID`;
- removes the quantity samples derived from the record: `ExternalID` equal to the id or
  `<id>_<suffix>` (one record giving several samples), starting within a day of the removed
  sleep or workout;
- records a tombstone in `ohs_data.deleted_records`. A record whose id is tombstoned is never
  written again, so a re-pull, an old raw object processed again, or a deletion that arrives
  before the record cannot bring it back. Applying a deletion twice is harmless.

So give every record you may later delete a stable `ExternalID`, and its derived samples the id
or `<id>_<suffix>`. A deletion is final for that id: do not emit one for a record that can come
back.

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

Run `go test ./connectors/<slug>/` and `make guard` before opening a pull request.
