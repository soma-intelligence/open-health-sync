import { useEffect, useRef, useState } from "react";

import { Icon } from "./Icon";

/** Copies text to the clipboard and says so for a moment. */
export function CopyButton({ text, label = "Copy" }: { text: string; label?: string }) {
  const [state, setState] = useState<"idle" | "copied" | "failed">("idle");
  const timer = useRef<number | undefined>(undefined);
  useEffect(
    () => () => {
      window.clearTimeout(timer.current);
    },
    [],
  );

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
      setState("copied");
    } catch {
      setState("failed");
    }
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => {
      setState("idle");
    }, 1600);
  };

  return (
    <button
      type="button"
      className="btn ghost sm"
      onClick={() => {
        void copy();
      }}
    >
      <Icon name={state === "copied" ? "check" : "copy"} size="sm" />
      <span role="status">{state === "copied" ? "Copied" : state === "failed" ? "Copy failed" : label}</span>
    </button>
  );
}
