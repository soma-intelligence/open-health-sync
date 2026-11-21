export interface SkeletonProps {
  /** The shape of what is coming: table rows, a row of stat tiles, a few lines of text, or a whole page. */
  kind?: "table" | "tiles" | "lines" | "page";
  /** Rows of a table, tiles of a row, or lines of text. */
  count?: number;
  /** What a screen reader hears. */
  label?: string;
}

/**
 * The loading state of a block: grey bars at the size of the content that replaces them, so nothing
 * jumps when the data lands. No shimmer; it fades in after 200 ms so a fast answer shows no flash.
 * The region says "Loading" to assistive tech and is aria-busy until it goes.
 */
export function Skeleton({ kind = "table", count, label = "Loading" }: SkeletonProps) {
  let body;
  if (kind === "tiles") {
    body = (
      <div className="skel-tiles">
        {Array.from({ length: count ?? 3 }, (_, i) => (
          <div key={i} className="skel-tile">
            <span className="skel-bar w-40" />
            <span className="skel-bar big w-30" />
            <span className="skel-bar w-60" />
          </div>
        ))}
      </div>
    );
  } else if (kind === "lines") {
    body = (
      <div className="skel-lines">
        {Array.from({ length: count ?? 3 }, (_, i) => (
          <span key={i} className={i % 3 === 2 ? "skel-bar w-60" : "skel-bar w-90"} />
        ))}
      </div>
    );
  } else if (kind === "page") {
    body = (
      <div className="skel-page">
        <span className="skel-bar title w-30" />
        <span className="skel-bar w-60" />
        <div className="skel-card">
          <SkeletonRows count={count ?? 4} />
        </div>
      </div>
    );
  } else {
    body = <SkeletonRows count={count ?? 5} />;
  }
  return (
    <div className={`skeleton skeleton-${kind}`} role="status" aria-busy="true">
      <span className="sr-only">{label}</span>
      <div aria-hidden="true">{body}</div>
    </div>
  );
}

function SkeletonRows({ count }: { count: number }) {
  return (
    <div className="skel-rows">
      <div className="skel-row head">
        <span className="skel-bar w-20" />
        <span className="skel-bar w-15" />
        <span className="skel-bar w-15" />
      </div>
      {Array.from({ length: count }, (_, i) => (
        <div key={i} className="skel-row">
          <span className="skel-bar w-40" />
          <span className="skel-bar w-20" />
          <span className="skel-bar w-15" />
        </div>
      ))}
    </div>
  );
}
