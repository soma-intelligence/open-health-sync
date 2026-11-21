import type { Pipeline } from "../../api/types";
import type { Tone } from "../../lib/classify";
import { relTime } from "../../lib/format";
import { pipelineStages, type Stage } from "../../lib/pipeline";
import { Pill } from "../../ui/Pill";
import { StageCard } from "./StageCard";

/** One line for the whole pipeline: the worst stage decides, an idle stage is not a fault. */
export function pipelineSummary(stages: readonly Stage[]): { tone: Tone; text: string } {
  const count = (tone: Tone) => stages.filter((s) => s.tone === tone).length;
  const bad = count("bad");
  const warn = count("warn");
  const ok = count("ok");
  if (bad > 0) return { tone: "bad", text: bad === 1 ? "1 stage blocked" : `${String(bad)} stages blocked` };
  if (warn > 0) return { tone: "warn", text: warn === 1 ? "1 stage to check" : `${String(warn)} stages to check` };
  if (ok === 0) return { tone: "off", text: "Nothing flowing yet" };
  return { tone: "ok", text: ok === stages.length ? "Every stage OK" : `${String(ok)} ${ok === 1 ? "stage" : "stages"} OK, the rest idle` };
}

/**
 * The pipeline as a light card: one tile per stage in the order data flows, each with its state chip.
 * stale: the last refresh failed, so the figures are the last ones that came back, not live ones.
 */
export function PipelineBand({ pipeline, stale = false }: { pipeline: Pipeline; stale?: boolean }) {
  const stages = pipelineStages(pipeline);
  const summary = pipelineSummary(stages);
  const label = stale ? "Pipeline, last known" : "Pipeline, live";
  return (
    <section className="card pipeline" aria-label={label}>
      <header className="card-h">
        <h2>Pipeline</h2>
        <span className={stale ? "live stale" : "live"}>{stale ? "Last known" : "Live"}</span>
        <span className="pipeline-updated">Updated {relTime(pipeline.generated_at)}</span>
        <div className="card-r">
          <Pill tone={summary.tone}>{summary.text}</Pill>
        </div>
      </header>
      <div className="card-b">
        <div className="stages">
          {stages.map((stage) => (
            <StageCard key={stage.key} stage={stage} />
          ))}
        </div>
      </div>
    </section>
  );
}
