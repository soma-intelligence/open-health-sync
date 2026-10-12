"use client";
import { createOpenAPIPage } from "fumadocs-openapi/ui";

// No "try it" playground: an instance only answers cross-origin requests for /openapi.yaml,
// so calls from this site's origin would fail. Each operation shows request samples instead.
export const APIPage = createOpenAPIPage({
  playground: { enabled: false },
});
