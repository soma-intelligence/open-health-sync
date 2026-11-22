/** A small pill saying whether something ships today: the brand tone if it does, neutral if not. */
const labels = { available: "Available", planned: "Planned", soon: "Coming soon", beta: "Free beta" } as const;

export function Status({ is, children }: { is: keyof typeof labels; children?: React.ReactNode }) {
  return (
    <span className="ohs-status-pill not-prose" data-tone={is === "available" || is === "beta" ? "on" : "off"}>
      {children ?? labels[is]}
    </span>
  );
}
