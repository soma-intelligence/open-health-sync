import { cn } from "@/lib/cn";

const styles = {
  available: "border-emerald-500/30 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300",
  planned: "border-amber-500/30 bg-amber-500/10 text-amber-700 dark:text-amber-300",
  soon: "border-sky-500/30 bg-sky-500/10 text-sky-700 dark:text-sky-300",
  beta: "border-violet-500/30 bg-violet-500/10 text-violet-700 dark:text-violet-300",
} as const;

const labels = { available: "Available", planned: "Planned", soon: "Coming soon", beta: "Free beta" } as const;

/** A small pill saying whether something ships today. */
export function Status({ is, children }: { is: keyof typeof styles; children?: React.ReactNode }) {
  return (
    <span
      className={cn(
        "not-prose inline-flex items-center rounded-full border px-2 py-0.5 text-xs font-medium align-middle",
        styles[is],
      )}
    >
      {children ?? labels[is]}
    </span>
  );
}
