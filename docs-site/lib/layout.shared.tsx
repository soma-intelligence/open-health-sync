import type { BaseLayoutProps } from "fumadocs-ui/layouts/shared";
import { Logo } from "@/components/logo";
import { latestVersion, repoUrl } from "./shared";

export function baseOptions(): BaseLayoutProps {
  return {
    nav: {
      title: <Logo />,
      url: "/documentation",
    },
    githubUrl: repoUrl,
    links: [
      { text: "Releases", url: `${repoUrl}/releases`, secondary: false },
      {
        type: "custom",
        secondary: true,
        children: (
          <span className="rounded-full border border-fd-primary/30 bg-fd-primary/10 px-2 py-0.5 font-mono text-xs text-fd-primary">
            v{latestVersion}
          </span>
        ),
      },
    ],
  };
}
