import { useId } from "react";
import { Link } from "react-router";

import { routerPath } from "../../lib/basename";
import { splitFigure } from "../../lib/format";
import type { Stage, StageFigure } from "../../lib/pipeline";
import { Pill } from "../../ui/Pill";

function Figure({ figure }: { figure: StageFigure }) {
  const value = figure.to ? <Link to={routerPath(figure.to)}>{figure.value}</Link> : figure.value;
  return (
    <div className="stage-fig">
      <dt>{figure.label}</dt>
      <dd className={figure.tone ?? undefined}>{value}</dd>
    </div>
  );
}

/** One stage of the pipeline: its state chip, the headline figure drawn large and the rest small. */
export function StageCard({ stage }: { stage: Stage }) {
  const titleId = useId();
  const [head, ...rest] = stage.figures;
  const big = splitFigure(head?.value);
  return (
    <article className={`stage ${stage.tone}`} aria-labelledby={titleId}>
      <div className="stage-h">
        <h3 id={titleId}>{stage.title}</h3>
        <Pill tone={stage.tone}>{stage.state}</Pill>
      </div>
      <p className="stage-big">
        <span className="stage-num">{big.figure}</span>
        {big.unit ? <span className="stage-unit">{big.unit}</span> : null}
      </p>
      <p className="stage-cap">{head?.label}</p>
      <p className="stage-role">{stage.role}</p>
      <dl className="stage-list">
        {rest.map((f) => (
          <Figure key={f.label} figure={f} />
        ))}
      </dl>
    </article>
  );
}
