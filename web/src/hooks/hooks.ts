import { useEffect, useRef, useState } from "react";
import { api } from "../api/client";
import type { TelemetrySnapshot } from "../api/types";

export function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setV(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return v;
}

/** Polls /api/telemetry; pauses while the tab is hidden or when paused. */
export function useTelemetry(intervalMs: number, paused: boolean, bump: unknown) {
  const [snap, setSnap] = useState<TelemetrySnapshot | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [updatedAt, setUpdatedAt] = useState<Date | null>(null);

  useEffect(() => {
    if (paused) return;
    let alive = true;
    const tick = async () => {
      if (document.hidden) return;
      try {
        const s = await api.telemetry();
        if (!alive) return;
        setSnap(s);
        setError(null);
        setUpdatedAt(new Date());
      } catch (e) {
        if (alive) setError((e as Error).message);
      }
    };
    tick();
    const id = setInterval(tick, intervalMs);
    return () => {
      alive = false;
      clearInterval(id);
    };
  }, [intervalMs, paused, bump]);

  return { snap, error, updatedAt };
}

export function usePrefersReducedMotion(): boolean {
  const [reduced, setReduced] = useState(
    () => typeof window !== "undefined" && window.matchMedia("(prefers-reduced-motion: reduce)").matches,
  );
  useEffect(() => {
    const mq = window.matchMedia("(prefers-reduced-motion: reduce)");
    const on = () => setReduced(mq.matches);
    mq.addEventListener("change", on);
    return () => mq.removeEventListener("change", on);
  }, []);
  return reduced;
}

/**
 * 30-second previews that start instantly: hovering a song calls prime(),
 * which begins buffering its mp3 in a small pool, so the click only has to
 * press play.
 */
export function usePreview() {
  const pool = useRef(new Map<string, HTMLAudioElement>());
  const current = useRef<HTMLAudioElement | null>(null);
  const [playing, setPlaying] = useState<string | null>(null);

  useEffect(() => () => current.current?.pause(), []);

  const get = (url: string) => {
    let a = pool.current.get(url);
    if (!a) {
      a = new Audio();
      a.preload = "auto";
      a.src = url;
      pool.current.set(url, a);
      // Keep the pool small; drop the oldest idle element.
      if (pool.current.size > 8) {
        const [oldUrl, old] = pool.current.entries().next().value as [string, HTMLAudioElement];
        if (old !== current.current) {
          old.src = "";
          pool.current.delete(oldUrl);
        }
      }
    }
    return a;
  };

  const prime = (url?: string) => {
    if (url) get(url);
  };

  const toggle = (key: string, url?: string) => {
    if (!url) return;
    if (playing === key) {
      current.current?.pause();
      setPlaying(null);
      return;
    }
    current.current?.pause();
    const a = get(url);
    a.currentTime = 0;
    a.volume = 0.85;
    a.onended = () => setPlaying(null);
    current.current = a;
    a.play().then(
      () => setPlaying(key),
      () => setPlaying(null),
    );
  };

  return { playing, toggle, prime };
}

/** Requests a smaller rendition of CDN cover art (Deezer, Cover Art Archive). */
export function art(url: string | undefined, px: number): string | undefined {
  if (!url) return url;
  if (url.includes("dzcdn.net")) return url.replace(/\/(\d+)x\1-/, `/${px}x${px}-`);
  if (url.includes("coverartarchive.org")) return url.replace(/-(250|500|1200)\.jpg$/, px > 250 ? "-500.jpg" : "-250.jpg");
  return url;
}

/**
 * Adds `is-visible` to elements carrying [data-reveal] as they scroll into
 * view, so cards pop and light up while you browse. Re-scans when `deps`
 * change (new results). Under reduced motion CSS shows them immediately.
 */
export function useReveal(deps: unknown[]) {
  useEffect(() => {
    const els = Array.from(document.querySelectorAll<HTMLElement>("[data-reveal]:not(.is-visible)"));
    if (!("IntersectionObserver" in window)) {
      els.forEach((el) => el.classList.add("is-visible"));
      return;
    }
    const io = new IntersectionObserver(
      (entries) => {
        for (const e of entries) {
          if (e.isIntersecting) {
            e.target.classList.add("is-visible");
            io.unobserve(e.target);
          }
        }
      },
      { rootMargin: "0px 0px -8% 0px", threshold: 0.15 },
    );
    els.forEach((el) => io.observe(el));
    return () => io.disconnect();
  }, deps);
}
