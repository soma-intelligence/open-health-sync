# What the hosted beta does with data

> **This page describes current practice. It is not legal terms.** It is not a privacy policy, a
> terms of service, a data processing agreement or a business associate agreement, and none of
> those exists yet for the hosted beta. It says what the service does today, so a developer can
> decide what to send it. It can change; the history of this file shows what changed and when.
>
> Operated by: `[OWNER TO FILL: legal entity and address]`
> Contact for privacy, deletion and security questions: `[OWNER TO FILL: contact address]`

The hosted beta at [app.openhealthsync.com](https://app.openhealthsync.com) is the open source
server of this repository, run for developers who would rather not run one. It is free during
the beta. You sign up, get an API key, and call the same [API](api.md) as a self-hosted
instance. Everything below follows from how that server works
([security model](security-model.md)) and from the decisions listed here.

## In short

| Question | Answer today |
|---|---|
| Does it accept real user data? | Not yet. Test data only for now: use demo and synthetic users until data processing terms and the providers' production approvals are in place |
| How long is data kept? | Until you delete it, or delete the user it belongs to. There is no fixed retention period and nothing is deleted on a schedule |
| Is it HIPAA compliant? | No. No business associate agreement (BAA) is offered |
| Is it certified or audited (SOC 2, ISO 27001, HITRUST)? | No. No certification is claimed |
| How is it run? | As a single instance, with no stated uptime commitment |
| Is health data encrypted by the application? | No. Health records and raw payloads are stored in clear; provider tokens and secrets are encrypted (below) |
| Does deletion reach backups? | No. Deletion removes the live rows, raw objects and provider grants. Backups taken before it keep what they hold |

If you need a BAA, a data processing agreement, a certification or a contractual uptime, the
hosted beta is not the right place for that data today. [Self-host](self-hosting.md) the same
server on infrastructure that meets your requirements instead: the API is the same, so code
moves over with a new base URL and a new key.

## What it stores

For each user of your project, the instance stores what you send it and what it pulls for you:

- **Health records**: samples, sleep, workouts, nutrition and daily summaries, in Postgres.
- **Raw payloads**: every provider answer and every device upload, as received, in the raw
  store.
- **Provider connections**: for each cloud source a user connects, the OAuth access and refresh
  tokens the provider issued (for Eight Sleep, which has no OAuth, the account password).
- **Your credentials and settings**: API keys, user tokens, webhook endpoints and their signing
  secrets, consent records.
- **An audit log**: one event per authenticated request, with the credential, the user touched,
  the action and the status. An event never holds a health value, a request body or a query
  string ([audit log](audit-log.md)).
- **Logs**: tenant, user and connection ids, provider names and route patterns. Never keys,
  tokens, OAuth codes or health values.

## What is encrypted and what is in clear

This is the application's own protection, the same as on any instance
([the full table](security-model.md#what-is-protected)):

| Data | How it is stored |
|---|---|
| Health records (samples, sleep, workouts, daily summaries) | In clear in Postgres. Every query filters on the tenant and the user |
| Raw provider and device payloads | Gzip, not encrypted, in the raw store, under a path per tenant and user |
| Provider OAuth access and refresh tokens | AES-256-GCM under the instance's encryption key, bound to the row |
| Provider account passwords (Eight Sleep only) | AES-256-GCM under the instance's encryption key, bound to the row; never logged; wiped on disconnect |
| Webhook signing secrets, and provider keys saved for a project | AES-256-GCM under the instance's encryption key |
| Tenant API keys and user tokens | SHA-256 hash only: a lost one cannot be shown again |
| OAuth `state` values | SHA-256 hash only, single use, 10 minute lifetime |
| Share link files | Encrypted (JWE, AES-256-GCM) under a key held only in the link |

Health data is not encrypted by the application. Encryption at rest of the database and the raw
store is a matter of the hosting underneath:
`[OWNER TO FILL: whether the disks and the bucket of the hosted instance are encrypted at rest, and where the instance is hosted]`.

Tenants are separated in application queries, not by Postgres row-level security. Whoever
operates the instance can read the database and the raw store, and so can read every project's
data: nothing in the application protects data from the operator.

The hosted instance is reached over HTTPS only.

## How long data is kept

Until you delete it. There is no fixed retention period: the instance does not delete health
records or raw payloads on a schedule, and it does not delete a project's data because the
project is idle. During the beta the service may change or stop; this page makes no promise
about notice. Keep your own copy of anything you cannot lose
([exports](exports.md), [user archive](exports.md#complete-user-archive-data-portability)).

## Deleting data

Deletion is done by you, the developer, with your project's API key. A user of your app has no
account on the hosted beta: they ask you, and you make the call. If you cannot, write to the
contact above.

| To delete | Call | What it removes |
|---|---|---|
| One user, entirely | `DELETE /v1/users/{id}` | The user's tokens stop working at once. A background job then revokes the provider grants, deletes every raw object of the user, every row of the user in the health and sync tables, their tokens and unsent webhook deliveries, and the user itself. `GET /v1/deletions/{job_id}` says when it is done ([API](api.md#users)) |
| One provider's data for a user | `DELETE /v1/users/{id}/connections/{provider}?delete_data=true`, or `DELETE /v1/users/{id}/data/{provider}` | The connection and what came from that provider ([API](api.md#erasing-one-providers-data)) |
| A whole project or account | `[OWNER TO FILL: how a developer deletes a project or an account on the hosted beta]` | |

What deletion does not reach:

- **Backups.** Backups taken before the deletion keep what they hold until they expire.
  `[OWNER TO FILL: how often the hosted instance is backed up and how long a backup is kept]`
- **Provider grants, sometimes.** Revoking the grant at the provider is best effort: if the
  provider is unreachable, the data is still deleted here but the grant may stay valid there
  until the user removes it in the provider's own settings.
- **Copies outside the instance.** Anything you already read or exported, what your webhook
  receiver stored, and what a reader of a share link downloaded.
- **The audit log.** The events about a deleted user stay, with ids only and no health value,
  as proof the erasure happened (`user.erased`).

## Not HIPAA compliant, not certified

The hosted beta is not HIPAA compliant and no BAA is offered, so a covered entity or a business
associate must not put protected health information on it. No SOC 2, ISO 27001, HITRUST or other
certification is claimed, and the code has had no external audit. Whether your use of it meets
GDPR or another law is not something this page settles: there is no data processing agreement to
sign yet.

Open Health Sync is not a medical device. It checks that values are plausible, not that they are
medically correct.

## One instance

The hosted beta runs as a single instance of the server. There is no stated uptime commitment
and no second region: if that instance is down, the API is down, and provider notifications sent
meanwhile are caught up by the scheduled pulls when it comes back
([background sync](background-sync.md)).

## Security problems

Report a vulnerability privately, as [SECURITY.md](../SECURITY.md) says. Test only against an
instance you run yourself, not against the hosted one.

## Not written yet

These exist for neither the hosted beta nor this page, and the owner has to supply them:

- `[OWNER TO FILL: legal entity that operates the service, and its address]`
- `[OWNER TO FILL: contact address for privacy and deletion requests]`
- `[OWNER TO FILL: hosting provider and region, and the list of sub-processors]`
- `[OWNER TO FILL: backup schedule and backup retention]`
- `[OWNER TO FILL: how to delete a project or an account]`
- Terms of service, a privacy policy and a data processing agreement.
