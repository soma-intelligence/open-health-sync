import { readPref, writePref, type PrefName } from "./prefs";

describe("prefs", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    window.localStorage.clear();
  });

  it("stores only the three preferences under ohs.console.", () => {
    const names: PrefName[] = ["theme", "inspector", "inspector-lang"];
    names.forEach((n) => {
      writePref(n, "x");
    });
    expect(Object.keys(window.localStorage).sort()).toEqual([
      "ohs.console.inspector",
      "ohs.console.inspector-lang",
      "ohs.console.theme",
    ]);
    expect(readPref("theme")).toBe("x");
    writePref("other" as PrefName, "y");
    expect(window.localStorage).toHaveLength(3);
    expect(readPref("other" as PrefName)).toBeNull();
  });

  it("survives a storage that throws", () => {
    const boom = () => {
      throw new Error("blocked");
    };
    vi.stubGlobal("localStorage", { getItem: boom, setItem: boom, removeItem: boom, clear: boom });
    expect(readPref("theme")).toBeNull();
    expect(() => {
      writePref("theme", "dark");
    }).not.toThrow();
  });
});
