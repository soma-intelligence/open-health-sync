import { Fragment } from "react";

import type { Pipeline } from "../../api/types";
import { relTime } from "../../lib/format";
import { pipelineStages } from "../../lib/pipeline";
import { Icon } from "../../ui/Icon";
import { StageCard } from "./StageCard";

/** The black band: the five stages in the order data flows. Black in both themes. */
export function PipelineBand({ pipeline }: { pipeline: Pipeline }) {
  const stages = pipelineStages(pipeline);
  return (
    <section className="band" aria-label="Pipeline, live">
      <div className="band-top">
        <span className="band-label">
          <span className="live-dot" aria-hidden="true" />
          Pipeline, live
        </span>
        <span className="band-label">Updated {relTime(pipeline.generated_at)}</span>
      </div>
      <div className="stages">
        {stages.map((stage, i) => (
          <Fragment key={stage.key}>
            {i > 0 ? (
              <span className="stage-link" aria-hidden="true">
                <Icon name="chevron" size="sm" />
              </span>
            ) : null}
            <StageCard stage={stage} />
          </Fragment>
        ))}
      </div>
    </section>
  );
}
