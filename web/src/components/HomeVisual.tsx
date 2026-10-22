import { useEffect, useRef } from "react";
import { usePrefersReducedMotion } from "../hooks/hooks";

/**
 * The empty-state backdrop: endless sheet music. A few staves scroll right to
 * left with a generated melody. Hovering a staff speeds it up and wakes it:
 * the lines bend toward the cursor, notes near it lift and take colour, and
 * every note that crosses the cursor "plays" (a ripple runs down the staff).
 * Clicking a staff plucks it like a string and turns on soft sound; typing in
 * the search nudges the whole score forward.
 */

type Kind = "w" | "h" | "q" | "e" | "r";
type Note = { x: number; steps: number[]; kind: Kind; dir: 1 | -1; pair?: Note; lead?: boolean; lift: number; lit: number };
type Pluck = { x: number; t0: number; amp: number };
type Spark = { x: number; y: number; vx: number; vy: number; life: number; hue: number };
type Staff = {
  y0: number;
  speed: number;
  base: number;
  offset: number;
  cursor: number; // score position of the next bar to generate
  last: number; // last melody step
  notes: Note[];
  bars: number[];
  plucks: Pluck[];
  hover: number; // 0..1, smoothed
  phase: number;
};

// Bars as rhythm tokens (each sums to 4 beats). "ee" is a beamed pair of eighths.
const PATTERNS = [
  ["q", "q", "q", "q"], ["h", "q", "q"], ["q", "ee", "q", "q"], ["ee", "ee", "h"], ["h", "h"],
  ["w"], ["q", "r", "q", "q"], ["ee", "q", "ee", "q"], ["q", "q", "h"], ["ee", "ee", "ee", "q"],
];
const BEATS: Record<string, number> = { w: 4, h: 2, q: 1, r: 1, ee: 1 };
// Hue per pitch, drifting violet → cyan → pink → amber up the staff.
const HUES = [265, 220, 190, 160, 330, 300, 20, 40];
const hueOf = (step: number) => HUES[(((step + 4) % HUES.length) + HUES.length) % HUES.length];
// Diatonic C major; step 0 is the bottom line (E4).
const freqOf = (step: number) => {
  const d = step + 2; // C4 = -2
  const oct = Math.floor(d / 7);
  const semis = [0, 2, 4, 5, 7, 9, 11][((d % 7) + 7) % 7] + oct * 12;
  return 261.63 * Math.pow(2, semis / 12);
};

