import { Activity, ArrowUpRight, ExternalLink, Heart, Pause, Play, Sparkles, ThumbsDown, ThumbsUp, X } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api } from "./api/client";
import type { AgentPlan, Health, LanguageOption, LibraryEntry, Recommendation, SeedQuery, SimilarResponse, Suggestion, Track } from "./api/types";
import { AgentPanel } from "./components/AgentPanel";
import { ContextMenu, type MenuItem } from "./components/ContextMenu";
import { Drawer } from "./components/Drawer";
import { LanguageMenu } from "./components/LanguageMenu";
import { Library } from "./components/Library";
import { Detail } from "./components/Results";
import { SearchBar } from "./components/SearchBar";
import { Space } from "./components/Space";
import { SpotifyExport } from "./components/SpotifyExport";
import { LearnerPanel, SourceHealth, SourceTrace, Sparkline } from "./components/Telemetry";
import { art, usePreview, useReveal, useTelemetry } from "./hooks/hooks";

const seedFor = (t: Track): SeedQuery =>
  t.ids.deezerId ? { deezerId: t.ids.deezerId } : { artist: t.artist, title: t.title, isrc: t.ids.isrc };

const planRequest = (seed: SeedQuery, plan: AgentPlan, languages?: string[]) => ({
  seed,
  limit: 12,
  languages,
  steer: plan.steer,
  targets: plan.targets,
  reference: plan.reference,
  referenceWeight: plan.referenceWeight,
});

/** Merge artwork/previews from /api/enrich into a response. */
function mergeEnriched(r: SimilarResponse, filled: Track[]): SimilarResponse {
  const byKey = new Map(filled.map((t) => [t.key, t]));
  return {
    ...r,
    recommendations: r.recommendations.map((rec) => {
      const f = byKey.get(rec.track.key);
      return f ? { ...rec, track: { ...rec.track, artworkUrl: rec.track.artworkUrl || f.artworkUrl, previewUrl: rec.track.previewUrl || f.previewUrl, ids: { ...f.ids, ...rec.track.ids } } } : rec;
    }),
  };
}

const LANG_KEY = "auris.language";
const readLang = () => {
  try {
    return localStorage.getItem(LANG_KEY);
  } catch {
    return null;
  }
};

