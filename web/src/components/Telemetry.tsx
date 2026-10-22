import { Pause, Play, RotateCcw } from "lucide-react";
import type { LearnerSnapshot, RequestSample, SourceReport, SourceSnapshot, SourceStatus } from "../api/types";

const SOURCE_LABEL: Record<string, string> = {
  lastfm: "Last.fm",
  reccobeats: "ReccoBeats",
  deezer: "Deezer",
  listenbrainz: "ListenBrainz",
  musicbrainz: "MusicBrainz",
};

export const sourceLabel = (s: string) => SOURCE_LABEL[s] ?? s;

type PillStatus = SourceStatus | "idle";

const STATUS_TEXT: Record<PillStatus, string> = {
  idle: "IDLE",
  ok: "OK",
  empty: "EMPTY",
  error: "ERR",
  disabled: "OFF",
  skipped: "SKIP",
  timeout: "T/O",
};

export function StatusPill({ status }: { status: PillStatus }) {
  return (
    <span className={`pill pill--${status}`} title={status}>
      <span className="pill__dot" aria-hidden />
      {STATUS_TEXT[status]}
    </span>
  );
}

/** Per-request pipeline trace: what each source did for *this* query. */
export function SourceTrace({ reports, tookMs, pool }: { reports: SourceReport[]; tookMs: number; pool: number }) {
  const max = Math.max(1, ...reports.map((r) => r.latencyMs));
  const stages = ["resolve", "candidates", "features", "enrich"] as const;
  return (
    <section className="panel" aria-labelledby="trace-h">
      <header className="panel__head">
        <h2 id="trace-h">Pipeline trace</h2>
        <span className="panel__meta mono">
          {tookMs}ms · pool {pool}
        </span>
      </header>
      {stages.map((stage) => {
        const rows = reports.filter((r) => r.stage === stage);
        if (!rows.length) return null;
        return (
          <div key={stage} className="trace__stage">
            <h3 className="trace__stagename mono">{stage}</h3>
            <ul className="trace__rows">
              {rows.map((r, i) => (
                <li key={r.source + i} className="trace__row" title={r.error ?? ""}>
                  <span className="trace__src">{sourceLabel(r.source)}</span>
                  <StatusPill status={r.status} />
                  <span className="trace__bar" aria-hidden>
                    <span style={{ width: `${(r.latencyMs / max) * 100}%` }} className={`trace__fill trace__fill--${r.status}`} />
                  </span>
                  <span className="trace__num mono">{r.status === "disabled" ? "—" : `${r.latencyMs}ms`}</span>
                  <span className="trace__num mono">{r.count}</span>
                </li>
              ))}
            </ul>
          </div>
        );
      })}
      {reports.some((r) => r.error && r.status !== "skipped") && (
        <details className="trace__errors">
          <summary>Errors</summary>
          <ul>
            {reports
              .filter((r) => r.error && r.status !== "skipped")
              .map((r, i) => (
                <li key={i} className="mono">
                  <strong>{sourceLabel(r.source)}</strong> ({r.stage}): {r.error}
                </li>
              ))}
          </ul>
        </details>
      )}
    </section>
  );
}