export function HomeVisual() {
  const ref = useRef<HTMLCanvasElement>(null);
  const reduced = usePrefersReducedMotion();

  useEffect(() => {
    const canvas = ref.current;
    if (!canvas) return;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;

    let w = 0, h = 0, dpr = 1, s = 10, beat = 50, pad = 24;
    let staves: Staff[] = [];

    const newStaff = (y0: number, i: number): Staff => ({
      y0, speed: 0, base: 26 + i * 6, offset: 0, cursor: 0, last: 4, notes: [], bars: [], plucks: [], hover: 0, phase: i * 1.7,
    });

    const layout = () => {
      const rect = canvas.getBoundingClientRect();
      dpr = Math.min(2, window.devicePixelRatio || 1);
      w = rect.width;
      h = rect.height;
      canvas.width = Math.round(w * dpr);
      canvas.height = Math.round(h * dpr);
      s = Math.max(7, Math.min(10, Math.min(w, h) * 0.011));
      beat = s * 5.2;
      pad = Math.max(16, w * 0.03);
      const gap = Math.max(84, Math.min(140, h * 0.15));
      const cy = h / 2;
      // Two staves above the search box and two below it.
      staves = [-1.75, -0.85, 0.85, 1.75].map((k, i) => {
        const st = newStaff(cy + k * gap, i);
        st.offset = -w * 0.15 - i * beat * 1.3; // stagger so the bars don't line up
        st.cursor = st.offset + w * 0.2;
        return st;
      }).filter((st) => st.y0 - 3 * s > 56 && st.y0 + 3 * s < h - 8);
    };
    layout();
    const ro = new ResizeObserver(layout);
    ro.observe(canvas);

    // --- melody ------------------------------------------------------------
    const walk = (st: Staff) => {
      const r = Math.random();
      const jump = r < 0.12 ? (Math.random() < 0.5 ? -3 : 4) : Math.round((Math.random() - 0.5) * 4);
      let n = st.last + jump;
      if (n < -3 || n > 10) n = st.last - jump;
      st.last = Math.max(-3, Math.min(10, n));
      return st.last;
    };
    const fill = (st: Staff) => {
      while (st.cursor < st.offset + w + beat * 4) {
        const bar = PATTERNS[Math.floor(Math.random() * PATTERNS.length)];
        let x = st.cursor + beat * 0.5;
        for (const tok of bar) {
          if (tok === "ee") {
            const a = walk(st), b = walk(st);
            const dir: 1 | -1 = (a + b) / 2 < 4 ? 1 : -1;
            const n1: Note = { x, steps: [a], kind: "e", dir, lift: 0, lit: 0, lead: true };
            const n2: Note = { x: x + beat * 0.5, steps: [b], kind: "e", dir, lift: 0, lit: 0 };
            n1.pair = n2;
            n2.pair = n1;
            st.notes.push(n1, n2);
          } else if (tok === "r") {
            st.notes.push({ x, steps: [4], kind: "r", dir: 1, lift: 0, lit: 0 });
          } else {
            const a = walk(st);
            // Long notes sometimes carry a third or a fifth.
            const steps = (tok === "h" || tok === "w") && Math.random() < 0.45 ? [a, a + (Math.random() < 0.5 ? 2 : 4)] : [a];
            const mid = steps.reduce((p, c) => p + c, 0) / steps.length;
            st.notes.push({ x, steps, kind: tok as Kind, dir: mid < 4 ? 1 : -1, lift: 0, lit: 0 });
          }
          x += beat * BEATS[tok];
        }
        st.cursor += beat * 4 + beat * 0.5;
        st.bars.push(st.cursor);
      }
      const gone = st.offset - beat * 2;
      while (st.notes.length && st.notes[0].x < gone) st.notes.shift();
      while (st.bars.length && st.bars[0] < gone) st.bars.shift();
    };

    // --- sound (only after a click on the score) ----------------------------
    let audio: AudioContext | null = null;
    const play = (steps: number[], loud = 1) => {
      if (!audio || audio.state !== "running") return;
      const t = audio.currentTime;
      for (const st of steps) {
        const o = audio.createOscillator();
        const g = audio.createGain();
        o.type = "triangle";
        o.frequency.value = freqOf(st);
        g.gain.setValueAtTime(0, t);
        g.gain.linearRampToValueAtTime(0.035 * loud, t + 0.012);
        g.gain.exponentialRampToValueAtTime(0.0001, t + 0.7);
        o.connect(g).connect(audio.destination);
        o.start(t);
        o.stop(t + 0.75);
      }
    };

    // --- input ---------------------------------------------------------------
    const ptr = { x: -9999, y: -9999, sx: -9999, sy: -9999 };
    let surge = 0;
    const sparks: Spark[] = [];
    const local = (e: PointerEvent) => {
      const r = canvas.getBoundingClientRect();
      return { x: e.clientX - r.left, y: e.clientY - r.top };
    };
    const staffAt = (y: number) => staves.find((st) => Math.abs(y - st.y0) < s * 4.5);
    const onMove = (e: PointerEvent) => {
      const p = local(e);
      ptr.x = p.x;
      ptr.y = p.y;
      if (ptr.sx < -1000) { ptr.sx = p.x; ptr.sy = p.y; }
    };
    const onLeave = () => { ptr.x = ptr.y = -9999; };
    const onDown = (e: PointerEvent) => {
      const t = e.target as Element | null;
      if (t?.closest?.("input, button, a, header, aside, [role=dialog], [role=listbox], .search")) return;
      const p = local(e);
      const st = staffAt(p.y);
      if (!st) return;
      if (!audio) {
        try {
          audio = new AudioContext();
        } catch {
          /* no audio: visuals only */
        }
      }
      audio?.resume();
      st.plucks.push({ x: p.x, t0: performance.now(), amp: Math.max(-s * 2.2, Math.min(s * 2.2, (p.y - st.y0) * 0.9)) || s });
      // Every note under the cursor jumps and sounds together.
      const hit: number[] = [];
      for (const n of st.notes) {
        const sx = n.x - st.offset;
        if (Math.abs(sx - p.x) < beat * 1.6 && n.kind !== "r") {
          n.lit = 1;
          n.lift = -s * 1.4;
          hit.push(...n.steps);
          burst(sx, noteY(st, n.steps[0], sx), hueOf(n.steps[0]), 5);
        }
      }
      play(hit.slice(0, 4), 1.2);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key.length === 1 || e.key === "Backspace") surge = Math.min(3, surge + 0.6);
    };
    window.addEventListener("pointermove", onMove, { passive: true });
    document.addEventListener("pointerleave", onLeave);
    window.addEventListener("pointerdown", onDown);
    window.addEventListener("keydown", onKey);

    const burst = (x: number, y: number, hue: number, n: number) => {
      for (let i = 0; i < n; i++) {
        const a = -Math.PI / 2 + (Math.random() - 0.5) * 1.6;
        const v = 0.8 + Math.random() * 1.8;
        sparks.push({ x, y, vx: Math.cos(a) * v, vy: Math.sin(a) * v, life: 1, hue });
      }
      if (sparks.length > 220) sparks.splice(0, sparks.length - 220);
    };

    // --- geometry ------------------------------------------------------------
    let now = performance.now();
    /** Vertical displacement of a staff at screen x: drift + plucks + cursor pull. */
    const disp = (st: Staff, x: number) => {
      const t = now / 1000;
      let d = Math.sin(x * 0.0045 + t * 0.5 + st.phase) * s * 0.5;
      for (const p of st.plucks) {
        const age = (now - p.t0) / 1000;
        d += p.amp * Math.exp(-age * 1.7) * Math.sin((x - p.x) * 0.055 - age * 22) * Math.exp(-Math.abs(x - p.x) / (60 + age * 520));
      }
      if (st.hover > 0.01) {
        const pull = Math.max(-s * 3, Math.min(s * 3, ptr.sy - st.y0)) * 0.45;
        d += pull * st.hover * Math.exp(-(((x - ptr.sx) / 130) ** 2));
      }
      return d;
    };
    const noteY = (st: Staff, step: number, x: number) => st.y0 + 2 * s - step * (s / 2) + disp(st, x);

    // --- drawing ---------------------------------------------------------------
    const ink = (alpha: number) => `rgba(250,250,250,${alpha})`;

    const drawLines = (st: Staff, from: number, to: number, alpha: number) => {
      ctx.strokeStyle = ink(alpha);
      ctx.lineWidth = 1;
      for (let k = 0; k < 5; k++) {
        ctx.beginPath();
        for (let x = from; x <= to + 8; x += 8) {
          const y = st.y0 + (k - 2) * s + disp(st, x);
          x === from ? ctx.moveTo(x, y) : ctx.lineTo(x, y);
        }
        ctx.stroke();
      }
    };

    const drawClef = (x: number, st: Staff) => {
      const gy = st.y0 + s + disp(st, x); // the G line
      ctx.save();
      ctx.translate(x, gy);
      ctx.scale(s, s);
      ctx.lineWidth = 0.16;
      ctx.lineCap = "round";
      ctx.strokeStyle = ink(0.85);
      ctx.beginPath();
      ctx.moveTo(0.15, 0.35);
      ctx.bezierCurveTo(-0.35, 0.4, -0.4, -0.45, 0.1, -0.5);
      ctx.bezierCurveTo(0.75, -0.55, 0.85, 0.55, 0.15, 0.85);
      ctx.bezierCurveTo(-0.75, 1.2, -1.1, 0.05, -0.6, -0.6);
      ctx.bezierCurveTo(-0.2, -1.2, 0.6, -1.9, 0.6, -3.0);
      ctx.bezierCurveTo(0.6, -3.9, 0.15, -4.4, -0.05, -4.2);
      ctx.bezierCurveTo(-0.45, -3.8, -0.35, -2.6, -0.1, -1.6);
      ctx.lineTo(0.35, 2.3);
      ctx.bezierCurveTo(0.45, 2.9, -0.05, 3.25, -0.4, 2.95);
      ctx.stroke();
      ctx.fillStyle = ink(0.85);
      ctx.beginPath();
      ctx.arc(-0.38, 2.72, 0.28, 0, Math.PI * 2);
      ctx.fill();
      ctx.restore();
      // 4/4
      ctx.fillStyle = ink(0.8);
      ctx.font = `700 ${Math.round(s * 2.1)}px Georgia, "Times New Roman", serif`;
      ctx.textAlign = "center";
      ctx.textBaseline = "middle";
      const tx = x + s * 2.6;
      ctx.fillText("4", tx, st.y0 - s + disp(st, tx));
      ctx.fillText("4", tx, st.y0 + s + disp(st, tx));
    };

    const stemEnd = (st: Staff, n: Note, sx: number) => {
      const ys = n.steps.map((p) => noteY(st, p, sx) + n.lift);
      const far = n.dir === 1 ? Math.min(...ys) : Math.max(...ys);
      return { x: sx + (n.dir === 1 ? s * 0.58 : -s * 0.58), y0: n.dir === 1 ? Math.max(...ys) : Math.min(...ys), y1: far - n.dir * s * 3.4 };
    };

    const drawNote = (st: Staff, n: Note, sx: number) => {
      const near = st.hover * Math.exp(-(((sx - ptr.sx) / (beat * 1.4)) ** 2));
      const glow = Math.max(n.lit, near * 0.8);
      const hue = hueOf(n.steps[0]);
      const col = glow > 0.05 ? `hsla(${hue}, ${60 + glow * 30}%, ${80 - glow * 12}%, ${0.6 + glow * 0.4})` : ink(0.62);
      ctx.strokeStyle = col;
      ctx.fillStyle = col;
      const scale = 1 + glow * 0.25;

      if (n.kind === "r") {
        const y = st.y0 + disp(st, sx) + n.lift;
        ctx.lineWidth = s * 0.22;
        ctx.beginPath();
        ctx.moveTo(sx - s * 0.3, y - s * 1.4);
        ctx.lineTo(sx + s * 0.35, y - s * 0.6);
        ctx.lineTo(sx - s * 0.25, y + s * 0.1);
        ctx.lineTo(sx + s * 0.35, y + s * 0.8);
        ctx.quadraticCurveTo(sx - s * 0.6, y + s * 0.5, sx - s * 0.05, y + s * 1.5);
        ctx.stroke();
        return;
      }

      if (glow > 0.15) {
        const y = noteY(st, n.steps[0], sx) + n.lift;
        const g = ctx.createRadialGradient(sx, y, 0, sx, y, s * 3.5);
        g.addColorStop(0, `hsla(${hue}, 85%, 60%, ${0.22 * glow})`);
        g.addColorStop(1, "hsla(0,0%,0%,0)");
        ctx.fillStyle = g;
        ctx.fillRect(sx - s * 3.5, y - s * 3.5, s * 7, s * 7);
        ctx.fillStyle = col;
      }

      for (const p of n.steps) {
        const y = noteY(st, p, sx) + n.lift;
        // Ledger lines below/above the staff.
        ctx.lineWidth = 1;
        for (let l = -2; l >= p; l -= 2) {
          const ly = noteY(st, l, sx) + n.lift;
          ctx.beginPath();
          ctx.moveTo(sx - s * 1.05, ly);
          ctx.lineTo(sx + s * 1.05, ly);
          ctx.stroke();
        }
        for (let l = 10; l <= p; l += 2) {
          const ly = noteY(st, l, sx) + n.lift;
          ctx.beginPath();
          ctx.moveTo(sx - s * 1.05, ly);
          ctx.lineTo(sx + s * 1.05, ly);
          ctx.stroke();
        }
        ctx.beginPath();
        ctx.ellipse(sx, y, s * 0.66 * scale, s * 0.46 * scale, -0.35, 0, Math.PI * 2);
        if (n.kind === "h" || n.kind === "w") {
          ctx.lineWidth = s * (n.kind === "w" ? 0.24 : 0.18);
          ctx.stroke();
        } else ctx.fill();
      }

      if (n.kind === "w") return;
      const e = stemEnd(st, n, sx);
      ctx.lineWidth = Math.max(1, s * 0.12);
      ctx.beginPath();
      ctx.moveTo(e.x, e.y0);
      ctx.lineTo(e.x, n.kind === "e" && n.pair ? beamY(st, n) : e.y1);
      ctx.stroke();

      if (n.kind === "e" && n.lead && n.pair) {
        const px = n.pair.x - st.offset;
        const a = stemEnd(st, n, sx), b = stemEnd(st, n.pair, px);
        const ya = beamY(st, n), yb = beamY(st, n.pair);
        ctx.lineWidth = s * 0.5;
        ctx.lineCap = "butt";
        ctx.beginPath();
        ctx.moveTo(a.x, ya + n.dir * s * 0.2);
        ctx.lineTo(b.x, yb + n.dir * s * 0.2);
        ctx.stroke();
        ctx.lineCap = "round";
      }
    };
    /** Beam height for a beamed eighth: the pair's stem ends, with the slope limited. */
    const beamY = (st: Staff, n: Note) => {
      const lead = n.lead ? n : n.pair!;
      const other = lead.pair!;
      const la = stemEnd(st, lead, lead.x - st.offset).y1;
      const lb = stemEnd(st, other, other.x - st.offset).y1;
      const mid = lead.dir === 1 ? Math.min(la, lb) : Math.max(la, lb);
      const slope = Math.max(-s * 0.8, Math.min(s * 0.8, (lb - la) / 2));
      return n === lead ? mid - slope * 0.5 : mid + slope * 0.5;
    };

    const start = performance.now();
    let raf = 0, last = start;

    const frame = (t: number) => {
      now = t;
      const dt = Math.min(50, t - last) / 1000;
      last = t;
      ptr.sx += (ptr.x - ptr.sx) * Math.min(1, dt * 14);
      ptr.sy += (ptr.y - ptr.sy) * Math.min(1, dt * 14);
      surge *= Math.pow(0.25, dt);

      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      ctx.clearRect(0, 0, w, h);
      const panel = pad + s * 4.4;

      for (const st of staves) {
        const hovered = ptr.x > -1000 && Math.abs(ptr.y - st.y0) < s * 4.5 && ptr.x > panel;
        st.hover += ((hovered ? 1 : 0) - st.hover) * Math.min(1, dt * 6);
        const target = st.base * (1 + surge * 0.8) + st.hover * 190;
        st.speed += (target - st.speed) * Math.min(1, dt * 3);
        const prev = st.offset;
        st.offset += st.speed * dt;
        st.plucks = st.plucks.filter((p) => now - p.t0 < 2600);
        fill(st);

        // Notes that cross the cursor play while the staff is awake.
        if (st.hover > 0.5) {
          for (const n of st.notes) {
            if (n.kind === "r") continue;
            const a = n.x - prev, b = n.x - st.offset;
            if (a > ptr.sx && b <= ptr.sx) {
              n.lit = 1;
              n.lift = -s * 0.9;
              st.plucks.push({ x: ptr.sx, t0: now, amp: s * 0.35 * (n.steps[0] % 2 ? 1 : -1) });
              burst(b, noteY(st, n.steps[0], b), hueOf(n.steps[0]), 3);
              play(n.steps);
            }
          }
        }

        // Playhead under the cursor.
        if (st.hover > 0.02) {
          const top = st.y0 - 3.2 * s + disp(st, ptr.sx), bot = st.y0 + 3.2 * s + disp(st, ptr.sx);
          const g = ctx.createLinearGradient(0, top, 0, bot);
          g.addColorStop(0, "rgba(255,255,255,0)");
          g.addColorStop(0.5, `rgba(255,255,255,${0.35 * st.hover})`);
          g.addColorStop(1, "rgba(255,255,255,0)");
          ctx.strokeStyle = g;
          ctx.lineWidth = 1;
          ctx.beginPath();
          ctx.moveTo(ptr.sx, top);
          ctx.lineTo(ptr.sx, bot);
          ctx.stroke();
        }

        // Bar lines and notes.
        ctx.lineWidth = 1;
        for (const bx of st.bars) {
          const x = bx - st.offset;
          if (x < panel - 10 || x > w + 10) continue;
          const d = disp(st, x);
          ctx.strokeStyle = ink(0.32);
          ctx.beginPath();
          ctx.moveTo(x, st.y0 - 2 * s + d);
          ctx.lineTo(x, st.y0 + 2 * s + d);
          ctx.stroke();
        }
        for (const n of st.notes) {
          const sx = n.x - st.offset;
          n.lift += (0 - n.lift) * Math.min(1, dt * 5);
          n.lit = Math.max(0, n.lit - dt * 1.1);
          if (sx < panel - 30 || sx > w + 30) continue;
          const near = st.hover * Math.exp(-(((sx - ptr.sx) / (beat * 1.4)) ** 2));
          n.lift = Math.min(n.lift, -near * s * 0.6);
          drawNote(st, n, sx);
        }
      }

      // Sparks rising off played notes.
      for (let i = sparks.length - 1; i >= 0; i--) {
        const p = sparks[i];
        p.vy += 0.6 * dt * 60 * 0.02;
        p.x += p.vx * dt * 60;
        p.y += p.vy * dt * 60;
        p.life -= dt * 1.2;
        if (p.life <= 0) { sparks.splice(i, 1); continue; }
        ctx.fillStyle = `hsla(${p.hue}, 85%, 72%, ${p.life * 0.8})`;
        ctx.beginPath();
        ctx.arc(p.x, p.y, 1.3, 0, Math.PI * 2);
        ctx.fill();
      }

      // Left panel (notes slide under the clef) and right fade.
      const lg = ctx.createLinearGradient(panel - 4, 0, panel + s * 5, 0);
      lg.addColorStop(0, "rgba(0,0,0,1)");
      lg.addColorStop(1, "rgba(0,0,0,0)");
      ctx.fillStyle = "#000";
      ctx.fillRect(0, 0, panel - 4, h);
      ctx.fillStyle = lg;
      ctx.fillRect(panel - 4, 0, s * 5 + 4, h);

      for (const st of staves) {
        drawLines(st, pad - s * 0.6, w, 0.16 + st.hover * 0.14);
        ctx.strokeStyle = ink(0.4);
        ctx.beginPath();
        ctx.moveTo(pad - s * 0.6, st.y0 - 2 * s + disp(st, pad - s * 0.6));
        ctx.lineTo(pad - s * 0.6, st.y0 + 2 * s + disp(st, pad - s * 0.6));
        ctx.stroke();
        drawClef(pad + s * 0.6, st);
      }

      const rg = ctx.createLinearGradient(w - Math.min(160, w * 0.15), 0, w, 0);
      rg.addColorStop(0, "rgba(0,0,0,0)");
      rg.addColorStop(1, "rgba(0,0,0,1)");
      ctx.fillStyle = rg;
      ctx.fillRect(w - Math.min(160, w * 0.15), 0, Math.min(160, w * 0.15), h);

      if (!reduced && !document.hidden) raf = requestAnimationFrame(frame);
    };

    const onVis = () => {
      if (!document.hidden && !reduced) {
        last = performance.now();
        cancelAnimationFrame(raf);
        raf = requestAnimationFrame(frame);
      }
    };
    document.addEventListener("visibilitychange", onVis);
    if (reduced) frame(start + 16);
    else raf = requestAnimationFrame(frame);

    return () => {
      cancelAnimationFrame(raf);
      ro.disconnect();
      window.removeEventListener("pointermove", onMove);
      document.removeEventListener("pointerleave", onLeave);
      window.removeEventListener("pointerdown", onDown);
      window.removeEventListener("keydown", onKey);
      document.removeEventListener("visibilitychange", onVis);
      audio?.close();
    };
  }, [reduced]);

  return <canvas ref={ref} className="home-visual" aria-hidden />;
}