export default function App() {
  const [seedQuery, setSeedQuery] = useState<SeedQuery | null>(null);
  const [next, setNext] = useState<Track | null>(null);
  const [language, setLanguage] = useState<string | null>(readLang);
  const [langOptions, setLangOptions] = useState<LanguageOption[]>([]);
  const [spotifyBump, setSpotifyBump] = useState(0);
  const languages = useMemo(() => (language ? [language] : undefined), [language]);
  const [base, setBase] = useState<SimilarResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const [branches, setBranches] = useState<Suggestion[]>([]);
  const [active, setActive] = useState<string | null>(null);
  const [branchRes, setBranchRes] = useState<Record<string, SimilarResponse>>({});
  const [pending, setPending] = useState<Set<string>>(new Set());
  const inflight = useRef(new Set<string>());

  const [selected, setSelected] = useState<string | null>(null);
  const [trail, setTrail] = useState<Track[]>([]);
  const [library, setLibrary] = useState<LibraryEntry[]>([]);
  const [health, setHealth] = useState<Health | null>(null);
  const [menu, setMenu] = useState<{ track: Track; x: number; y: number } | null>(null);
  const [agentAt, setAgentAt] = useState<{ track: Track; x: number; y: number } | null>(null);
  const [libOpen, setLibOpen] = useState(false);
  const [telOpen, setTelOpen] = useState(false);
  const [telPaused, setTelPaused] = useState(false);
  const [toast, setToast] = useState<string | null>(null);
  const [bump, setBump] = useState(0);

  const [ask, setAsk] = useState("");
  const [asking, setAsking] = useState(false);
  const [askReply, setAskReply] = useState("");

  const preview = usePreview();
  const { snap, error: telError, updatedAt } = useTelemetry(5000, telPaused || !telOpen, bump);

  const seed = base?.seed ?? null;
  const shown: SimilarResponse | null = (active && branchRes[active]) || base;
  const nodes: Recommendation[] = active ? branchRes[active]?.recommendations ?? [] : base?.recommendations ?? [];
  useReveal([shown?.requestId]);

  useEffect(() => {
    if (!toast) return;
    const t = setTimeout(() => setToast(null), 2200);
    return () => clearTimeout(t);
  }, [toast]);

  useEffect(() => {
    api.health().then(setHealth, (e: Error) => setError(`API unreachable: ${e.message}`));
    api.library.list().then((l) => setLibrary(l.entries), () => {});
    api.languages().then((l) => setLangOptions(l.languages), () => {});
    // Back from Spotify's login page.
    const params = new URLSearchParams(window.location.search);
    const sp = params.get("spotify");
    if (sp) {
      setToast(sp === "connected" ? "Spotify connected" : "Spotify not connected");
      setLibOpen(true);
      setSpotifyBump((b) => b + 1);
      params.delete("spotify");
      const q = params.toString();
      window.history.replaceState(null, "", window.location.pathname + (q ? `?${q}` : ""));
    }
  }, []);

  const changeLanguage = (code: string | null) => {
    setLanguage(code);
    try {
      if (code) localStorage.setItem(LANG_KEY, code);
      else localStorage.removeItem(LANG_KEY);
    } catch {
      /* private mode */
    }
    // Directions are re-fetched under the new filter.
    setBranchRes({});
    inflight.current.clear();
    setActive(null);
  };

  /** Artwork the server didn't wait for: ask again once it's cached. */
  const fillIn = useCallback((r: SimilarResponse, apply: (f: (prev: SimilarResponse) => SimilarResponse) => void) => {
    const missing = r.recommendations.filter((x) => !x.track.artworkUrl || !x.track.previewUrl).map((x) => x.track);
    if (!missing.length) return;
    setTimeout(() => {
      api.enrich(missing).then((res) => apply((prev) => mergeEnriched(prev, res.tracks)), () => {});
    }, 900);
  }, []);

  // 1. Seed → neighbourhood, then suggestions.
  useEffect(() => {
    if (!seedQuery) return;
    const ctl = new AbortController();
    setLoading(true);
    setError(null);
    api
      .similar({ seed: seedQuery, limit: 15, languages }, ctl.signal)
      .then((r) => {
        setBase(r);
        setNext(null);
        setTrail((t) => (t[t.length - 1]?.key === r.seed.key ? t : [...t.filter((x) => x.key !== r.seed.key), r.seed].slice(-8)));
        fillIn(r, (f) => setBase((prev) => (prev && prev.requestId === r.requestId ? f(prev) : prev)));
        setBump((b) => b + 1);
        return api.suggest(r.seed, ctl.signal);
      })
      .then((s) => setBranches(s.suggestions))
      .catch((e: Error) => {
        if (e.name !== "AbortError") {
          setError(e.message);
          setNext(null);
        }
      })
      .finally(() => !ctl.signal.aborted && setLoading(false));
    return () => ctl.abort();
  }, [seedQuery, languages, fillIn]);

  const runBranch = useCallback(
    async (b: Suggestion) => {
      if (!seed || inflight.current.has(b.id)) return;
      inflight.current.add(b.id);
      setPending((p) => new Set(p).add(b.id));
      try {
        const r = await api.similar(planRequest(seedFor(seed), b.plan, languages));
        setBranchRes((m) => ({ ...m, [b.id]: r }));
        fillIn(r, (f) => setBranchRes((m) => (m[b.id] ? { ...m, [b.id]: f(m[b.id]) } : m)));
      } catch {
        inflight.current.delete(b.id); // allow a retry on click
      } finally {
        setPending((p) => {
          const n = new Set(p);
          n.delete(b.id);
          return n;
        });
      }
    },
    [seed, languages, fillIn],
  );

  // 2. Prefetch every direction, two at a time, so taking one is instant.
  useEffect(() => {
    if (!branches.length || !seed) return;
    let cancelled = false;
    const queue = branches.filter((b) => b.kind !== "custom");
    const worker = async () => {
      while (!cancelled && queue.length) await runBranch(queue.shift()!);
    };
    worker();
    worker();
    return () => {
      cancelled = true;
    };
  }, [branches, seed, runBranch]);

  const travel = useCallback((t: Track) => {
    setNext(t);
    setSeedQuery(seedFor(t));
    setBranches([]);
    setBranchRes({});
    setActive(null);
    setSelected(null);
    inflight.current.clear();
    window.scrollTo({ top: 0, behavior: "smooth" });
  }, []);

  const onBranch = (id: string) => {
    setSelected(null);
    if (active === id) return setActive(null);
    setActive(id);
    const b = branches.find((x) => x.id === id);
    if (b && !branchRes[id]) runBranch(b);
  };

  // Free-form: "make it sound like…" becomes a new direction.
  const askAgent = async (prompt: string, track: Track | null) => {
    if (!prompt.trim() || !track) return;
    setAsking(true);
    setAskReply("");
    let plan: AgentPlan | null = null;
    try {
      await api.agent({ prompt, track }, (ev) => {
        if (ev.type === "delta") setAskReply((r) => r + ev.data.text);
        if (ev.type === "plan") plan = ev.data;
        if (ev.type === "result") addCustom(ev.data, plan, prompt);
        if (ev.type === "error") setAskReply(ev.data.message);
      });
      setAsk("");
    } catch (e) {
      setAskReply((e as Error).message);
    } finally {
      setAsking(false);
    }
  };

  const addCustom = useCallback(
    (r: SimilarResponse, plan: AgentPlan | null, prompt: string) => {
      if (base && r.seed.key !== base.seed.key) {
        // The agent moved to another song: travel there.
        travel(r.seed);
        return;
      }
      const id = `custom-${Date.now()}`;
      const label = plan?.label?.trim() || prompt.split(/\s+/).slice(0, 3).join(" ");
      setBranches((bs) => [...bs.filter((b) => b.kind !== "custom").slice(0, 5), { id, label, kind: "custom", plan: plan ?? {} }]);
      setBranchRes((m) => ({ ...m, [id]: r }));
      setActive(id);
      fillIn(r, (f) => setBranchRes((m) => (m[id] ? { ...m, [id]: f(m[id]) } : m)));
    },
    [base, travel, fillIn],
  );

  // --- library ---
  const saved = useMemo(() => new Set(library.map((e) => e.track.key)), [library]);
  const toggleSave = useCallback(
    async (t: Track) => {
      if (saved.has(t.key)) {
        setLibrary((l) => l.filter((e) => e.track.key !== t.key));
        api.library.remove(t.key).catch(() => setToast("Couldn't update library"));
        return;
      }
      const label = branches.find((b) => b.id === active)?.label;
      const entry = { track: t, from: seed ? `${seed.artist} – ${seed.title}` : undefined, vibe: label };
      setLibrary((l) => [{ ...entry, savedAt: new Date().toISOString() }, ...l]);
      setToast("Saved");
      api.library.add(entry).catch((e: Error) => {
        setLibrary((l) => l.filter((x) => x.track.key !== t.key));
        setToast(`Couldn't save: ${e.message}`);
      });
    },
    [saved, branches, active, seed],
  );

  const feedback = (t: Track, signal: "like" | "skip") => {
    if (!shown) return;
    api.feedback(shown.requestId, t.key, signal).then(
      () => (setToast(signal === "like" ? "More like this" : "Less like this"), setBump((b) => b + 1)),
      (e: Error) => setToast(e.message),
    );
  };

  const openMenu = useCallback((track: Track, x: number, y: number) => setMenu({ track, x, y }), []);
  const closeMenu = useCallback(() => setMenu(null), []);
  const closeAgent = useCallback(() => setAgentAt(null), []);

  const menuItems = (t: Track): MenuItem[] => {
    const inShown = !!shown?.recommendations.some((r) => r.track.key === t.key);
    return [
      {
        id: "play",
        label: preview.playing === t.key ? "Pause" : "Play",
        icon: preview.playing === t.key ? Pause : Play,
        disabled: !t.previewUrl,
        onSelect: () => preview.toggle(t.key, t.previewUrl),
      },
      { id: "go", label: "Go to song", icon: ArrowUpRight, disabled: t.key === seed?.key, onSelect: () => travel(t) },
      { id: "ask", label: "Make it sound like…", icon: Sparkles, onSelect: () => setAgentAt({ track: t, x: menu!.x, y: menu!.y }) },
      { id: "save", label: saved.has(t.key) ? "Saved" : "Save", icon: Heart, onSelect: () => toggleSave(t) },
      { id: "like", label: "More like this", icon: ThumbsUp, disabled: !inShown, onSelect: () => feedback(t, "like") },
      { id: "skip", label: "Less like this", icon: ThumbsDown, disabled: !inShown, onSelect: () => feedback(t, "skip") },
      { id: "open", label: "Open", icon: ExternalLink, disabled: !t.url, onSelect: () => window.open(t.url, "_blank", "noopener") },
    ];
  };

  const agentOn = !!health?.agent.enabled;
  const selectedRec = shown?.recommendations.find((r) => r.track.key === selected);

  return (
    <div className="app">
      <header className="bar">
        <button className="wordmark" onClick={() => window.scrollTo({ top: 0, behavior: "smooth" })}>
          auris
        </button>
        {seed && <SearchBar onPick={travel} onFreeText={(q) => setSeedQuery({ query: q })} compact />}
        <nav className="bar__nav" aria-label="Panels">
          {langOptions.length > 0 && <LanguageMenu options={langOptions} value={language} onChange={changeLanguage} />}
          <button className="icon-btn" onClick={() => setLibOpen(true)} aria-label={`Library, ${library.length} songs`}>
            <Heart size={18} />
            {library.length > 0 && <span className="badge">{library.length}</span>}
          </button>
          <button className="icon-btn" onClick={() => setTelOpen(true)} aria-label="Telemetry">
            <Activity size={18} />
          </button>
        </nav>
      </header>

      {error && (
        <div className="banner" role="alert">
          {error}
          <button className="icon-btn" onClick={() => setError(null)} aria-label="Dismiss">
            <X size={14} />
          </button>
        </div>
      )}

      <main className="stage">
        <Space
          seed={seed}
          next={next}
          branches={branches}
          active={active}
          pending={pending}
          nodes={nodes}
          loading={loading}
          selected={selected}
          playing={preview.playing}
          saved={saved}
          onBranch={onBranch}
          onSelect={(t) => {
            setSelected(t.key);
            if (preview.playing !== t.key) preview.toggle(t.key, t.previewUrl);
          }}
          onTravel={travel}
          onPlay={(t) => preview.toggle(t.key, t.previewUrl)}
          onToggleSave={toggleSave}
          onPrime={preview.prime}
          onMenu={openMenu}
        >
          {!seed && (
            <div className="stage__search">
              <SearchBar onPick={travel} onFreeText={(q) => setSeedQuery({ query: q })} />
            </div>
          )}
        </Space>

        {seed && (
          <div className="dock">
            {trail.length > 1 && (
              <ol className="trail" aria-label="History">
                {trail.slice(0, -1).map((t) => (
                  <li key={t.key}>
                    <button onClick={() => travel(t)} aria-label={`Back to ${t.title}`} title={`${t.title} · ${t.artist}`}>
                      {t.artworkUrl ? <img src={art(t.artworkUrl, 120)} alt="" /> : <span />}
                    </button>
                  </li>
                ))}
              </ol>
            )}
            {agentOn && (
              <form
                className={`ask ${asking ? "is-busy" : ""}`}
                onSubmit={(e) => {
                  e.preventDefault();
                  askAgent(ask, seed);
                }}
              >
                <Sparkles size={15} aria-hidden />
                <label htmlFor="ask" className="sr-only">
                  Describe a direction
                </label>
                <input id="ask" value={ask} onChange={(e) => setAsk(e.target.value)} placeholder="Make it sound like…" disabled={asking} autoComplete="off" />
                {askReply && <p className="ask__reply" aria-live="polite">{askReply}</p>}
              </form>
            )}
          </div>
        )}
      </main>

      {nodes.length > 0 && (
        <section className="list" aria-labelledby="list-h">
          <h2 id="list-h" className="sr-only">
            Results
          </h2>
          <ol>
            {nodes.map((r, i) => (
              <li
                key={r.track.key}
                data-reveal
                style={{ "--i": i % 10 } as React.CSSProperties}
                className={`row ${preview.playing === r.track.key ? "is-playing" : ""}`}
                onContextMenu={(e) => (e.preventDefault(), openMenu(r.track, e.clientX, e.clientY))}
                onPointerEnter={() => preview.prime(r.track.previewUrl)}
              >
                <button className="row__art" onClick={() => preview.toggle(r.track.key, r.track.previewUrl)} aria-label={`Play ${r.track.title}`}>
                  {r.track.artworkUrl ? <img src={art(r.track.artworkUrl, 120)} alt="" loading="lazy" /> : <span />}
                  <span className="row__play" aria-hidden>
                    {preview.playing === r.track.key ? <Pause size={14} /> : <Play size={14} />}
                  </span>
                </button>
                <button className="row__text" onClick={() => travel(r.track)}>
                  <span className="row__title">{r.track.title}</span>
                  <span className="row__artist">{r.track.artist}</span>
                </button>
                <span className="row__score">{Math.round(r.score * 100)}</span>
                <button className={`icon-btn ${saved.has(r.track.key) ? "is-on" : ""}`} onClick={() => toggleSave(r.track)} aria-label={saved.has(r.track.key) ? "Remove from library" : "Save"}>
                  <Heart size={15} />
                </button>
              </li>
            ))}
          </ol>
        </section>
      )}

      {menu && <ContextMenu x={menu.x} y={menu.y} title={menu.track.title} subtitle={menu.track.artist} items={menuItems(menu.track)} onClose={closeMenu} />}

      <AgentPanel
        track={agentAt?.track ?? null}
        anchor={agentAt ? { x: agentAt.x, y: agentAt.y } : null}
        steer={[]}
        enabled={agentOn}
        model={health?.agent.model}
        onClose={closeAgent}
        onResult={(r, plan, prompt) => addCustom(r, plan, prompt)}
      />

      <Drawer open={libOpen} side="right" title="Library" onClose={() => setLibOpen(false)}>
        <SpotifyExport count={library.length} refreshKey={`${spotifyBump}-${libOpen}`} />
        <Library
          entries={library}
          playing={preview.playing}
          onPlay={(t) => preview.toggle(t.key, t.previewUrl)}
          onExplore={(t) => (setLibOpen(false), travel(t))}
          onRemove={(key) => {
            const t = library.find((e) => e.track.key === key)?.track;
            if (t) toggleSave(t);
          }}
          onMenu={openMenu}
        />
      </Drawer>

      <Drawer open={telOpen} side="right" title="Telemetry" onClose={() => setTelOpen(false)}>
        <div className="telstack">
          <Detail rec={selectedRec} seed={seed} target={shown?.target} onReseed={(t) => (setTelOpen(false), travel(t))} />
          {shown && <SourceTrace reports={shown.sources} tookMs={shown.tookMs} pool={shown.candidatePool} />}
          <SourceHealth sources={snap?.sources ?? []} updatedAt={updatedAt} paused={telPaused} onTogglePause={() => setTelPaused((p) => !p)} error={telError} />
          <Sparkline samples={snap?.recent ?? []} />
          <LearnerPanel learner={snap?.learner} onReset={() => api.resetModel().then(() => setBump((b) => b + 1), (e: Error) => setError(e.message))} />
        </div>
      </Drawer>

      <div className="toast-region" aria-live="polite">
        {toast && <div className="toast">{toast}</div>}
      </div>
    </div>
  );
}