/** Process-wide source health from /api/telemetry. */
export function SourceHealth({
  sources,
  updatedAt,
  paused,
  onTogglePause,
  error,
}: {
  sources: SourceSnapshot[];
  updatedAt: Date | null;
  paused: boolean;
  onTogglePause: () => void;
  error: string | null;
}) {
  const stale = !updatedAt || Date.now() - updatedAt.getTime() > 15000;
  return (
    <section className="panel" aria-labelledby="health-h">
      <header className="panel__head">
        <h2 id="health-h">
          Source health{" "}
          <span className={`live ${paused || stale || error ? "is-stale" : ""}`}>
            {paused ? "paused" : error ? "offline" : stale ? "stale" : "live"}
          </span>
        </h2>
        <button className="icon-btn" onClick={onTogglePause} aria-label={paused ? "Resume live updates" : "Pause live updates"}>
          {paused ? <Play size={14} /> : <Pause size={14} />}
        </button>
      </header>
      {error && <p className="panel__error">Telemetry unreachable: {error}</p>}
      <table className="health">
        <thead>
          <tr>
            <th scope="col">Source</th>
            <th scope="col">Req</th>
            <th scope="col">Err</th>
            <th scope="col">Cache</th>
            <th scope="col">p50</th>
            <th scope="col">p95</th>
          </tr>
        </thead>
        <tbody>
          {sources.map((s) => {
            const errRate = s.requests ? s.errors / Math.max(1, s.requests - s.cacheHits) : 0;
            const status: PillStatus = !s.enabled ? "disabled" : !s.requests ? "idle" : errRate > 0.5 ? "error" : "ok";
            return (
              <tr key={s.source} title={s.lastError ? `Last error: ${s.lastError}` : ""}>
                <th scope="row">
                  <StatusPill status={status} /> {sourceLabel(s.source)}
                </th>
                <td className="mono">{s.requests}</td>
                <td className={`mono ${errRate > 0.2 ? "is-bad" : ""}`}>{s.errors}</td>
                <td className="mono">{s.cacheHits}</td>
                <td className="mono">{s.p50Ms ? Math.round(s.p50Ms) : "—"}</td>
                <td className="mono">{s.p95Ms ? Math.round(s.p95Ms) : "—"}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
      {updatedAt && <p className="panel__foot mono">updated {updatedAt.toLocaleTimeString()}</p>}
    </section>
  );
}

/** Latency of recent recommendation requests. */
export function Sparkline({ samples }: { samples: RequestSample[] }) {
  const W = 260;
  const H = 48;
  if (samples.length < 2) {
    return (
      <section className="panel" aria-labelledby="spark-h">
        <header className="panel__head">
          <h2 id="spark-h">Request latency</h2>
        </header>
        <p className="panel__empty">Run a few searches to see a trend.</p>
      </section>
    );
  }
  const max = Math.max(...samples.map((s) => s.tookMs), 1);
  const pts = samples.map((s, i) => [(i / (samples.length - 1)) * W, H - (s.tookMs / max) * (H - 6) - 3] as const);
  const d = pts.map(([x, y], i) => `${i ? "L" : "M"}${x.toFixed(1)},${y.toFixed(1)}`).join(" ");
  const last = samples[samples.length - 1];
  return (
    <section className="panel" aria-labelledby="spark-h">
      <header className="panel__head">
        <h2 id="spark-h">Request latency</h2>
        <span className="panel__meta mono">
          last {last.tookMs}ms · max {max}ms
        </span>
      </header>
      <svg viewBox={`0 0 ${W} ${H}`} className="spark" role="img" aria-label={`Latest request took ${last.tookMs} milliseconds`}>
        <path d={`${d} L${W},${H} L0,${H} Z`} className="spark__area" />
        <path d={d} className="spark__line" />
        {pts.map(([x, y], i) =>
          samples[i].steered ? <circle key={i} cx={x} cy={y} r={2.2} className="spark__steer" /> : null,
        )}
      </svg>
      <p className="panel__foot">
        <span className="legend-dot legend-dot--steer" aria-hidden /> steered request
      </p>
    </section>
  );
}

/** The learned ranker's weights versus their priors. */
export function LearnerPanel({ learner, onReset }: { learner?: LearnerSnapshot; onReset: () => void }) {
  if (!learner) return null;
  const maxAbs = Math.max(...learner.weights.map((w) => Math.abs(w.weight)), Math.abs(learner.bias), 1);
  return (
    <section className="panel" aria-labelledby="learn-h">
      <header className="panel__head">
        <h2 id="learn-h">Ranker</h2>
        <span className="panel__meta mono">
          {learner.likes}▲ {learner.skips}▼
        </span>
        <button className="icon-btn" onClick={onReset} aria-label="Reset learned weights to priors">
          <RotateCcw size={14} />
        </button>
      </header>
      <ul className="weights">
        {learner.weights.map((w) => {
          const pct = (Math.abs(w.weight) / maxAbs) * 50;
          const prior = 50 + (w.prior / maxAbs) * 50;
          return (
            <li key={w.signal} className="weights__row">
              <span className="weights__name mono">{w.signal}</span>
              <span className="weights__bar" aria-hidden>
                <span
                  className={`weights__fill ${w.weight < 0 ? "is-neg" : ""}`}
                  style={w.weight >= 0 ? { left: "50%", width: `${pct}%` } : { right: "50%", width: `${pct}%` }}
                />
                <span className="weights__prior" style={{ left: `${prior}%` }} />
              </span>
              <span className="weights__val mono">{w.weight.toFixed(2)}</span>
            </li>
          );
        })}
      </ul>
      <p className="panel__foot">Like or skip results to train it. Tick = prior.</p>
    </section>
  );
}
