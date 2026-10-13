# Security policy

## Reporting a vulnerability

Please report security problems privately and do not open a public issue.

Use GitHub Private Vulnerability Reporting:
**[report a vulnerability](https://github.com/soma-intelligence/open-health-sync/security/advisories/new)**
(the "Report a vulnerability" button on the repository's Security tab). Only the maintainers
see the report, and we can work on a fix and an advisory with you there.

Include what you found, how to reproduce it, the version or commit, and what an attacker gains.
Please do not include real health data: use invented values.

What to expect (these are goals):

- an acknowledgement within 3 working days;
- an assessment and a plan within 14 days;
- a fix and a coordinated disclosure date agreed with you, through a GitHub security advisory.
  We credit reporters who want it.

Please give us a reasonable time to ship a fix before you disclose, and test only against
instances you run yourself.

## Supported versions

| Version | Supported |
|---|---|
| 0.1.x (latest release) | yes |
| `main` | best effort |
| anything older | no |

While the version is 0.x, fixes ship in a new release only; there are no backports. Upgrade to
the latest release to get them.

## Scope

In scope: the `ohs` binary, its API, the container image and the compose files in this
repository. Examples: authentication or tenant isolation bypass, reading or writing another
user's data, secret exposure in logs or responses, injection, request forgery on the OAuth
callback or the inbound routes, denial of service from a single unauthenticated request.

Out of scope: weaknesses in a third party provider's API, in your reverse proxy or database
configuration, or an instance running without the settings in
[docs/self-hosting.md](docs/self-hosting.md#operating-notes).

## How the project handles secrets

- API keys (`ohs_sk_`) and user tokens (`ohs_ut_`) are stored as SHA-256 hashes only.
- OAuth tokens are encrypted with AES-256-GCM under `OHS_ENCRYPTION_KEY`, bound to the tenant,
  user and provider. Losing the key loses the stored OAuth tokens; leaking it exposes them.
- Logs never contain tokens, keys or health values.
- Every query on user data filters on `tenant_id` and `user_id`.

Known limits that operators should plan around are listed in
[docs/self-hosting.md](docs/self-hosting.md#operating-notes).
