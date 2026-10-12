import { DocsLayout } from "fumadocs-ui/layouts/notebook";
import { baseOptions } from "@/lib/layout.shared";
import { source } from "@/lib/source";

export default function Layout({ children }: LayoutProps<"/documentation">) {
  const base = baseOptions();
  return (
    <DocsLayout tree={source.getPageTree()} {...base} tabMode="navbar" nav={{ ...base.nav, mode: "top" }}>
      {children}
    </DocsLayout>
  );
}
