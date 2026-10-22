import { ExternalLink, Sparkles } from "lucide-react";
import type { Features, Recommendation, Track } from "../api/types";

const FEATURE_ROWS: { k: keyof Features; label: string; fmt?: (v: number) => string; max?: number; min?: number }[] = [
  { k: "energy", label: "Energy" },
  { k: "valence", label: "Mood" },
  { k: "danceability", label: "Dance" },
  { k: "acousticness", label: "Acoustic" },
  { k: "instrumentalness", label: "Instrumental" },
  { k: "speechiness", label: "Speech" },
  { k: "tempo", label: "Tempo", fmt: (v) => `${Math.round(v)} bpm`, min: 60, max: 200 },
];

/** Seed vs target vs selected, one bar per feature (bars beat radar for reading values). */
export function FeatureCompare({ seed, target, other }: { seed?: Features; target?: Features; other?: Features }) {
  const norm = (row: (typeof FEATURE_ROWS)[number], v?: number) =>
    v === undefined ? undefined : Math.max(0, Math.min(1, (v - (row.min ?? 0)) / ((row.max ?? 1) - (row.min ?? 0))));
  return (
    <div className="features" role="table" aria-label="Audio feature comparison">
      <div className="features__legend" role="row">
        <span className="legend-dot legend-dot--seed" aria-hidden /> seed
        <span className="legend-dot legend-dot--target" aria-hidden /> target
        {other && (
          <>
            <span className="legend-dot legend-dot--other" aria-hidden /> selected
          </>
        )}
      </div>
      {FEATURE_ROWS.map((row) => {
        const s = norm(row, seed?.[row.k]);
        const t = norm(row, target?.[row.k]);
        const o = norm(row, other?.[row.k]);
        const raw = other?.[row.k] ?? seed?.[row.k];
        return (
          <div key={row.k} className="features__row" role="row">
            <span className="features__name" role="rowheader">
              {row.label}
            </span>
            <span className="features__track" role="cell">
              {s !== undefined && <span className="features__seed" style={{ width: `${s * 100}%` }} />}
              {o !== undefined && <span className="features__other" style={{ width: `${o * 100}%` }} />}
              {t !== undefined && <span className="features__target" style={{ left: `${t * 100}%` }} />}
            </span>
            <span className="features__val mono" role="cell">
              {raw === undefined ? "—" : row.fmt ? row.fmt(raw) : raw.toFixed(2)}
            </span>
          </div>
        );
      })}
    </div>
  );
}

/** Explains the selected recommendation: reasons + every ranking signal. */
export function Detail({
  rec,
  seed,
  target,
  onReseed,
}: {
  rec?: Recommendation;
  seed: Track | null;
  target?: Features;
  onReseed: (t: Track) => void;
}) {
  return (
    <section className="panel" aria-labelledby="detail-h">
      <header className="panel__head">
        <h2 id="detail-h">{rec ? "Why this track" : "Audio profile"}</h2>
      </header>
      {rec && (
        <div className="detail">
          <div className="detail__head">
            {rec.track.artworkUrl && <img src={rec.track.artworkUrl} alt="" width={56} height={56} />}
            <div>
              <p className="detail__title">{rec.track.title}</p>
              <p className="detail__artist">{rec.track.artist}</p>
            </div>
          </div>
          <ul className="detail__reasons">
            {rec.reasons.map((r) => (
              <li key={r}>{r}</li>
            ))}
          </ul>
          <dl className="signals">
            {Object.entries(rec.breakdown.signals)
              .sort((a, b) => a[0].localeCompare(b[0]))
              .map(([k, v]) => (
                <div key={k} className="signals__item">
                  <dt className="mono">{k}</dt>
                  <dd className="mono">{v.toFixed(2)}</dd>
                </div>
              ))}
          </dl>
          <div className="detail__actions">
            <button className="btn btn--primary" onClick={() => onReseed(rec.track)}>
              <Sparkles size={14} aria-hidden /> Explore from here
            </button>
            {rec.track.url && (
              <a className="btn" href={rec.track.url} target="_blank" rel="noreferrer">
                <ExternalLink size={14} aria-hidden /> Open
              </a>
            )}
          </div>
        </div>
      )}
      {seed && <FeatureCompare seed={seed.features} target={target} other={rec?.track.features} />}
      {!seed && <p className="panel__empty">No song selected yet.</p>}
    </section>
  );
}
