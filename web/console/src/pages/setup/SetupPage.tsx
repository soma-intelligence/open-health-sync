import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router";

import { ApiError } from "../../api/client";
import { useSession } from "../../session/SessionProvider";
import { Button } from "../../ui/Button";
import { CopyButton } from "../../ui/CopyButton";
import { BrandMark } from "../../ui/Icon";
import { useRunSetup, useSetupState } from "./api";
import { MAX_TENANT_NAME, SetupError } from "./setup";
import "./setup.css";

interface Issued {
  tenantId: string;
  key: string;
}

/** The first-run page: the setup token makes the first tenant, whose key is shown once. */
export function SetupPage() {
  const state = useSetupState();
  const run = useRunSetup();
  const { signIn } = useSession();
  const navigate = useNavigate();
  const [token, setToken] = useState("");
  const [name, setName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [closed, setClosed] = useState(false);
  const [issued, setIssued] = useState<Issued | null>(null);
  const [signing, setSigning] = useState(false);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setError(null);
    try {
      const out = await run.mutateAsync({ token, name });
      // Only this component's state holds the key: the mutation drops it at once.
      setIssued({ tenantId: out.tenant_id, key: out.api_key });
      setToken("");
    } catch (e) {
      if (e instanceof SetupError && e.status === 409) {
        setToken("");
        setClosed(true);
      } else {
        setError(e instanceof SetupError ? e.message : `Setup failed: ${e instanceof Error ? e.message : "unknown error"}`);
      }
    } finally {
      run.reset();
    }
  };

  const signInWithKey = async () => {
    if (!issued) return;
    setSigning(true);
    setError(null);
    try {
      await signIn(issued.key);
      setIssued(null);
      await navigate("/overview");
    } catch (e) {
      setError(e instanceof ApiError ? e.detail || e.message : "The server could not be reached.");
    } finally {
      setSigning(false);
    }
  };

  let body;
  if (issued) {
    body = (
      <div className="setup-result">
        <p className="setup-warn">
          <strong>Store this API key now: it will not be shown again.</strong>
        </p>
        <p className="muted">
          Tenant <span className="mono">{issued.tenantId}</span>
        </p>
        <div className="field">
          <label htmlFor="setup-key">API key</label>
          <div className="secret-row">
            <input
              id="setup-key"
              className="input mono"
              readOnly
              autoComplete="off"
              spellCheck={false}
              value={issued.key}
              onFocus={(e) => {
                e.currentTarget.select();
              }}
            />
            <CopyButton text={issued.key} />
          </div>
        </div>
        {error ? (
          <p role="alert" className="form-error">
            {error}
          </p>
        ) : null}
        <Button
          kind="primary"
          disabled={signing}
          onClick={() => {
            void signInWithKey();
          }}
        >
          Sign in with this key
        </Button>
      </div>
    );
  } else if (state.isPending) {
    body = <p className="muted">Checking this instance...</p>;
  } else if (state.isError) {
    body = (
      <p role="alert" className="form-error">
        {state.error instanceof SetupError ? state.error.message : "Cannot reach this instance."}
      </p>
    );
  } else if (state.data === "off") {
    body = (
      <>
        <p>First-run setup is off on this instance.</p>
        <p className="muted">
          <span className="mono">OHS_SETUP_TOKEN</span> is not set. Create a tenant with <span className="mono">ohs admin tenant create</span>,
          or set the token and restart.
        </p>
        <Link to="/sign-in">Sign in</Link>
      </>
    );
  } else if (state.data === "done" || closed) {
    body = (
      <>
        <p>This instance is already set up.</p>
        <Link to="/sign-in">Sign in</Link>
      </>
    );
  } else {
    body = (
      <form
        className="setup-form"
        autoComplete="off"
        onSubmit={(e) => {
          void submit(e);
        }}
      >
        <p className="muted">
          This instance has no tenant yet. Enter its setup token (<span className="mono">OHS_SETUP_TOKEN</span>) to create the first tenant and its
          API key. This works once, and the first tenant is the instance admin.
        </p>
        <div className="field">
          <label htmlFor="setup-token">Setup token</label>
          <input
            id="setup-token"
            className="input mono"
            type="password"
            autoComplete="off"
            spellCheck={false}
            value={token}
            onChange={(e) => {
              setToken(e.target.value);
            }}
          />
        </div>
        <div className="field">
          <label htmlFor="tenant-name">Tenant name (optional)</label>
          <input
            id="tenant-name"
            className="input"
            type="text"
            autoComplete="off"
            spellCheck={false}
            maxLength={MAX_TENANT_NAME}
            placeholder="default"
            value={name}
            onChange={(e) => {
              setName(e.target.value);
            }}
          />
        </div>
        {error ? (
          <p role="alert" className="form-error">
            {error}
          </p>
        ) : null}
        <Button kind="primary" type="submit" disabled={run.isPending}>
          Create the first tenant
        </Button>
      </form>
    );
  }

  return (
    <main className="setup-page">
      <section className="setup-panel">
        <div className="setup-brand">
          <BrandMark />
          <span>Open Health Sync</span>
        </div>
        <p className="setup-eyebrow">First run</p>
        <h1>Set up this instance</h1>
        {body}
      </section>
    </main>
  );
}
