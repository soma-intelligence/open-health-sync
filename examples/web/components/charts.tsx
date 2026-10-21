// SPDX-License-Identifier: AGPL-3.0-only
//
// Two small charts, no library: daily columns and a line. They keep a fixed height at any width,
// so text stays at its size from a phone to a wide screen. One series each, so the card title
// names it and there is no legend; each mark has a tooltip (title), and the values are in the
// table or the figures beside the chart.

export interface Point {
  /** YYYY-MM-DD */
  day: string;
  value: number;
  /** Tooltip text. */
  title: string;
}

function niceStep(raw: number): number {
  const power = 10 ** Math.floor(Math.log10(raw));
  const unit = raw / power;
  return (unit <= 1 ? 1 : unit <= 2 ? 2 : unit <= 5 ? 5 : 10) * power;
}

function compact(n: number): string {
  return n >= 1000 ? `${Number((n / 1000).toFixed(1))}K` : String(n);
}

const shortDay = (day: string) =>
  new Intl.DateTimeFormat("en-GB", { day: "numeric", month: "short", timeZone: "UTC" }).format(new Date(`${day}T00:00:00Z`));

const pct = (n: number) => `${Number(n.toFixed(3))}%`;

function Ticks({ ticks, at }: { ticks: number[]; at: (t: number) => number }) {
  return ticks.map((t) => (
    <div key={t} className="tick" style={{ bottom: pct(at(t)) }}>
      <span>{compact(t)}</span>
    </div>
  ));
}

/** First, middle and last day under the plot. */
function DayLabels({ points }: { points: Point[] }) {
  const shown = points.length <= 2 ? points : [points[0], points[Math.floor((points.length - 1) / 2)], points.at(-1)];
  return (
    <div className="x-labels" aria-hidden="true">
      {shown.map((p, i) => (
        <span key={`${p?.day}-${i}`}>{shortDay(p?.day ?? "")}</span>
      ))}
    </div>
  );
}

/** Daily columns from a zero baseline, rounded at the data end. */
export function Columns({ points, label, height = 160 }: { points: Point[]; label: string; height?: number }) {
  const max = Math.max(...points.map((p) => p.value), 1);
  const step = niceStep(max / 3);
  const top = Math.ceil(max / step) * step;
  const ticks = Array.from({ length: Math.round(top / step) + 1 }, (_, i) => i * step);
  const at = (v: number) => (100 * v) / top;
  return (
    <figure className="chart" role="img" aria-label={label}>
      <div className="plot" style={{ height }}>
        <Ticks ticks={ticks} at={at} />
        <div className="columns">
          {points.map((p) => (
            <div key={p.day} className="column" title={p.title}>
              {p.value > 0 && <span className="bar" style={{ height: pct(at(p.value)) }} />}
            </div>
          ))}
        </div>
      </div>
      <DayLabels points={points} />
    </figure>
  );
}

/** A 2px line with a light wash under it and a dot on the last value. */
export function Line({ points, label, unit, height = 160 }: { points: Point[]; label: string; unit: string; height?: number }) {
  const values = points.map((p) => p.value);
  const lo = Math.floor((Math.min(...values) - 3) / 5) * 5;
  const hi = Math.ceil((Math.max(...values) + 3) / 5) * 5;
  const ticks = [lo, Math.round((lo + hi) / 2), hi];
  // Percentages of the plot: x from the left, y from the bottom.
  const x = (i: number) => (points.length === 1 ? 50 : (100 * i) / (points.length - 1));
  const y = (v: number) => (100 * (v - lo)) / (hi - lo || 1);
  const path = points.map((p, i) => `${i === 0 ? "M" : "L"}${x(i).toFixed(3)},${(100 - y(p.value)).toFixed(3)}`).join(" ");
  const wash = `${path} L${x(points.length - 1)},100 L${x(0)},100 Z`;
  const last = points.at(-1);
  const end = last ? { left: pct(x(points.length - 1)), bottom: pct(y(last.value)) } : undefined;
  return (
    <figure className="chart line-chart" role="img" aria-label={label}>
      <div className="plot" style={{ height }}>
        <Ticks ticks={ticks} at={y} />
        <svg viewBox="0 0 100 100" preserveAspectRatio="none" aria-hidden="true">
          <path className="wash" d={wash} />
          <path className="line" d={path} />
        </svg>
        {points.map((p, i) => (
          <span key={p.day} className="hit" title={p.title} style={{ left: pct(x(i)), bottom: pct(y(p.value)) }} />
        ))}
        {last && end && (
          <>
            <span className="dot" style={end} />
            <span className="end-label" style={end}>
              {last.value} {unit}
            </span>
          </>
        )}
      </div>
      <DayLabels points={points} />
    </figure>
  );
}
