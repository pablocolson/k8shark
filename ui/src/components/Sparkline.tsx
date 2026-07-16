import type { StatsPoint } from "../types";

const WIDTH = 90;
const HEIGHT = 28;
const PAD = 2;

// A dependency-free inline-SVG sparkline of entries/sec over time (matches
// ServiceMap's "no charting lib" approach). Renders an empty placeholder
// until there are at least two points to draw a line between.
export function Sparkline({ points }: { points: StatsPoint[] }) {
  if (points.length < 2) {
    return <svg className="sparkline" width={WIDTH} height={HEIGHT} aria-hidden="true" />;
  }

  const rates = points.map((p) => p.entriesPerSec);
  const max = Math.max(1, ...rates);
  const step = WIDTH / (points.length - 1);
  const y = (r: number) => HEIGHT - PAD - (r / max) * (HEIGHT - PAD * 2);

  const coords = rates.map((r, i) => `${(i * step).toFixed(1)},${y(r).toFixed(1)}`).join(" ");
  const last = rates[rates.length - 1];

  return (
    <svg
      className="sparkline"
      width={WIDTH}
      height={HEIGHT}
      viewBox={`0 0 ${WIDTH} ${HEIGHT}`}
      role="img"
      aria-label={`entries per second trend over the last ${points.length} samples, most recent ${last.toFixed(1)}/s`}
    >
      <polyline points={coords} fill="none" stroke="var(--accent)" strokeWidth={1.5} strokeLinejoin="round" />
      <circle cx={WIDTH} cy={y(last)} r={2.2} fill="var(--accent)" />
    </svg>
  );
}
