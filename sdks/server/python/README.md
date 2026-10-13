# Open Health Sync server SDK for Python

A client of the Open Health Sync API for your backend, on the Python standard library alone
(`urllib.request`, `json`, `hmac`), Python 3.9 or later. Licensed AGPL-3.0-only, like the rest
of the repository. Not published to PyPI.

- `open_health_sync/client.py` and `open_health_sync/models.py` are **generated** from
  `api/openapi.yaml` by `internal/sdkgen`. Do not edit them: change the OpenAPI document and run
  `make sdk` from the repository root. A Go test fails when they are out of date.
- `open_health_sync/webhooks.py` (webhook signature verification) is written by hand.

## Install

From a clone of the repository:

```sh
pip install /path/to/open-health-sync/sdks/server/python
```

pip can also install it straight from the Git repository, with
`#subdirectory=sdks/server/python` after the repository URL. Or copy the `open_health_sync`
directory into your project.

## Use

```python
import os
from open_health_sync import OpenHealthSync, OhsError

ohs = OpenHealthSync("https://health.example.com", os.environ["OHS_API_KEY"])

for user in ohs.iter_users():
    print(user["id"], user["external_ref"])

consent = ohs.start_connection(user_id, "withings", {"redirect_uri": "https://app.example.com/done"})

for day in ohs.iter_daily(user_id, start="2026-01-01", end="2026-02-01"):
    print(day["date"], day.get("step_count"))
```

Every operation of the API is a method named after its `operationId` in snake_case; every
cursor list also has an `iter_...` method that follows `next_cursor`. Responses are plain
dicts, typed as TypedDicts in `models.py`. Errors are raised as `OhsError` (`status`, `title`,
`detail`, `request_id`). See [docs/server-sdks.md](../../../docs/server-sdks.md).

## Test

```sh
python -m unittest discover -s tests
```
