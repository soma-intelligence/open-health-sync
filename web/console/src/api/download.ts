import type { ApiClient, Download } from "./client";

/** Fetches path as a file and hands it to the browser's save dialog. */
export async function downloadFile(api: ApiClient, path: string, fallbackName: string): Promise<void> {
  const { data } = await api.request<Download>("GET", path, { as: "blob" });
  const url = URL.createObjectURL(data.blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = data.filename ?? fallbackName;
  link.rel = "noopener";
  document.body.append(link);
  link.click();
  link.remove();
  // Safari and some Firefox versions start the save after click() returns: revoking at once can abort it.
  setTimeout(() => {
    URL.revokeObjectURL(url);
  }, 1000);
}
