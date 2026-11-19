import { useState, type FormEvent } from "react";
import { Link, Navigate, useLocation } from "react-router";

import { BASENAME } from "../lib/basename";
import { errorLine } from "../lib/errorText";
import { useSetupState } from "../pages/setup/api";
import { isApiKey } from "../pages/setup/setup";
import "../pages/setup/setup.css";
import { Button } from "../ui/Button";
import { safeReturnPath } from "./returnPath";
import { useSession } from "./SessionProvider";

export function SignInPage() {
  const { status, expired, signIn } = useSession();
  const location = useLocation();
  const [key, setKey] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // "off" (the route is not mounted), "done" and every failure: no link.
  const setup = useSetupState(status === "signed-out");

  if (status === "signed-in") {
    const state = location.state as { from?: unknown } | null;
    return <Navigate to={safeReturnPath(state?.from).slice(BASENAME.length) || "/"} replace />;
  }

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!isApiKey(key)) {
      setError("That is not a tenant API key.");
      return;
    }
    setError(null);
    setBusy(true);
    try {
      await signIn(key);
    } catch (e) {
      // A refused key is cleared; after a network failure the key stays, so the operator only presses Sign in again.
      if (!(e instanceof TypeError)) setKey("");
      setError(errorLine(e, "The server could not be reached."));
    } finally {
      setBusy(false);
    }
  };

  // The same card as the first-run page: both doors into the console look alike.
  return (
    <main className="setup-page">
      <section className="setup-panel">
        <p className="muted">Open Health Sync console</p>
        <h1>Sign in</h1>
        {expired ? (
          <p className="notice-bar" role="status">
            Your session ended, so you were signed out. Sign in again to go back to the page you were on.
          </p>
        ) : null}
        <form
          className="setup-form"
          autoComplete="off"
          onSubmit={(e) => {
            void submit(e);
          }}
        >
          <div className="field">
            <label htmlFor="api-key">Tenant API key</label>
            <input
              id="api-key"
              className="input mono"
              type="password"
              autoComplete="off"
              spellCheck={false}
              value={key}
              onChange={(e) => {
                setKey(e.target.value);
              }}
            />
          </div>
          {error ? (
            <p role="alert" className="form-error">
              {error}
            </p>
          ) : null}
          <Button kind="primary" type="submit" disabled={busy}>
            Sign in
          </Button>
          <p className="muted">The key is checked once. The console then holds a session, never the key.</p>
          {setup.data === "needed" ? <Link to="/setup">First run: create the first tenant</Link> : null}
        </form>
      </section>
    </main>
  );
}
