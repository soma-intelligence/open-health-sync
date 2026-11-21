"use client";
import { useCodeUsage, useOperation, useResponseExamples } from "fumadocs-openapi/operation";
import { type CodeUsageGenerator, createCodeUsageGeneratorRegistry } from "fumadocs-openapi/requests/generators";
import { curl } from "fumadocs-openapi/requests/generators/curl";
import { javascript } from "fumadocs-openapi/requests/generators/javascript";
import { python } from "fumadocs-openapi/requests/generators/python";
import { createOpenAPIPage } from "fumadocs-openapi/ui";
import { DynamicCodeBlock } from "fumadocs-ui/components/dynamic-codeblock";
import { type KeyboardEvent, useEffect, useId, useState } from "react";
import { MethodBadge } from "@/components/method-badge";

/** A Swift sample with URLSession, the way an iOS app calls the API. */
const swift: CodeUsageGenerator = {
  label: "Swift",
  lang: "swift",
  generate(data) {
    const lines = [
      "import Foundation",
      "",
      `var request = URLRequest(url: URL(string: ${JSON.stringify(data.url)})!)`,
      `request.httpMethod = ${JSON.stringify(data.method.toUpperCase())}`,
    ];
    for (const name in data.header) {
      lines.push(
        `request.setValue(${JSON.stringify(data.header[name].value)}, forHTTPHeaderField: ${JSON.stringify(name)})`,
      );
    }
    if (data.body !== undefined && data.bodyMediaType) {
      const body = typeof data.body === "string" ? data.body : JSON.stringify(data.body, null, 2);
      lines.push(`request.setValue(${JSON.stringify(data.bodyMediaType)}, forHTTPHeaderField: "Content-Type")`);
      lines.push(`request.httpBody = Data("""`, ...body.split("\n"), `""".utf8)`);
    }
    lines.push(
      "",
      "let (data, response) = try await URLSession.shared.data(for: request)",
      "print((response as? HTTPURLResponse)?.statusCode ?? 0, String(decoding: data, as: UTF8.self))",
    );
    return lines.join("\n");
  },
};

const codeUsages = createCodeUsageGeneratorRegistry();
// The order of the tabs: the shell first, then the three languages most apps call the API from.
codeUsages.add("curl", curl);
codeUsages.add("javascript", javascript);
codeUsages.add("python", python);
codeUsages.add("swift", swift);

const storageKey = "ohs-docs-code-lang";

function readLang(): string {
  try {
    return localStorage.getItem(storageKey) ?? "curl";
  } catch {
    return "curl";
  }
}

/** Arrow keys move between tabs, as in any tablist. */
function onTabKey(e: KeyboardEvent<HTMLDivElement>) {
  if (e.key !== "ArrowRight" && e.key !== "ArrowLeft") return;
  const tabs = Array.from(e.currentTarget.querySelectorAll<HTMLButtonElement>('[role="tab"]'));
  const i = tabs.indexOf(document.activeElement as HTMLButtonElement);
  const next = tabs[(i + (e.key === "ArrowRight" ? 1 : tabs.length - 1)) % tabs.length];
  next?.focus();
  next?.click();
}

/**
 * The right column of an endpoint: the request in cURL, JavaScript, Python or Swift, then an example response
 * for each status. No "try it" runner: an instance only answers cross-origin requests for
 * /openapi.yaml, so each sample has a copy button instead.
 */
function CodePanel() {
  const { codeUsages: registry } = useOperation();
  const generators = Array.from(registry.map());
  const [lang, setLang] = useState("curl");
  useEffect(() => setLang(readLang()), []);
  const current = generators.some(([id]) => id === lang) ? lang : (generators[0]?.[0] ?? "curl");
  const code = useCodeUsage(current);
  const responses = useResponseExamples();
  const [status, setStatus] = useState(0);
  const response = responses[status] ?? responses[0];
  const sample = response?.examples?.[0]?.sample;
  const tabsId = useId();

  function pick(id: string) {
    setLang(id);
    try {
      localStorage.setItem(storageKey, id);
    } catch {}
  }

  return (
    <div className="ohs-code-panel">
      <div className="ohs-code-head">
        <span className="ohs-code-title">Request</span>
        <div className="ohs-segmented" role="tablist" aria-label="Language" tabIndex={-1} onKeyDown={onTabKey}>
          {generators.map(([id, gen]) => (
            <button
              key={id}
              type="button"
              role="tab"
              id={`${tabsId}-${id}`}
              aria-selected={id === current}
              aria-controls={`${tabsId}-code`}
              tabIndex={id === current ? 0 : -1}
              onClick={() => pick(id)}
            >
              {gen.label ?? gen.lang}
            </button>
          ))}
        </div>
      </div>
      <div id={`${tabsId}-code`} role="tabpanel" aria-labelledby={`${tabsId}-${current}`}>
        <DynamicCodeBlock lang={registry.get(current)?.lang ?? "bash"} code={code ?? ""} />
      </div>
      {response ? (
        <>
          <div className="ohs-code-head">
            <span className="ohs-code-title">Response</span>
            <div className="ohs-status-list" role="tablist" aria-label="Status" tabIndex={-1} onKeyDown={onTabKey}>
              {responses.map((tab, i) => (
                <button
                  key={tab.code}
                  type="button"
                  role="tab"
                  aria-selected={tab === response}
                  tabIndex={tab === response ? 0 : -1}
                  className="ohs-status"
                  data-tone={tab.code.startsWith("2") ? "ok" : tab.code.startsWith("4") ? "warn" : "bad"}
                  onClick={() => setStatus(i)}
                >
                  {tab.code}
                </button>
              ))}
            </div>
          </div>
          {sample === undefined ? (
            <p className="ohs-code-empty">{response.response.description || "No body."}</p>
          ) : (
            <DynamicCodeBlock
              lang={response.mediaType?.includes("json") ? "json" : "text"}
              code={typeof sample === "string" ? sample : JSON.stringify(sample, null, 2)}
            />
          )}
        </>
      ) : null}
    </div>
  );
}

/** The scopes an operation needs, from its security requirements. */
function scopesOf(security: { scopes: string[] }[][]): string[] {
  return [...new Set(security.flatMap((alt) => alt.flatMap((s) => s.scopes)))];
}

function EndpointBar({ method, path }: { method: string; path: string }) {
  const { security } = useOperation();
  const scopes = scopesOf(security);
  return (
    <div className="ohs-endpoint-bar">
      <MethodBadge method={method.toUpperCase()} />
      <code>{path}</code>
      {scopes.length ? <span className="ohs-endpoint-scope">scope {scopes.join(", ")}</span> : null}
    </div>
  );
}

/**
 * An endpoint: the method, path and description across the top, then two columns, the reference on
 * the left and the code on the right. Under 1200 px the code comes before the parameters, so a
 * phone reaches a working request without scrolling through every field.
 */
export const APIPage = createOpenAPIPage({
  playground: { enabled: false },
  codeUsages,
  content: {
    renderOperationLayout: (slots, { path, method }) => (
      <div className="ohs-endpoint-grid">
        <div className="ohs-endpoint-top">
          <EndpointBar method={method} path={path} />
          {slots.description}
        </div>
        <div className="ohs-endpoint-ref">
          {slots.authSchemes}
          {slots.parameters}
          {slots.body}
          {slots.responses}
          {slots.callbacks}
        </div>
        <div className="ohs-endpoint-code">
          <CodePanel />
        </div>
      </div>
    ),
  },
});
