import { Link } from "react-router";
import { useState } from "react";

import { ApiError } from "../api/client";
import type { Setting } from "../api/types";
import { readPref, writePref } from "../lib/prefs";
import { useSettingsQuery } from "../pages/settings/api";
import { sectionSlug } from "../pages/settings/model";
import { useIsAdmin } from "../session/SessionProvider";
import { CopyButton } from "../ui/CopyButton";
import { Icon } from "../ui/Icon";
import { Mono } from "../ui/Mono";
import { Pill } from "../ui/Pill";
import { useInspectorEntry } from "./InspectorProvider";
import { relatedSettings, snippet, type InspectorKind, type SnippetLang } from "./snippets";

const LANGS: readonly { id: SnippetLang; label: string }[] = [
  { id: "curl", label: "cURL" },
  { id: "ts", label: "TypeScript" },
  { id: "python", label: "Python" },
];

const KIND_LABEL: Record<InspectorKind, string> = {
  pipeline: "pipeline",
  users: "users",
  user: "user",
  providers: "providers",
  provider: "provider",
  jobs: "jobs",
  webhooks: "webhooks",
  webhook: "webhook",
  keys: "keys",
  audit: "audit",
  "audit-event": "audit event",
  settings: "settings",
  setting: "setting",
};

function initialLang(): SnippetLang {
  const stored = readPref("inspector-lang");
  return LANGS.find((l) => l.id === stored)?.id ?? "curl";
}

const json = (value: unknown): string => value === undefined ? "" : JSON.stringify(value, null, 2);

function RelatedSettings({ kind }: { kind: InspectorKind }) {
  const names = relatedSettings(kind);
  const admin = useIsAdmin();
  const query = useSettingsQuery({ passive: true });
  if (names.length === 0) return null;
  const byName = new Map<string, Setting>((query.data?.data ?? []).map((s) => [s.name, s]));
  return (
    <section className="insp-sec">
      <h4>Configuration</h4>
      <ul className="insp-settings">
        {names.map((name) => {
          const s = byName.get(name);
          return (
            <li key={name}>
              {s ? (
                <Link className="mono" to={`/settings/${sectionSlug(s.section)}/${name}`}>
                  {name}
                </Link>
              ) : (
                <Mono>{name}</Mono>
              )}
              {s ? (
                <span className="insp-value">
                  <Mono>{s.secret ? (s.value === "set" ? "set" : "not set") : (s.value ?? "unset")}</Mono>
                  <span className="muted">{s.source}</span>
                </span>
              ) : null}
            </li>
          );
        })}
      </ul>
      {admin ? null : <p className="muted">Settings need an instance admin session.</p>}
    </section>
  );
}

/** The inspector: the call behind the page, as snippets, the raw answer and the settings that shape it. */
export function InspectorPane({ onClose }: { onClose?: (() => void) | undefined }) {
  const entry = useInspectorEntry();
  const [lang, setLang] = useState<SnippetLang>(initialLang);

  const pick = (next: SnippetLang) => {
    setLang(next);
    writePref("inspector-lang", next);
  };

  return (
    <>
      <header className="insp-head">
        <div>
          <span className="insp-eyebrow">INSPECTOR</span>
          {entry ? (
            <p className="insp-title">
              <Mono>{KIND_LABEL[entry.kind]}</Mono> <Mono>{entry.title}</Mono>
              {entry.id ? (
                <>
                  {" "}
                  <Mono>{entry.id}</Mono>
                </>
              ) : null}
            </p>
          ) : null}
        </div>
        {onClose ? (
          <button type="button" className="icon-btn" aria-label="Close inspector" onClick={onClose}>
            <Icon name="x" />
          </button>
        ) : null}
      </header>
      {entry ? (
        <div className="insp-body">
          <section className="insp-sec">
            <h4>Request</h4>
            <p className="insp-call">
              <span className="insp-method mono">{entry.request.method}</span> <Mono>{entry.request.path}</Mono>
            </p>
            <div className="insp-tabs" role="tablist" aria-label="Language">
              {LANGS.map((l) => (
                <button
                  key={l.id}
                  type="button"
                  role="tab"
                  aria-selected={lang === l.id}
                  className={lang === l.id ? "on" : undefined}
                  onClick={() => {
                    pick(l.id);
                  }}
                >
                  {l.label}
                </button>
              ))}
            </div>
            <div className="insp-code" role="tabpanel">
              <div className="insp-copy">
                <CopyButton text={snippet(entry.request, lang, window.location.origin)} />
              </div>
              <pre>{snippet(entry.request, lang, window.location.origin)}</pre>
            </div>
          </section>
          <section className="insp-sec">
            <h4>Answer</h4>
            {entry.error ? (
              <p>
                <Pill tone="bad">{String(entry.error.status)}</Pill>
              </p>
            ) : null}
            <div className="insp-code">
              <div className="insp-copy">
                <CopyButton text={json(entry.error ? responseBody(entry.error) : entry.response)} />
              </div>
              <pre>{json(entry.error ? responseBody(entry.error) : entry.response)}</pre>
            </div>
          </section>
          <RelatedSettings kind={entry.kind} />
        </div>
      ) : (
        <p className="insp-empty muted">Nothing to inspect on this page.</p>
      )}
    </>
  );
}

// The body the server sent; its detail when it sent none.
function responseBody(error: ApiError): unknown {
  return error.body ?? { detail: error.detail };
}
