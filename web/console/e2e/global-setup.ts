import { execFileSync, spawn, type ChildProcess } from "node:child_process";
import { randomBytes } from "node:crypto";
import { mkdirSync, mkdtempSync, openSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(here, "..", "..", "..");

const ADMIN_PORT = 18090;
const READ_ONLY_PORT = 18091;

/** What global-teardown stops and removes, handed over through the environment. */
const STATE_ENV = "OHS_E2E_STATE";

interface State {
  dir: string;
  pids: number[];
}

function run(bin: string, args: string[], env: NodeJS.ProcessEnv): string {
  try {
    return execFileSync(bin, args, { cwd: repoRoot, env, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
  } catch (e) {
    const err = e as { stderr?: Buffer | string; message: string };
    throw new Error(`${bin} ${args.join(" ")} failed: ${String(err.stderr ?? err.message)}`);
  }
}

function createTenant(bin: string, name: string, env: NodeJS.ProcessEnv): { id: string; key: string } {
  const out = run(bin, ["admin", "tenant", "create", "--name", name], env);
  const id = /\(id ([0-9a-f-]{36})/.exec(out)?.[1];
  const key = out.split("\n").find((line) => /^ohs_sk_\S+$/.test(line.trim()))?.trim();
  if (!id || !key) throw new Error(`could not read the id and the key of tenant ${name} from the output of ohs admin tenant create`);
  return { id, key };
}

async function waitReady(url: string, process: ChildProcess, log: string): Promise<void> {
  const deadline = Date.now() + 60_000;
  while (Date.now() < deadline) {
    if (process.exitCode !== null) throw new Error(`ohs serve exited with ${String(process.exitCode)}; see ${log}`);
    try {
      if ((await fetch(`${url}/readyz`)).ok) return;
    } catch {
      // not listening yet
    }
    await new Promise((r) => setTimeout(r, 250));
  }
  throw new Error(`${url}/readyz did not answer in 60 s; see ${log}`);
}

export default async function globalSetup(): Promise<void> {
  const database = process.env["OHS_E2E_DATABASE_URL"];
  if (!database) throw new Error("OHS_E2E_DATABASE_URL is required: the URL of an empty Postgres database the run may fill");

  const dir = mkdtempSync(join(tmpdir(), "ohs-e2e-"));
  const state: State = { dir, pids: [] };
  process.env[STATE_ENV] = JSON.stringify(state);
  try {
    const bin = join(dir, "ohs");
    run("go", ["build", "-o", bin, "./cmd/ohs"], process.env);

    const key = randomBytes(32).toString("base64");
    const env: NodeJS.ProcessEnv = {
      ...process.env,
      OHS_DATABASE_URL: database,
      OHS_ENCRYPTION_KEY: key,
      OHS_RAW_STORE: `file://${join(dir, "raw")}`,
      OHS_METRICS: "false",
    };
    mkdirSync(join(dir, "raw"));

    run(bin, ["migrate", "up"], env);
    const tenants = run(bin, ["admin", "tenant", "list"], env)
      .split("\n")
      .filter((line) => line.trim() !== "" && !line.startsWith("ID"));
    if (tenants.length > 0) throw new Error("OHS_E2E_DATABASE_URL must point at an empty database");

    // The first tenant is the instance admin.
    const admin = createTenant(bin, "e2e-admin", env);
    const other = createTenant(bin, "e2e-other", env);
    run(bin, ["seed", "--demo", "--tenant", admin.id], env);

    const serve = async (port: number, mode: string, roles: string): Promise<void> => {
      const url = `http://127.0.0.1:${String(port)}`;
      const log = join(dir, `serve-${String(port)}.log`);
      const fd = openSync(log, "a");
      const child = spawn(bin, ["serve"], {
        cwd: repoRoot,
        env: { ...env, OHS_LISTEN: `127.0.0.1:${String(port)}`, OHS_CONSOLE: mode, OHS_PUBLIC_URL: url, OHS_ROLES: roles },
        stdio: ["ignore", fd, fd],
      });
      if (child.pid !== undefined) state.pids.push(child.pid);
      process.env[STATE_ENV] = JSON.stringify(state);
      await waitReady(url, child, log);
    };
    await serve(ADMIN_PORT, "true", "api,collect,etl");
    await serve(READ_ONLY_PORT, "read-only", "api");

    process.env["OHS_E2E_URL"] = `http://127.0.0.1:${String(ADMIN_PORT)}`;
    process.env["OHS_E2E_RO_URL"] = `http://127.0.0.1:${String(READ_ONLY_PORT)}`;
    process.env["OHS_E2E_ADMIN_KEY"] = admin.key;
    process.env["OHS_E2E_OTHER_TENANT"] = other.id;
  } catch (e) {
    // global-teardown does not run when setup throws.
    const { default: teardown } = await import("./global-teardown");
    await teardown();
    throw e;
  }
}
