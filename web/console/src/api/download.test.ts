import type { ApiClient } from "./client";
import { downloadFile } from "./download";

describe("downloadFile", () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it("keeps the blob URL a second after the click, so a slow browser still saves", async () => {
    vi.useFakeTimers();
    const revoked: string[] = [];
    Object.defineProperty(URL, "createObjectURL", { configurable: true, value: () => "blob:fake" });
    Object.defineProperty(URL, "revokeObjectURL", {
      configurable: true,
      value: (url: string) => {
        revoked.push(url);
      },
    });
    const clicks: string[] = [];
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      clicks.push(this.download);
    });
    const api = {
      request: () => Promise.resolve({ status: 200, headers: new Headers(), data: { blob: new Blob(["x"]), filename: "ohs.env" } }),
    } as unknown as ApiClient;

    await downloadFile(api, "/v1/admin/settings/export", "fallback.env");
    expect(clicks).toEqual(["ohs.env"]);
    expect(revoked).toEqual([]);
    vi.advanceTimersByTime(999);
    expect(revoked).toEqual([]);
    vi.advanceTimersByTime(1);
    expect(revoked).toEqual(["blob:fake"]);
  });
});
