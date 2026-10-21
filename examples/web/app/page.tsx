// SPDX-License-Identifier: AGPL-3.0-only
//
// Step 1: create an end user and list the tenant's users.

import Link from "next/link";
import { randomBytes } from "node:crypto";
import type { User } from "@open-health-sync/server";
import { ActionForm } from "../components/action-form.tsx";
import { Steps } from "../components/steps.tsx";
import { Empty, ErrorNotice, Icon, Notice, PageHead, Route, Routes, formatDateTime } from "../components/ui.tsx";
import { readConfig } from "../lib/config.ts";
import { attempt, tenantClient } from "../lib/ohs.ts";
import { createUser } from "./actions.ts";

export const dynamic = "force-dynamic";

async function allUsers(): Promise<User[]> {
  const users: User[] = [];
  for await (const user of tenantClient().iterUsers({ limit: 100 })) users.push(user);
  // The API lists oldest first; show the newest first.
  return users.reverse();
}

export default async function UsersPage() {
  const { config, missing } = readConfig();
  return (
    <main className="view">
      <Steps current={1} />
      <PageHead
        step="Step 01 · Users"
        title="Create an end user"
        sub="Every record belongs to one end user of your app. Create one under your own reference for that person (external_ref), then open it to send data in."
        routes={
          <Routes>
            <Route method="POST" path="/v1/users" />
            <Route method="GET" path="/v1/users" />
          </Routes>
        }
      />
      {config ? <Content /> : <Setup missing={missing} />}
    </main>
  );
}

function Setup({ missing }: { missing: string[] }) {
  return (
    <Notice kind="warn">
      <p>
        <strong>Set {missing.join(" and ")}</strong> in <code>.env.local</code>, then restart <code>npm run dev</code>.
      </p>
      <p className="small">
        Copy <code>.env.example</code>, put your instance URL in <code>OHS_URL</code> and a tenant key in{" "}
        <code>OHS_API_KEY</code>: <code>ohs admin tenant create --name web-example</code> prints one, and so does the
        admin console of your instance.
      </p>
    </Notice>
  );
}

async function Content() {
  const ohs = tenantClient();
  const [health, tenant, users] = await Promise.all([
    attempt(() => ohs.healthz()),
    attempt(() => ohs.getTenant()),
    attempt(allUsers),
  ]);
  const { config } = readConfig();
  return (
    <>
      {!health.ok && <ErrorNotice what="Reaching the instance" error={health.error} />}
      {!tenant.ok && health.ok && <ErrorNotice what="Checking the API key" error={tenant.error} />}
      <div className="grid-2 users-grid">
        <div>
          <section className="card">
            <div className="card-head">
              <h2>New user</h2>
              <Route method="POST" path="/v1/users" />
            </div>
            <div className="card-body">
              <ActionForm action={createUser} submit="Create user" pending="Creating..." primary>
                <div className="field">
                  <label htmlFor="external_ref">Your reference for this person</label>
                  <input
                    id="external_ref"
                    name="external_ref"
                    className="mono"
                    required
                    maxLength={200}
                    defaultValue={`customer-${randomBytes(2).toString("hex")}`}
                    autoComplete="off"
                    spellCheck={false}
                  />
                  <span className="help">
                    Unique per tenant, usually the id in your own database. Sent with an Idempotency-Key, so a double
                    submit creates one user.
                  </span>
                </div>
              </ActionForm>
            </div>
          </section>
          <section className="card">
            <div className="card-head">
              <h2>Connection</h2>
              {health.ok && tenant.ok ? <span className="pill ok">Connected</span> : <span className="pill bad">Not connected</span>}
            </div>
            <div className="card-body">
              <dl className="facts compact">
                <dt>Instance</dt>
                <dd>
                  <code>{config?.ohsUrl}</code>
                </dd>
                <dt>Tenant</dt>
                <dd>{tenant.ok ? tenant.value.name : "unknown"}</dd>
                <dt>API key</dt>
                <dd>
                  {tenant.ok ? <code>{tenant.value.key.prefix}...</code> : "set"}{" "}
                  <span className="dim">server side only</span>
                </dd>
                <dt>App URL</dt>
                <dd>
                  <code>{config?.appUrl}</code>
                </dd>
              </dl>
            </div>
          </section>
        </div>
        <section className="card">
          <div className="card-head">
            <h2>Users</h2>
            <Route method="GET" path="/v1/users" />
          </div>
          {!users.ok ? (
            <div className="card-body">
              <ErrorNotice what="Listing users" error={users.error} />
            </div>
          ) : users.value.length === 0 ? (
            <Empty title="No user yet">Create one on the left. It appears here, newest first.</Empty>
          ) : (
            <div className="scroll">
              <table>
                <thead>
                  <tr>
                    <th>Reference</th>
                    <th>Created</th>
                    <th>Status</th>
                    <th aria-label="Open" />
                  </tr>
                </thead>
                <tbody>
                  {users.value.slice(0, 50).map((user) => (
                    <tr key={user.id}>
                      <td>
                        <div className="cell-2">
                          <Link href={`/users/${user.id}`}>{user.external_ref ?? "(no reference)"}</Link>
                          <code className="sub">{user.id}</code>
                        </div>
                      </td>
                      <td className="nowrap dim">{formatDateTime(user.created_at)}</td>
                      <td>
                        <span className={`pill ${user.status === "active" ? "ok" : "warn"}`}>{user.status}</span>
                      </td>
                      <td className="num">
                        <Link className="button small" href={`/users/${user.id}`} aria-label={`Open ${user.external_ref ?? user.id}`}>
                          Open <Icon name="arrow" />
                        </Link>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {users.ok && users.value.length > 50 && (
            <div className="card-foot">Showing the 50 newest of {users.value.length} users.</div>
          )}
        </section>
      </div>
    </>
  );
}
