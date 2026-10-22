import { useEffect, useRef } from "react";
import { usePrefersReducedMotion } from "../hooks/hooks";

/**
 * The empty-state centrepiece: a vinyl record turning in mid-air above the
 * search box. It tilts toward the cursor (the reflections follow the light),
 * can be grabbed and spun by hand, keeps its momentum when let go, and hops
 * when clicked. Typing in the search gives it a small push.
 */

const LABEL = "#d4734a"; // the one colour on the record
const BASE_SPEED = 1.4; // rad/s when left alone

export function HomeVisual() {
  const root = useRef<HTMLDivElement>(null);
  const float = useRef<HTMLDivElement>(null);
  const glow = useRef<HTMLDivElement>(null);
  const cv = useRef<HTMLCanvasElement>(null);
  const reduced = usePrefersReducedMotion();

  useEffect(() => {
    const el = root.current, fl = float.current, gl = glow.current, canvas = cv.current;
    if (!el || !fl || !gl || !canvas) return;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;

    let D = 0, R = 0, dpr = 1;
    let grooves: HTMLCanvasElement | null = null;
    let label: HTMLCanvasElement | null = null;

    // Static textures, rebuilt on resize: they only ever get rotated.
    const build = () => {
      const rect = canvas.getBoundingClientRect();
      dpr = Math.min(2, window.devicePixelRatio || 1);
      D = rect.width;
      R = D / 2;
      canvas.width = canvas.height = Math.round(D * dpr);

      grooves = document.createElement("canvas");
      grooves.width = grooves.height = canvas.width;
      const g = grooves.getContext("2d")!;
      g.scale(dpr, dpr);
      g.translate(R, R);
      const body = g.createRadialGradient(0, 0, R * 0.3, 0, 0, R);
      body.addColorStop(0, "#191919");
      body.addColorStop(1, "#0e0e0e");
      g.fillStyle = body;
      g.beginPath();
      g.arc(0, 0, R, 0, Math.PI * 2);
      g.fill();
      // Grooves, with a few smooth gaps between tracks.
      const gaps = [0.52, 0.66, 0.79, 0.88];
      for (let r = R * 0.36; r < R * 0.95; r += Math.max(0.9, R / 150)) {
        const u = r / R;
        if (gaps.some((gp) => Math.abs(u - gp) < 0.008)) continue;
        g.strokeStyle = `rgba(255,255,255,${0.025 + Math.random() * 0.045})`;
        g.lineWidth = 0.6;
        g.beginPath();
        g.arc(0, 0, r, 0, Math.PI * 2);
        g.stroke();
      }
      // A little dust, so the turning reads even in the grooves.
      for (let i = 0; i < 26; i++) {
        const a = Math.random() * Math.PI * 2, r = R * (0.38 + Math.random() * 0.56);
        g.fillStyle = `rgba(255,255,255,${0.06 + Math.random() * 0.08})`;
        g.beginPath();
        g.arc(Math.cos(a) * r, Math.sin(a) * r, 0.5 + Math.random() * 0.6, 0, Math.PI * 2);
        g.fill();
      }
      g.strokeStyle = "rgba(255,255,255,0.10)";
      g.lineWidth = 1.2;
      g.beginPath();
      g.arc(0, 0, R * 0.985, 0, Math.PI * 2);
      g.stroke();

      const RL = R * 0.33;
      label = document.createElement("canvas");
      label.width = label.height = Math.ceil(RL * 2 * dpr);
      const l = label.getContext("2d")!;
      l.scale(dpr, dpr);
      l.translate(RL, RL);
      const paper = l.createRadialGradient(-RL * 0.3, -RL * 0.3, 0, 0, 0, RL);
      paper.addColorStop(0, "#e08a62");
      paper.addColorStop(1, LABEL);
      l.fillStyle = paper;
      l.beginPath();
      l.arc(0, 0, RL, 0, Math.PI * 2);
      l.fill();
      l.strokeStyle = "rgba(0,0,0,0.22)";
      l.lineWidth = 1;
      l.beginPath();
      l.arc(0, 0, RL * 0.9, 0, Math.PI * 2);
      l.stroke();
      l.fillStyle = "rgba(20,10,6,0.85)";
      l.textAlign = "center";
      l.textBaseline = "middle";
      l.font = `600 ${Math.round(RL * 0.3)}px Inter, system-ui, sans-serif`;
      l.fillText("auris", 0, -RL * 0.42);
      l.font = `500 ${Math.round(RL * 0.13)}px Inter, system-ui, sans-serif`;
      l.fillText("33⅓  ·  SIDE A", 0, RL * 0.5);
      l.fillRect(-RL * 0.5, RL * 0.3, RL * 1.0, 0.8);
    };
    build();
    const ro = new ResizeObserver(build);
    ro.observe(canvas);

    // --- motion state ------------------------------------------------------
    let angle = 0, vel = reduced ? 0 : BASE_SPEED;
    let tiltX = 0, tiltY = 0, hop = 0;
    const ptr = { x: -9999, y: -9999 };
    const drag = { on: false, last: 0, at: 0, moved: 0, start: 0, id: -1 };

    const centre = () => {
      const r = canvas.getBoundingClientRect();
      return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
    };
    const onMove = (e: PointerEvent) => {
      ptr.x = e.clientX;
      ptr.y = e.clientY;
      if (!drag.on || e.pointerId !== drag.id) return;
      const c = centre();
      const a = Math.atan2(e.clientY - c.y, e.clientX - c.x);
      let d = a - drag.last;
      if (d > Math.PI) d -= Math.PI * 2;
      if (d < -Math.PI) d += Math.PI * 2;
      const now = performance.now();
      const dt = Math.max(8, now - drag.at) / 1000;
      angle += d;
      vel = vel * 0.5 + (d / dt) * 0.5; // smoothed hand speed
      drag.last = a;
      drag.at = now;
      drag.moved += Math.abs(d);
    };
    const onDown = (e: PointerEvent) => {
      const c = centre();
      if (Math.hypot(e.clientX - c.x, e.clientY - c.y) > R) return;
      e.preventDefault();
      canvas.setPointerCapture(e.pointerId);
      drag.on = true;
      drag.id = e.pointerId;
      drag.last = Math.atan2(e.clientY - c.y, e.clientX - c.x);
      drag.at = drag.start = performance.now();
      drag.moved = 0;
      vel *= 0.2; // a hand on the record slows it
      canvas.classList.add("is-grabbing");
    };
    const onUp = (e: PointerEvent) => {
      if (!drag.on || e.pointerId !== drag.id) return;
      drag.on = false;
      canvas.classList.remove("is-grabbing");
      if (drag.moved < 0.06 && performance.now() - drag.start < 300) {
        hop = 1;
        vel = Math.max(vel, BASE_SPEED) + 7;
      } else if (performance.now() - drag.at > 120) {
        vel = 0; // held still before letting go
      }
    };
    const onKey = (e: KeyboardEvent) => {
      if (!reduced && (e.key.length === 1 || e.key === "Backspace")) vel = Math.min(vel + 0.9, 12);
    };
    window.addEventListener("pointermove", onMove, { passive: true });
    canvas.addEventListener("pointerdown", onDown);
    canvas.addEventListener("pointerup", onUp);
    canvas.addEventListener("pointercancel", onUp);
    window.addEventListener("keydown", onKey);

    // --- frame ---------------------------------------------------------------
    const start = performance.now();
    let raf = 0, last = start;
    const frame = (now: number) => {
      const dt = Math.min(0.05, (now - last) / 1000);
      last = now;
      const t = (now - start) / 1000;

      if (!drag.on) {
        const base = reduced ? 0 : BASE_SPEED;
        vel += (base - vel) * (1 - Math.exp(-dt * 0.9));
        angle += vel * dt;
      }

      // Tilt toward the cursor while it's near the record.
      const c = centre();
      const dx = (ptr.x - c.x) / R, dy = (ptr.y - c.y) / R;
      const dist = Math.hypot(dx, dy);
      const near = Math.max(0, Math.min(1, (3.2 - dist) / 1.6));
      const clampd = (v: number) => Math.max(-1.2, Math.min(1.2, v));
      tiltY += (clampd(dx) * 16 * near - tiltY) * Math.min(1, dt * 6);
      tiltX += (-clampd(dy) * 16 * near - tiltX) * Math.min(1, dt * 6);

      hop = Math.max(0, hop - dt * 1.6);
      const bob = reduced ? 0 : Math.sin(t * 1.3) * 6;
      const lift = Math.sin(hop * Math.PI) * 22;
      fl.style.transform = `translateY(${bob - lift}px) rotateX(${tiltX}deg) rotateY(${tiltY}deg)`;
      const up = (bob - lift + 30) / 60; // 0 high … 1 low
      gl.style.transform = `translateX(-50%) scale(${0.75 + up * 0.35})`;
      gl.style.opacity = String(0.45 + up * 0.55);

      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      ctx.clearRect(0, 0, D, D);
      ctx.translate(R, R);

      // Record body.
      if (grooves) {
        ctx.save();
        ctx.rotate(angle);
        ctx.drawImage(grooves, -R, -R, D, D);
        ctx.restore();
      }

      // Reflections stay with the light, not the record; tilting moves the light.
      const light = -0.75 + (tiltY - tiltX) * 0.035;
      const shine = 0.7 + near * 0.5 + Math.min(0.5, Math.abs(vel) * 0.03);
      ctx.save();
      ctx.beginPath();
      ctx.arc(0, 0, R * 0.975, 0, Math.PI * 2);
      ctx.arc(0, 0, R * 0.345, 0, Math.PI * 2, true);
      ctx.clip();
      ctx.globalCompositeOperation = "lighter";
      const sheen = ctx.createConicGradient(light, 0, 0);
      const band = (at: number, hue: number, a: number) => {
        sheen.addColorStop(at - 0.07, "rgba(0,0,0,0)");
        sheen.addColorStop(at - 0.025, `hsla(${hue}, 70%, 70%, ${a * 0.5 * shine})`);
        sheen.addColorStop(at, `hsla(${hue + 30}, 40%, 88%, ${a * shine})`);
        sheen.addColorStop(at + 0.025, `hsla(${hue + 60}, 70%, 70%, ${a * 0.5 * shine})`);
        sheen.addColorStop(at + 0.07, "rgba(0,0,0,0)");
      };
      sheen.addColorStop(0, "rgba(0,0,0,0)");
      band(0.12, 190, 0.2); // cool on one side
      band(0.62, 15, 0.14); // warm on the other
      sheen.addColorStop(1, "rgba(0,0,0,0)");
      ctx.fillStyle = sheen;
      ctx.fillRect(-R, -R, D, D);
      ctx.restore();

      // Label.
      if (label) {
        const RL = R * 0.33;
        ctx.save();
        ctx.rotate(angle);
        ctx.drawImage(label, -RL, -RL, RL * 2, RL * 2);
        ctx.restore();
      }

      // Rim light on the side facing the light.
      ctx.save();
      ctx.globalCompositeOperation = "lighter";
      ctx.strokeStyle = `rgba(255,255,255,${0.1 + near * 0.12})`;
      ctx.lineWidth = 1.2;
      ctx.beginPath();
      ctx.arc(0, 0, R - 0.8, light + Math.PI * 0.05, light + Math.PI * 0.75);
      ctx.stroke();
      ctx.restore();

      // Spindle hole.
      ctx.globalCompositeOperation = "destination-out";
      ctx.beginPath();
      ctx.arc(0, 0, Math.max(2.5, R * 0.022), 0, Math.PI * 2);
      ctx.fill();
      ctx.globalCompositeOperation = "source-over";

      if (!document.hidden) raf = requestAnimationFrame(frame);
    };
    const onVis = () => {
      if (!document.hidden) {
        last = performance.now();
        cancelAnimationFrame(raf);
        raf = requestAnimationFrame(frame);
      }
    };
    document.addEventListener("visibilitychange", onVis);
    raf = requestAnimationFrame(frame);

    return () => {
      cancelAnimationFrame(raf);
      ro.disconnect();
      window.removeEventListener("pointermove", onMove);
      canvas.removeEventListener("pointerdown", onDown);
      canvas.removeEventListener("pointerup", onUp);
      canvas.removeEventListener("pointercancel", onUp);
      window.removeEventListener("keydown", onKey);
      document.removeEventListener("visibilitychange", onVis);
    };
  }, [reduced]);

  return (
    <div ref={root} className="vinyl" aria-hidden>
      <div ref={glow} className="vinyl__glow" />
      <div ref={float} className="vinyl__float">
        <canvas ref={cv} className="vinyl__disc" />
      </div>
    </div>
  );
}
