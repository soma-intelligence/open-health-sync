# Menstrual cycles

`GET /v1/users/{id}/menstrual-cycles` lists a user's menstrual cycles, derived from the menstrual
flow the user's sources recorded. The cycles are computed at read time from the stored
`menstrual_flow` category samples: there is no table of cycles, so the answer always agrees with
the samples, and a deleted sample (a provider deletion, a user erasure) is gone from the next
answer.

The route restates what was recorded, nothing more. It does not predict the next period, the
fertile window or ovulation, and it does not score or judge a cycle.

## Route

| Route | Answers |
|---|---|
| `GET /v1/users/{id}/menstrual-cycles` | one page of cycles, oldest first |

It takes a tenant key, like every read. A user of another tenant, or a user being deleted, is a
404. The MCP tool `get_menstrual_cycles` returns the same records (see [mcp.md](mcp.md)).

| Parameter | Meaning |
|---|---|
| `start`, `end` | RFC 3339 or `YYYY-MM-DD`, compared as dates: a cycle is in the window when `start <= start_date < end` |
| `cursor` | the `next_cursor` of the previous page |
| `limit` | cycles per page, 100 by default, at most 1000 |

```sh
curl -s "$OHS/v1/users/$USER_ID/menstrual-cycles?start=2026-01-01" -H "Authorization: Bearer $KEY"
```

```json
{
  "data": [
    {
      "start_date": "2026-01-02",
      "period_end_date": "2026-01-04",
      "period_length_days": 3,
      "end_date": "2026-01-29",
      "cycle_length_days": 28,
      "providers": ["apple_health", "health_connect"],
      "flow_days": [
        {"date": "2026-01-02", "flow": "heavy", "providers": ["apple_health", "health_connect"]},
        {"date": "2026-01-03", "flow": "medium", "providers": ["apple_health"]},
        {"date": "2026-01-04", "flow": "light", "providers": ["apple_health"]}
      ]
    },
    {
      "start_date": "2026-01-30",
      "period_end_date": "2026-02-03",
      "period_length_days": 5,
      "end_date": null,
      "cycle_length_days": null,
      "providers": ["apple_health"],
      "flow_days": [
        {"date": "2026-01-30", "flow": "medium", "providers": ["apple_health"]},
        {"date": "2026-01-31", "flow": "light", "providers": ["apple_health"]},
        {"date": "2026-02-03", "flow": "unspecified", "providers": ["apple_health"]}
      ]
    }
  ],
  "next_cursor": null
}
```

## Rules

1. **Flow samples.** Only category samples of type `menstrual_flow` are read, from every
   provider. A sample whose `value_label` is `none` (the user logged no flow) is ignored.
   `unspecified`, `light`, `medium` and `heavy` keep their label; a sample with no label, or a
   label outside that list, is flow of unknown amount and reads as `unspecified`. Other category
   types are never read: `intermenstrual_bleeding` (spotting between periods), ovulation tests,
   cervical mucus and sexual activity neither start nor extend a period.
2. **Flow days.** A sample belongs to the local calendar day of its `start_date`, in the IANA
   zone of its `timezone` metadata (Apple Health sets it from `HKTimeZone`, Health Connect from
   the record's zone offset when it is a whole number of hours), or UTC when the sample has none or names a zone the server does
   not know. A day with at least one flow sample is a flow day. A day several samples or
   several providers report is one flow day: its `flow` is the heaviest label recorded that day
   (`heavy` > `medium` > `light` > `unspecified`) and its `providers` lists each provider once,
   sorted.
3. **Periods.** A period is a run of flow days in which two consecutive flow days are at most
   3 days apart: up to 2 days without recorded flow inside a period do not split it. A day or two
   without a log is common in the middle of a period, and no cycle is that short, so such a gap
   is never a new period. A third day without flow ends the period. `period_length_days` counts
   from the first flow day to the last, both included, gap days included; `flow_days` lists only
   the days with flow.
4. **Cycles.** A cycle starts on the first day of a period (`start_date`) and ends the day before
   the first day of the next period (`end_date`). `cycle_length_days` is the number of days from
   one period's first day to the next one's. The latest cycle is open: no later period is
   recorded, so `end_date` and `cycle_length_days` are null. It closes on its own once flow of a
   new period is stored. `providers` lists the providers of the period's flow days.

## Paging and windows

Cycles are ordered by `start_date`. `start` and `end` select cycles by their start day; a cycle
at the edge of the window still gets its full period and its length, read from the data past
`end`. A window that starts inside a period does not cut it: that period started before the
window, so its cycle is not in the window, and the next one is. The cursor holds the start day of
the page's last cycle, and the next page continues the day after it, with the same rules.

## Limits

One call reads at most 50000 flow samples from its lower bound on (the samples of the 4 days
before it, read to place that bound inside or outside a period, do not count). Past that, the
page ends early: it holds the cycles that are complete and a `next_cursor`, so a page can hold
fewer than `limit` cycles and still not be the last. A single cycle whose period and the next
period's first day take more than 50000 samples is refused with a 400; real data never comes
close.

The work of a call is one index range scan over the user's `menstrual_flow` samples, read in
chunks of 1000 and stopped as soon as the page is known.

## Sources

| Provider | Flow | Mapping |
|---|---|---|
| `apple_health` | `HKCategoryTypeIdentifierMenstrualFlow` | value 1 to 5: `unspecified`, `light`, `medium`, `heavy`, `none` |
| `health_connect` | `MenstruationFlow` | `flow` `light`, `medium`, `heavy`; `unknown` is `unspecified` |

Both connectors also store intermenstrual bleeding (`HKCategoryTypeIdentifierIntermenstrualBleeding`,
`IntermenstrualBleeding`) as `intermenstrual_bleeding`, readable with
`GET /v1/users/{id}/category-samples?type=intermenstrual_bleeding`. Health Connect's
`MenstruationPeriod` record (a span with no flow per day) is not stored.
