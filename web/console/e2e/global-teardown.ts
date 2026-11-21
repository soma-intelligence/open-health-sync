import { rmSync } from "node:fs";

const STATE_ENV = "OHS_E2E_STATE";

interface State {
  dir: string;
  pids: number[];
}

/** Stops both servers and removes the temporary directory. */
export default async function globalTeardown(): Promise<void> {
  const raw = process.env[STATE_ENV];
  if (!raw) return;
  const state = JSON.parse(raw) as State;
  for (const pid of state.pids) {
    try {
      process.kill(pid, "SIGTERM");
    } catch {
      // already gone
    }
  }
  // Let the servers close their files before the directory goes.
  await new Promise((r) => setTimeout(r, 500));
  for (const pid of state.pids) {
    try {
      process.kill(pid, 0);
      process.kill(pid, "SIGKILL");
    } catch {
      // gone
    }
  }
  rmSync(state.dir, { recursive: true, force: true });
  Reflect.deleteProperty(process.env, STATE_ENV);
}
