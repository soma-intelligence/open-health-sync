import { Header } from "@/components/layout/header";

export default function Layout({ children }: LayoutProps<"/">) {
  return (
    <div className="ohs-docs">
      <Header />
      {children}
    </div>
  );
}
