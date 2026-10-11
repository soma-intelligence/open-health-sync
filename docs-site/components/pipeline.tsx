import { Bot, Database, FileArchive, Globe, Smartphone, Watch, Workflow } from "lucide-react";
import type { ReactNode } from "react";

function Box({ icon, title, children }: { icon: ReactNode; title: string; children: ReactNode }) {
  return (
    <div className="flex min-w-0 flex-1 flex-col gap-1 rounded-xl border bg-fd-card p-3 shadow-sm">
      <div className="flex items-center gap-2 text-sm font-medium text-fd-card-foreground">
        <span className="text-fd-primary">{icon}</span>
        {title}
      </div>
      <p className="text-xs leading-relaxed text-fd-muted-foreground">{children}</p>
    </div>
  );
}

function Arrow() {
  return (
    <div aria-hidden="true" className="flex items-center justify-center text-fd-muted-foreground">
      <span className="hidden md:inline">→</span>
      <span className="md:hidden">↓</span>
    </div>
  );
}

/** Sources flow through one binary into Postgres, out as REST and MCP. */
export function Pipeline() {
  return (
    <figure className="not-prose my-6">
      <div className="flex flex-col gap-2 md:flex-row md:items-stretch">
        <div className="flex flex-col gap-2 md:w-40">
          <Box icon={<Smartphone className="size-4" />} title="Apple Health">
            Your iOS app uploads gzip NDJSON parts.
          </Box>
          <Box icon={<Watch className="size-4" />} title="Cloud providers">
            OAuth pulls and push notifications.
          </Box>
        </div>
        <Arrow />
        <Box icon={<FileArchive className="size-4" />} title="Raw store">
          Every payload kept verbatim, on disk or S3, never rewritten.
        </Box>
        <Arrow />
        <Box icon={<Workflow className="size-4" />} title="ETL">
          Map, convert units, validate, de-duplicate, roll up local days.
        </Box>
        <Arrow />
        <Box icon={<Database className="size-4" />} title="Postgres">
          One canonical model, scoped by tenant and user.
        </Box>
        <Arrow />
        <div className="flex flex-col gap-2 md:w-40">
          <Box icon={<Globe className="size-4" />} title="REST API">
            Described by OpenAPI 3.1.
          </Box>
          <Box icon={<Bot className="size-4" />} title="MCP server">
            Read only, for AI agents.
          </Box>
        </div>
      </div>
    </figure>
  );
}
