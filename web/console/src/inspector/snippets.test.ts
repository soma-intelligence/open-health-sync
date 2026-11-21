import { snippet, type InspectorRequest } from "./snippets";

const o = "https://ohs.example.com";

describe("snippet", () => {
  it("curl GET", () => {
    expect(snippet({ method: "GET", path: "/v1/users", query: { limit: "50" } }, "curl", o)).toBe(
      'curl "https://ohs.example.com/v1/users?limit=50" \\\n  -H "Authorization: Bearer $OHS_API_KEY"',
    );
  });

  it("curl POST with a body", () => {
    const req: InspectorRequest = { method: "POST", path: "/v1/webhooks", body: { url: "https://x.test/it's", events: ["a"] } };
    const out = snippet(req, "curl", o);
    expect(out.startsWith('curl -X POST "https://ohs.example.com/v1/webhooks" \\\n')).toBe(true);
    expect(out).toContain('  -H "Content-Type: application/json"');
    expect(out).toContain("-d '{\n  \"url\": \"https://x.test/it'\\''s\",");
    expect(out.endsWith("}'")).toBe(true);
  });

  it("TypeScript", () => {
    const out = snippet({ method: "POST", path: "/v1/webhooks", body: { url: "https://x.test" } }, "ts", o);
    expect(out).toContain("Authorization: `Bearer ${process.env.OHS_API_KEY}`");
    expect(out).toContain('method: "POST"');
    expect(out).toContain("body: JSON.stringify(");
    expect(out.endsWith("const data = await res.json();")).toBe(true);
  });

  it("TypeScript GET has no body", () => {
    const out = snippet({ method: "GET", path: "/v1/users", query: { limit: "50" } }, "ts", o);
    expect(out).toContain("https://ohs.example.com/v1/users?limit=50");
    expect(out).not.toContain("JSON.stringify");
  });

  it("Python", () => {
    const get = snippet({ method: "GET", path: "/v1/users", query: { limit: "50" } }, "python", o);
    expect(get).toContain("import os, requests");
    expect(get).toContain("requests.get(");
    expect(get).toContain('params={"limit": "50"}');
    expect(get).toContain("f\"Bearer {os.environ['OHS_API_KEY']}\"");
    expect(get.endsWith("data = res.json()")).toBe(true);
    const post = snippet({ method: "POST", path: "/v1/x", body: { a: true, b: false, c: null, d: [1] } }, "python", o);
    expect(post).toContain("json=");
    expect(post).toContain("True");
    expect(post).toContain("False");
    expect(post).toContain("None");
    expect(post).not.toContain("true");
    expect(post).not.toContain("null");
  });

  it("never contains a key or the CSRF token", () => {
    const req: InspectorRequest = { method: "PUT", path: "/v1/keys", body: { name: "k" } };
    for (const lang of ["curl", "ts", "python"] as const) {
      const out = snippet(req, lang, o);
      expect(out).not.toContain("ohs_sk_");
      expect(out).not.toContain("X-OHS-CSRF");
    }
  });
});
