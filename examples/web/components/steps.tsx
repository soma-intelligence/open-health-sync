// SPDX-License-Identifier: AGPL-3.0-only
//
// The four steps of the example, in order. Steps 2 and 3 are about one user, so they link to
// that user's pages once one is picked.

import Link from "next/link";

const STEPS = [
  { n: "01", name: "Users", href: () => "/" },
  { n: "02", name: "Get data in", href: (user?: string) => user && `/users/${user}` },
  { n: "03", name: "Read it back", href: (user?: string) => user && `/users/${user}/data` },
  { n: "04", name: "Webhooks", href: () => "/webhooks" },
];

export function Steps({ current, userId }: { current: 1 | 2 | 3 | 4; userId?: string }) {
  return (
    <nav aria-label="Steps">
      <ol className="steps">
        {STEPS.map((step, i) => {
          const href = step.href(userId);
          const inner = (
            <>
              <span className="step-num">{step.n}</span>
              <span className="step-name">{step.name}</span>
            </>
          );
          return (
            <li key={step.n}>
              {i + 1 === current ? (
                <span className="step current" aria-current="step">
                  {inner}
                </span>
              ) : href ? (
                <Link href={href}>{inner}</Link>
              ) : (
                <span className="step unavailable" title="Create or pick a user first">
                  {inner}
                </span>
              )}
            </li>
          );
        })}
      </ol>
    </nav>
  );
}
