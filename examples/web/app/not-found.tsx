// SPDX-License-Identifier: AGPL-3.0-only

import Link from "next/link";
import { Empty } from "../components/ui.tsx";

export default function NotFound() {
  return (
    <main className="view">
      <section className="card">
        <Empty title="Not found">
          No such page, or no such user in this tenant (another tenant&apos;s user is a 404 too).{" "}
          <Link href="/">Back to the users</Link>.
        </Empty>
      </section>
    </main>
  );
}
