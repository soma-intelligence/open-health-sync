"use client";
import { Check, Copy, SquarePen } from "lucide-react";
import { useState } from "react";
import { asset } from "@/lib/asset";

/** Copy the page as Markdown (for an AI assistant), and edit its source on GitHub. */
export function PageActions({ markdownUrl, editUrl }: { markdownUrl: string; editUrl: string }) {
  const [state, setState] = useState<"idle" | "copied" | "failed">("idle");
  async function copy() {
    try {
      const res = await fetch(asset(markdownUrl));
      if (!res.ok) throw new Error(String(res.status));
      await navigator.clipboard.writeText(await res.text());
      setState("copied");
    } catch {
      setState("failed");
    }
    setTimeout(() => setState("idle"), 2000);
  }
  return (
    <div className="ohs-page-actions">
      <button type="button" className="ohs-btn ohs-btn-secondary" onClick={copy} aria-live="polite">
        {state === "copied" ? <Check aria-hidden="true" /> : <Copy aria-hidden="true" />}
        {state === "copied" ? "Copied" : state === "failed" ? "Could not copy" : "Copy as Markdown"}
      </button>
      <a href={editUrl} className="ohs-btn ohs-btn-secondary">
        <SquarePen aria-hidden="true" />
        Edit on GitHub
      </a>
    </div>
  );
}
