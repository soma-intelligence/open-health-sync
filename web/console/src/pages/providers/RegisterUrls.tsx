import type { AdminProvider } from "../../api/types";
import { Card } from "../../ui/Card";
import { CopyButton } from "../../ui/CopyButton";

function UrlRow({ label, url }: { label: string; url: string }) {
  return (
    <div className="url-row">
      <div className="url-label">{label}</div>
      <code className="mono url-value">{url}</code>
      <CopyButton text={url} label="Copy" />
    </div>
  );
}

/** The addresses to enter in the provider's developer portal. Nothing for a provider that registers none. */
export function RegisterUrls({ provider }: { provider: AdminProvider }) {
  const { redirect, inbound } = provider.urls;
  if (!redirect && !inbound) return null;
  return (
    <Card title="URLs to register">
      <p className="muted">In {provider.name}'s developer portal, create an app and enter {redirect && inbound ? "these addresses" : "this address"}.</p>
      {redirect ? <UrlRow label="Redirect URL" url={redirect} /> : null}
      {inbound ? <UrlRow label="Notification URL" url={inbound} /> : null}
    </Card>
  );
}
