import { useState } from "react";

import { Button } from "../../ui/Button";
import { Card } from "../../ui/Card";
import { ErrorPanel } from "../../ui/ErrorPanel";
import { usePreview, type PreviewDataset } from "./api";

const DATASETS: readonly [PreviewDataset, string][] = [
  ["daily", "Daily summaries"],
  ["sleep", "Sleep"],
  ["workouts", "Workouts"],
];

/** Health values wait for a press: each read is recorded in the audit log, and nothing is kept after it. */
export function PreviewPanel({ userId }: { userId: string }) {
  const preview = usePreview(userId);
  const [title, setTitle] = useState("");
  const result = preview.data;

  return (
    <Card title="Preview">
      <p className="muted">Reads the first 10 rows. Each read is recorded in the audit log.</p>
      <div className="button-row" role="group" aria-label="Preview">
        {DATASETS.map(([dataset, label]) => (
          <Button
            key={dataset}
            disabled={preview.isPending}
            onClick={() => {
              setTitle(label);
              preview.mutate(dataset);
            }}
          >
            {label}
          </Button>
        ))}
      </div>
      {preview.isError ? <ErrorPanel error={preview.error} /> : null}
      {result ? (
        <div className="preview-out">
          <h4>{`${title}: the first ${String(result.rows.length)}`}</h4>
          {result.rows.length > 0 ? <pre className="json mono">{JSON.stringify(result.rows, null, 2)}</pre> : <p className="muted">Nothing yet.</p>}
        </div>
      ) : null}
    </Card>
  );
}
