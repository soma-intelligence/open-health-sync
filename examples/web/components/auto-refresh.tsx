// SPDX-License-Identifier: AGPL-3.0-only
"use client";

// Reloads the page's server data every few seconds while it is visible: the webhook inbox, and
// the read-back page while the server is still processing an upload.

import { useRouter } from "next/navigation";
import { useEffect } from "react";

export function AutoRefresh({ every = 3000, label }: { every?: number; label: string }) {
  const router = useRouter();
  useEffect(() => {
    const id = setInterval(() => {
      if (document.visibilityState === "visible") router.refresh();
    }, every);
    return () => clearInterval(id);
  }, [router, every]);
  return <span className="pill live ok">{label}</span>;
}
