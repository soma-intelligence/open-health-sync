import { act, renderHook } from "@testing-library/react";

import { readPref } from "../lib/prefs";
import { applyTheme, useTheme } from "./theme";

describe("theme", () => {
  afterEach(() => {
    document.documentElement.removeAttribute("data-theme");
    window.localStorage.clear();
  });

  it("auto removes data-theme", () => {
    const root = document.createElement("div");
    root.setAttribute("data-theme", "dark");
    applyTheme("auto", root);
    expect(root).not.toHaveAttribute("data-theme");
  });

  it("light and dark set it", () => {
    const root = document.createElement("div");
    applyTheme("light", root);
    expect(root).toHaveAttribute("data-theme", "light");
    applyTheme("dark", root);
    expect(root).toHaveAttribute("data-theme", "dark");
  });

  it("the choice persists", () => {
    const first = renderHook(() => useTheme());
    expect(first.result.current[0]).toBe("auto");
    act(() => {
      first.result.current[1]("dark");
    });
    expect(document.documentElement).toHaveAttribute("data-theme", "dark");
    expect(readPref("theme")).toBe("dark");
    first.unmount();
    expect(renderHook(() => useTheme()).result.current[0]).toBe("dark");
  });
});
