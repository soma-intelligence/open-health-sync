import { useState } from "react";

import { withQuery } from "../../api/paths";
import { downloadFile } from "../../api/download";
import { useApi } from "../../session/SessionProvider";
import { Button } from "../../ui/Button";
import { Card } from "../../ui/Card";
import { useToast } from "../../ui/Toast";
import { userPath } from "./api";

const CSV: readonly [string, string][] = [
  ["samples", "Samples"],
  ["category-samples", "Category samples"],
  ["sleep", "Sleep"],
  ["workouts", "Workouts"],
  ["nutrition", "Nutrition"],
  ["daily", "Daily summaries"],
  ["ecg", "ECG"],
];

interface FileSpec {
  label: string;
  path: string;
  name: string;
}

/** Downloads: the file is fetched with the session and built in the page as an object URL. No link holds a key or a token. */
export function DownloadsPanel({ userId, bare = false }: { userId: string; bare?: boolean }) {
  const api = useApi();
  const toast = useToast();
  const [busy, setBusy] = useState<string | null>(null);

  const standard: FileSpec[] = [
    {
      label: "FHIR R4 Bundle",
      path: withQuery(`${userPath(userId, "fhir", "Patient", userId)}/$everything`, { limit: 1000 }),
      name: `fhir-${userId}.json`,
    },
    { label: "Open mHealth", path: withQuery(userPath(userId, "omh", "data-points"), { limit: 1000 }), name: `omh-${userId}.json` },
    { label: "Full archive (zip)", path: userPath(userId, "archive"), name: `archive-${userId}.zip` },
  ];
  const csv: FileSpec[] = CSV.map(([dataset, label]) => ({
    label,
    path: withQuery(userPath(userId, "exports", dataset), { format: "csv" }),
    name: `${dataset}-${userId}.csv`,
  }));

  const save = async (spec: FileSpec) => {
    setBusy(spec.name);
    try {
      await downloadFile(api, spec.path, spec.name);
    } catch (e) {
      toast(e instanceof Error ? e.message : "The download failed.", "bad");
    } finally {
      setBusy(null);
    }
  };

  const group = (title: string, specs: FileSpec[]) => (
    <>
      <h4>{title}</h4>
      <div className="button-row" role="group" aria-label={title}>
        {specs.map((s) => (
          <Button
            key={s.name}
            size="sm"
            disabled={busy !== null}
            onClick={() => {
              void save(s);
            }}
          >
            {s.label}
          </Button>
        ))}
      </div>
    </>
  );

  const body = (
    <>
      <p className="card-hint">
        Each export is recorded in the audit log. FHIR and Open mHealth hold the first 1,000 records; the archive holds everything.
      </p>
      {group("Standard formats", standard)}
      {group("CSV", csv)}
    </>
  );
  // Bare: inside the Consent and privacy card, under its own heading.
  if (bare) {
    return (
      <section className="export" aria-labelledby={`export-${userId}`}>
        <h3 id={`export-${userId}`} className="sub-title">
          Export
        </h3>
        {body}
      </section>
    );
  }
  return <Card title="Downloads">{body}</Card>;
}
