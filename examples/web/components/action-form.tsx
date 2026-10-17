// SPDX-License-Identifier: AGPL-3.0-only
"use client";

// A form that runs a Server Action and shows what it answered. The action runs on the server,
// where the tenant API key is; the browser only sends the form and gets the result back.

import { useActionState, type ReactNode } from "react";
import type { ActionResult } from "../lib/action-result.ts";

export function ActionForm(props: {
  action: (previous: ActionResult | null, form: FormData) => Promise<ActionResult | null>;
  submit: string;
  pending: string;
  primary?: boolean;
  inline?: boolean;
  /**
   * False hides the form but keeps the last result on screen. The page keeps the form mounted
   * after its action changed what the form is for (an endpoint just registered), so a secret
   * shown once is not lost when the page refreshes.
   */
  show?: boolean;
  children?: ReactNode;
}) {
  const [result, run, isPending] = useActionState(props.action, null);
  if (props.show === false) return result ? <Result result={result} /> : null;
  return (
    <form action={run} className={props.inline ? "inline-form" : "form"}>
      {props.children}
      <div className="button-row">
        <button type="submit" className={props.primary ? "primary" : undefined} disabled={isPending}>
          {isPending ? props.pending : props.submit}
        </button>
        {isPending && <span className="pill live ok">Working</span>}
      </div>
      {result && !isPending && <Result result={result} />}
    </form>
  );
}

function Result({ result }: { result: ActionResult }) {
  return (
    <div className="form-result" aria-live="polite">
      <div className={`notice ${result.ok ? "ok" : "bad"}`}>
        <div>
          <p>{result.message}</p>
          {result.secret && (
            <>
              <p className="small">Shown once. Put it in .env.local as OHS_WEBHOOK_SECRET to keep it after a restart:</p>
              <code className="secret">{result.secret}</code>
            </>
          )}
        </div>
      </div>
      {result.facts && result.facts.length > 0 && (
        <dl className="facts compact" style={{ marginTop: 12 }}>
          {result.facts.map(([label, value]) => (
            <div key={label} style={{ display: "contents" }}>
              <dt>{label}</dt>
              <dd>{value}</dd>
            </div>
          ))}
        </dl>
      )}
    </div>
  );
}
