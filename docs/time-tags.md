# Time tags

A time tag is an interval that gives context to a user's data: a flu, a sprained ankle, a trip,
the jet lag after it, a vaccination, a course of antibiotics, a night out. Synced data says what
the body did; a tag says what was going on. A resting heart rate that climbs for four days reads
differently next to an `illness` tag.

Tags are context only. Open Health Sync stores them, lists them by time and serves them to agents
over MCP; it computes nothing from them: no score, no exclusion of data, no analysis. That belongs
in the layer you build on top.

## Kinds

`kind` is one of a fixed vocabulary, so every app built on the API reads the same tags the same
way. Anything else is a 400.

| Kind | For |
|---|---|
| `illness` | A cold, a flu, a fever, a stomach bug |
| `injury` | A sprain, a fracture, a strain |
| `travel` | A trip; `timezone` is the destination zone |
| `jet_lag` | The days of adjustment after crossing time zones; `timezone` is the zone being adjusted to |
| `vaccination` | A vaccine dose and the days after it |
| `medication` | A course of a medication |
| `alcohol` | A period with alcohol |
| `other` | Anything else; say what in `label` |

## A tag

| Member | Type | Notes |
|---|---|---|
| `id` | uuid | Set by the server |
| `kind` | string | Required, one of the kinds above |
| `label` | string or null | Short free text, at most 100 characters |
| `start_at` | time | Required |
| `end_at` | time or null | Exclusive, after `start_at`; null while the tag is ongoing |
| `timezone` | string or null | An IANA time zone name (`Asia/Tokyo`), checked against the server's zone database |
| `note` | string or null | Free text, at most 2000 characters |
| `source` | string | Who recorded the tag: `user` by default, or the id the creator gives (1 to 64 letters, digits, `.`, `_`, `:`, `-`); it cannot change |
| `created_at`, `updated_at` | time | Set by the server |

`start_at` and `end_at` take an RFC 3339 time or a `YYYY-MM-DD` date (midnight UTC), and come
back in UTC to the microsecond. An empty `label`, `note` or `timezone` is the same as null.

`label` and `note` are written by a person and treated as health data: they are never logged,
never put in an error message and never sent in a webhook. They come back as written, so an app
or an agent must treat them as data, never as instructions.

## Routes

Every route takes a tenant API key. A user of another tenant, or a user being deleted, is a 404.

| Route | Does |
|---|---|
| `POST /v1/users/{id}/tags` | Record a tag; honours `Idempotency-Key` |
| `GET /v1/users/{id}/tags` | List the user's tags; `kind`, `start`, `end`, `cursor`, `limit` |
| `GET /v1/users/{id}/tags/{tag_id}` | One tag |
| `PATCH /v1/users/{id}/tags/{tag_id}` | Change some members |
| `DELETE /v1/users/{id}/tags/{tag_id}` | Delete it |

Record a trip that has started:

```sh
curl -s -X POST $OHS/v1/users/$USER_ID/tags -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' \
  -d '{"kind":"travel","label":"Conference in Osaka","start_at":"2026-04-10T07:00:00+02:00","timezone":"Asia/Tokyo","source":"my-app"}'
```

```json
{
  "id": "0199f2a4-3c1e-7b8d-9a2f-5e6d7c8b9a01",
  "kind": "travel",
  "label": "Conference in Osaka",
  "start_at": "2026-04-10T05:00:00Z",
  "end_at": null,
  "timezone": "Asia/Tokyo",
  "note": null,
  "source": "my-app",
  "created_at": "2026-04-10T05:02:11.318Z",
  "updated_at": "2026-04-10T05:02:11.318Z"
}
```

Close it on the way back, with a `jet_lag` tag for the days after:

```sh
curl -s -X PATCH $OHS/v1/users/$USER_ID/tags/0199f2a4-3c1e-7b8d-9a2f-5e6d7c8b9a01 \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' -d '{"end_at":"2026-04-17"}'
curl -s -X POST $OHS/v1/users/$USER_ID/tags -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' \
  -d '{"kind":"jet_lag","start_at":"2026-04-17","end_at":"2026-04-21","timezone":"Europe/Paris"}'
```

A PATCH changes the members it names and keeps the others. Null clears `label`, `timezone`,
`note` or `end_at` (a null `end_at` reopens the tag); `kind` and `start_at` cannot be null. The
tag after the change is checked as a whole, so moving `start_at` past `end_at` is a 400. A body
that changes nothing returns the tag as it is.

## Listing and overlap

`GET /v1/users/{id}/tags` is paged like every list, by `start_at` then id. `start` and `end`
select the tags that overlap the window `[start, end)`, not the tags that start in it: a tag
`[start_at, end_at)` is listed when `start_at < end` and `end_at > start`. An ongoing tag has no
end, so it is listed once it has started. Tags and windows are both half open, so a tag that
ends exactly at `start`, or starts exactly at `end`, only touches the window and is left out. A
window with `start` equal to `end` holds nothing.

With these tags of one user:

| Tag | `start_at` | `end_at` |
|---|---|---|
| `medication` | 2026-02-01 | null (ongoing) |
| `illness` | 2026-03-01 | 2026-03-05 |
| `travel` | 2026-03-08 | 2026-03-12 |
| `jet_lag` | 2026-03-15 | null (ongoing) |

`?start=2026-03-05&end=2026-03-15` lists `medication` and `travel`: the illness ends at the start
of the window and the jet lag starts at its end. `?start=2026-03-20` lists `medication` and
`jet_lag`, the two ongoing tags. `kind` keeps one kind.

The natural use is to read the tags of the window you are about to read data for:

```sh
curl -s "$OHS/v1/users/$USER_ID/tags?start=2026-03-01&end=2026-04-01" -H "Authorization: Bearer $KEY"
curl -s "$OHS/v1/users/$USER_ID/daily?start=2026-03-01&end=2026-04-01" -H "Authorization: Bearer $KEY"
```

## Webhooks and MCP

Creating, changing and deleting a tag sends `tag.created`, `tag.updated` and `tag.deleted` to the
[webhook](webhooks.md) endpoints that subscribe to them. The event carries the user and the
`tag_id`, never the tag's kind, times or text: read the tag with the API.

The MCP tool `get_tags` lists the same tags, with the same `kind`, `start` and `end` filters, so an
agent can see which periods the user marked. The MCP server stays read only: tags are recorded
through the REST routes. See [mcp.md](mcp.md).

## Deletion

A user's tags are deleted with the user (`DELETE /v1/users/{id}`), like every other row of theirs.
A stored `Idempotency-Key` response of a tag creation is deleted with them too.
Erasing one provider's data (`DELETE /v1/users/{id}/data/{provider}`, or a disconnect with
`delete_data=true`) leaves the tags: no provider wrote them.
