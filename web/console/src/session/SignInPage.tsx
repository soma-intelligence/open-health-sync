import { useState, type FormEvent } from "react";
import { Link, Navigate, useLocation } from "react-router";

import { ApiError } from "../api/client";
import { BASENAME } from "../lib/basename";
import { useSetupState } from "../pages/setup/api";
import { isApiKey } from "../pages/setup/setup";
import { safeReturnPath } from "./returnPath";
import { useSession } from "./SessionProvider";

export function SignInPage() {
  const { status, signIn } = useSession();
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
      setError(e instanceof ApiError ? e.detail || e.message : "The server could not be reached.");
    } finally {
      setKey("");
      setBusy(false);
    }
  };

  return (
    <main>
      <h1>Sign in</h1>
      <form
        onSubmit={(e) => {
          void submit(e);
        }}
      >
        <label htmlFor="api-key">Tenant API key</label>
        <input
          id="api-key"
          type="password"
          autoComplete="off"
          spellCheck={false}
          value={key}
          onChange={(e) => {
            setKey(e.target.value);
          }}
        />
        {error ? <p role="alert">{error}</p> : null}
        <button type="submit" disabled={busy}>
          Sign in
        </button>
      </form>
      <p>The key is checked once. The console then holds a session, never the key.</p>
      {setup.data === "needed" ? <Link to="/setup">First run: create the first tenant</Link> : null}
    </main>
  );
}
