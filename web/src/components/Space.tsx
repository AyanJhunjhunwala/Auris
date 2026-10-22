import { ArrowUpRight, Heart, Pause, Play } from "lucide-react";
import { useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import type { Recommendation, Suggestion, Track } from "../api/types";
import { art } from "../hooks/hooks";

interface Props {
  seed: Track | null;
  /** The song being travelled to, shown at the centre while it loads. */
  next: Track | null;
  branches: Suggestion[];
  active: string | null;
  pending: Set<string>; // branches still loading
  nodes: Recommendation[]; // what to place: the active branch's results, or the neighbourhood
  loading: boolean;
  selected: string | null;
  playing: string | null;
  saved: Set<string>;
  onBranch: (id: string) => void;
  onSelect: (t: Track) => void;
  onTravel: (t: Track) => void;
  onPlay: (t: Track) => void;
  onToggleSave: (t: Track) => void;
  onPrime: (url?: string) => void;
  onMenu: (t: Track, x: number, y: number) => void;
  children?: ReactNode; // empty state
}

interface Placed {
  rec: Recommendation;
  x: number;
  y: number;
  s: number; // diameter
}

const clamp = (v: number, lo: number, hi: number) => Math.max(lo, Math.min(hi, v));

/**
 * Spatial search. The song sits at the centre; each suggestion is a direction
 * radiating out of it. Taking a direction fans its results out along that
 * line, nearer the centre the closer the match. With no direction taken,
 * the nearest neighbours fill the space between the lines.
 */
export function Space(p: Props) {
  const ref = useRef<HTMLDivElement>(null);
  const [box, setBox] = useState({ w: 1100, h: 720 });

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver(([e]) => setBox({ w: e.contentRect.width, h: e.contentRect.height }));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const { w, h } = box;
  const small = w < 640;
  // Keep clear of the header (top) and the dock (bottom).
  const top = small ? 76 : 72;
  const bottom = small ? 84 : 76;
  const cx = w / 2;
  const cy = top + (h - top - bottom) / 2;
  const cover = clamp(Math.min(w, h - top - bottom) * 0.27, 120, 232);
  const coverR = cover / 2;
  // Directions end on an ellipse so wide screens give side branches room.
  const Ry = Math.max(coverR + 96, (h - top - bottom) / 2 - 20);
  const Rx = Math.max(coverR + 96, Math.min(w / 2 - (small ? 52 : 100), Ry * 1.5));
  // Each direction points at its label on the ellipse.
  const dirOf = (i: number) => {
    const t = -Math.PI / 2 + (i * 2 * Math.PI) / Math.max(p.branches.length, 1);
    const dx = Rx * Math.cos(t);
    const dy = Ry * Math.sin(t);
    const len = Math.hypot(dx, dy);
    return { ux: dx / len, uy: dy / len, len, angle: Math.atan2(dy, dx) };
  };
  const radiusAt = (a: number) => Math.hypot(Rx * Math.cos(a), Ry * Math.sin(a));
  // The title/artist block under the cover is an obstacle too.
  const meta = { x0: cx - 150, x1: cx + 150, y0: cy + coverR + 6, y1: cy + coverR + 66 };
  const n = Math.max(p.branches.length, 1);
  const activeIdx = p.branches.findIndex((b) => b.id === p.active);

  const placed = useMemo<Placed[]>(() => {
    const scale = small ? 0.78 : 1;
    // A direction still loading gets shimmering placeholders on its line.
    const ghost = activeIdx >= 0 && p.nodes.length === 0 && !!p.active && p.pending.has(p.active);
    const list: Recommendation[] = ghost
      ? Array.from({ length: 8 }, (_, i) => ({
          track: { key: `ghost-${i}`, title: "", artist: "", ids: {} },
          score: 0.7,
          evidence: [],
          breakdown: { signals: {}, logit: 0 },
          reasons: [],
        }))
      : p.nodes.slice(0, activeIdx >= 0 ? 10 : 15);
    // Along a taken direction: a grid laid on the line, rows stepping
    // outward (best match nearest the centre), as many columns as the
    // line's length needs.
    const r0 = coverR + 52;
    const cell = 62 * scale;
    const Ra = activeIdx >= 0 ? dirOf(activeIdx).len : Ry;
    const L = Math.max(0, Ra - 44 - cell / 2 - r0 - cell / 2); // stop short of the label
    const maxRows = Math.max(1, Math.floor(L / (cell + 6)) + 1);
    const cols = clamp(Math.ceil(list.length / maxRows), 2, 5);
    const rows = Math.ceil(list.length / cols);
    const out: Placed[] = list.map((rec, i) => {
      const s = (44 + rec.score * 22) * scale;
      let a: number, r: number;
      if (activeIdx >= 0) {
        const row = Math.floor(i / cols);
        const col = i % cols;
        const u = r0 + (rows > 1 ? (row / (rows - 1)) * L : 0) + cell / 2;
        const v = (col - (cols - 1) / 2) * (cell + 8) + (row % 2 ? 0.25 : -0.25) * cell;
        const base = dirOf(activeIdx).angle;
        const x = cx + u * Math.cos(base) - v * Math.sin(base);
        const y = cy + u * Math.sin(base) + v * Math.cos(base);
        return { rec, x, y, s };
      } else {
        // Neighbourhood: spread through the gaps between directions.
        const gap = (2 * Math.PI) / n;
        const lane = Math.floor(i / n);
        a = dirOf(i % n).angle + gap / 2 + ((lane % 3) - 1) * gap * 0.2;
        r = coverR + 60 + (1 - rec.score) * (radiusAt(a) - coverR - 70);
      }
      return { rec, x: cx + r * Math.cos(a), y: cy + r * Math.sin(a), s };
    });
    // Nudge overlapping covers apart and keep them clear of the centre.
    for (let it = 0; it < 60; it++) {
      for (let i = 0; i < out.length; i++) {
        const A = out[i];
        for (let j = i + 1; j < out.length; j++) {
          const B = out[j];
          const dx = B.x - A.x || 0.01;
          const dy = B.y - A.y;
          const d = Math.hypot(dx, dy);
          const min = (A.s + B.s) / 2 + 10;
          if (d < min) {
            const push = (min - d) / 2;
            A.x -= (dx / d) * push;
            A.y -= (dy / d) * push;
            B.x += (dx / d) * push;
            B.y += (dy / d) * push;
          }
        }
        const dx = A.x - cx;
        const dy = A.y - cy;
        const d = Math.hypot(dx, dy) || 1;
        const min = coverR + A.s / 2 + 22;
        if (d < min) {
          A.x = cx + (dx / d) * min;
          A.y = cy + (dy / d) * min;
        }
        const half = A.s / 2 + 6;
        if (A.x > meta.x0 - half && A.x < meta.x1 + half && A.y > meta.y0 - half && A.y < meta.y1 + half) {
          // Leave the box by its nearest side.
          const exits = [A.x - (meta.x0 - half), meta.x1 + half - A.x, meta.y1 + half - A.y];
          const k = exits.indexOf(Math.min(...exits));
          if (k === 0) A.x = meta.x0 - half;
          else if (k === 1) A.x = meta.x1 + half;
          else A.y = meta.y1 + half;
        }
        A.x = clamp(A.x, A.s / 2 + 8, w - A.s / 2 - 8);
        A.y = clamp(A.y, top + A.s / 2, h - bottom - A.s / 2);
      }
    }
    return out;
  }, [p.nodes, p.pending, p.active, activeIdx, w, h, n, small, coverR, Rx, Ry, cx, cy, top, bottom]);

  const menu = (t: Track) => (e: React.MouseEvent) => {
    e.preventDefault();
    p.onMenu(t, e.clientX, e.clientY);
  };

  return (
    <div ref={ref} className={`space ${p.loading ? "is-loading" : ""} ${activeIdx >= 0 ? "has-active" : ""}`} aria-busy={p.loading}>
      <svg className="space__field" width={w} height={h} aria-hidden>
        {[0.42, 0.7, 1].map((k, i) => (
          <ellipse key={i} cx={cx} cy={cy} rx={coverR + (Rx - coverR) * k} ry={coverR + (Ry - coverR) * k} className="space__ring" />
        ))}
        {p.seed &&
          p.branches.map((b, i) => {
            const d = dirOf(i);
            const state = b.id === p.active ? "active" : p.pending.has(b.id) ? "loading" : "idle";
            // Start at the cover's edge (a square, so scale by the larger axis).
            const edge = coverR / Math.max(Math.abs(d.ux), Math.abs(d.uy)) + 10;
            const x1 = cx + edge * d.ux;
            const y1 = cy + edge * d.uy;
            const x2 = cx + (d.len - 22) * d.ux;
            const y2 = cy + (d.len - 22) * d.uy;
            return (
              <g key={b.id} className={`branch-line is-${state}`}>
                <line x1={x1} y1={y1} x2={x2} y2={y2} className="branch-line__base" />
                <line x1={x1} y1={y1} x2={x2} y2={y2} className="branch-line__flow" />
              </g>
            );
          })}
        {activeIdx >= 0 &&
          placed.map((n) => <line key={n.rec.track.key} x1={cx} y1={cy} x2={n.x} y2={n.y} className="space__thread" />)}
        {p.loading && (
          <g className="orbiter" style={{ transformOrigin: `${cx}px ${cy}px` }}>
            <circle cx={cx} cy={cy} r={coverR * 1.42} className="orbiter__track" />
            <circle cx={cx} cy={cy} r={coverR * 1.42} className="orbiter__arc" pathLength={100} />
          </g>
        )}
      </svg>

      {p.loading &&
        [0, 1, 2].map((i) => (
          <span
            key={i}
            className="ripple"
            style={{ left: cx, top: cy, width: cover, height: cover, animationDelay: `${i * 0.6}s` }}
            aria-hidden
          />
        ))}

      {p.seed &&
        p.branches.map((b, i) => {
          const d = dirOf(i);
          const isActive = b.id === p.active;
          return (
            <button
              key={b.id}
              className={`branch ${isActive ? "is-active" : ""} ${p.pending.has(b.id) ? "is-pending" : ""} branch--${b.kind}`}
              style={{ left: cx + d.len * d.ux, top: cy + d.len * d.uy, animationDelay: `${120 + i * 60}ms` }}
              onClick={() => p.onBranch(b.id)}
              aria-pressed={isActive}
            >
              {b.label}
            </button>
          );
        })}

      {placed.map(({ rec, x, y, s }, i) => {
        const t = rec.track;
        if (t.key.startsWith("ghost-")) {
          return (
            <div
              key={t.key}
              className="node is-ghost"
              aria-hidden
              style={{ "--x": `${x}px`, "--y": `${y}px`, "--cx": `${cx}px`, "--cy": `${cy}px`, "--s": `${s}px`, animationDelay: `${i * 60}ms` } as React.CSSProperties}
            />
          );
        }
        const sel = p.selected === t.key;
        const playing = p.playing === t.key;
        return (
          <div
            key={t.key}
            className={`node ${sel ? "is-selected" : ""} ${playing ? "is-playing" : ""}`}
            style={
              {
                "--x": `${x}px`,
                "--y": `${y}px`,
                "--cx": `${cx}px`,
                "--cy": `${cy}px`,
                "--s": `${s}px`,
                animationDelay: `${i * 35}ms`,
              } as React.CSSProperties
            }
          >
            <button
              className="node__art"
              onClick={() => p.onSelect(t)}
              onDoubleClick={() => p.onTravel(t)}
              onContextMenu={menu(t)}
              onPointerEnter={() => p.onPrime(t.previewUrl)}
              onFocus={() => p.onPrime(t.previewUrl)}
              aria-label={`${t.title}, ${t.artist}. ${Math.round(rec.score * 100)}% match`}
              aria-pressed={sel}
            >
              {t.artworkUrl ? <img src={art(t.artworkUrl, 120)} alt="" decoding="async" draggable={false} /> : <span className="node__blank" />}
            </button>
            <span className="node__cap" aria-hidden>
              <b>{t.title}</b>
              <span>{t.artist}</span>
            </span>
            {sel && (
              <span className="node__ctl">
                <button onClick={() => p.onPlay(t)} disabled={!t.previewUrl} aria-label={playing ? "Pause" : "Play preview"}>
                  {playing ? <Pause size={14} /> : <Play size={14} />}
                </button>
                <button onClick={() => p.onTravel(t)} aria-label={`Go to ${t.title}`}>
                  <ArrowUpRight size={14} />
                </button>
                <button
                  onClick={() => p.onToggleSave(t)}
                  className={p.saved.has(t.key) ? "is-on" : ""}
                  aria-label={p.saved.has(t.key) ? "Remove from library" : "Save"}
                >
                  <Heart size={14} />
                </button>
              </span>
            )}
          </div>
        );
      })}

      <div className="core" style={{ left: cx, top: cy, width: cover, height: cover }}>
        {p.loading && p.next ? (
          <div className="core__art is-arriving" key={`next-${p.next.key}`} aria-busy="true">
            {p.next.artworkUrl ? <img src={art(p.next.artworkUrl, 500)} alt="" draggable={false} /> : <span className="node__blank" />}
          </div>
        ) : p.seed ? (
          <button
            className={`core__art ${p.playing === p.seed.key ? "is-playing" : ""}`}
            onClick={() => p.onPlay(p.seed!)}
            onContextMenu={menu(p.seed)}
            onPointerEnter={() => p.onPrime(p.seed?.previewUrl)}
            aria-label={`${p.playing === p.seed.key ? "Pause" : "Play"} ${p.seed.title}`}
          >
            {p.seed.artworkUrl ? (
              <img
                src={art(p.seed.artworkUrl, 500)}
                alt=""
                draggable={false}
                // React 18 doesn't know fetchPriority; set the attribute directly.
                ref={(el) => el?.setAttribute("fetchpriority", "high")}
              />
            ) : (
              <span className="node__blank" />
            )}
          </button>
        ) : (
          <span className="core__empty" />
        )}
        {(p.loading && p.next ? p.next : p.seed) && (
          <div className={`core__meta ${p.loading ? "is-loading" : ""}`} aria-live="polite">
            <h1>{(p.loading && p.next ? p.next : p.seed)!.title}</h1>
            <p>{(p.loading && p.next ? p.next : p.seed)!.artist}</p>
          </div>
        )}
      </div>

      {p.children}
    </div>
  );
}
