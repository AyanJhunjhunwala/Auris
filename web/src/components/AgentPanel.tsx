import { CornerDownLeft, Loader2, RotateCcw, Sparkles, Square, X } from "lucide-react";
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { api } from "../api/client";
import type { AgentPlan, SimilarResponse, Steer, Track } from "../api/types";


type Stage = "idle" | "thinking" | "searching" | "done" | "error";

interface Props {
  track: Track | null;
  anchor: { x: number; y: number } | null;
  steer: Steer[];
  enabled: boolean;
  model?: string;
  onClose: () => void;
  onResult: (resp: SimilarResponse, plan: AgentPlan | null, prompt: string) => void;
}

/**
 * "Hey, make this sound like…" — opens where you right-clicked, streams the
 * agent's reply token by token, shows the plan it chose, then hands the new
 * results to the page.
 */
export function AgentPanel({ track, anchor, steer, enabled, model, onClose, onResult }: Props) {
  const [prompt, setPrompt] = useState("");
  const [stage, setStage] = useState<Stage>("idle");
  const [reply, setReply] = useState("");
  const [plan, setPlan] = useState<AgentPlan | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [found, setFound] = useState(0);
  const [pos, setPos] = useState<{ left: number; top: number } | null>(null);
  const ref = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLTextAreaElement>(null);
  const ctl = useRef<AbortController | null>(null);
  const lastPrompt = useRef("");

  const open = !!anchor;

  useLayoutEffect(() => {
    if (!anchor || !ref.current) return;
    const r = ref.current.getBoundingClientRect();
    setPos({
      left: Math.max(12, Math.min(anchor.x, window.innerWidth - r.width - 12)),
      top: Math.max(12, Math.min(anchor.y, window.innerHeight - r.height - 12)),
    });
  }, [anchor, stage, reply]);

  // Fresh state each time it opens on a (possibly different) track.
  useEffect(() => {
    if (!open) return;
    setPrompt("");
    setStage("idle");
    setReply("");
    setPlan(null);
    setError(null);
    requestAnimationFrame(() => input.current?.focus());
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
      ctl.current?.abort();
    };
  }, [open, track?.key, onClose]);

  const run = async (text: string) => {
    const p = text.trim();
    if (!p || !track) return;
    lastPrompt.current = p;
    ctl.current?.abort();
    const c = new AbortController();
    ctl.current = c;
    setStage("thinking");
    setReply("");
    setPlan(null);
    setError(null);
    let gotPlan: AgentPlan | null = null;
    try {
      await api.agent(
        { prompt: p, track, steer },
        (ev) => {
          switch (ev.type) {
            case "status":
              setStage(ev.data.stage);
              break;
            case "delta":
              setReply((r) => r + ev.data.text);
              break;
            case "plan":
              gotPlan = ev.data;
              setPlan(ev.data);
              break;
            case "result":
              setFound(ev.data.recommendations.length);
              setStage("done");
              onResult(ev.data, gotPlan, p);
              break;
            case "error":
              setError(ev.data.message);
              setStage("error");
              break;
          }
        },
        c.signal,
      );
    } catch (e) {
      if ((e as Error).name !== "AbortError") {
        setError((e as Error).message);
        setStage("error");
      }
    }
  };

  if (!open) return null;
  const busy = stage === "thinking" || stage === "searching";

  return (
    <div
      ref={ref}
      className={`agent ${busy ? "is-busy" : ""}`}
      role="dialog"
      aria-label="Ask the Auris agent"
      style={pos ? { left: pos.left, top: pos.top } : { visibility: "hidden" }}
      onContextMenu={(e) => e.preventDefault()}
    >
      <header className="agent__head">
        <Sparkles size={15} className="agent__spark" aria-hidden />
        <span className="agent__badge" title={model ? `AI · ${model}` : "AI"}>
          AI
        </span>
        <button className="icon-btn" onClick={onClose} aria-label="Close agent">
          <X size={15} />
        </button>
      </header>

      {track && (
        <div className="agent__track">
          {track.artworkUrl ? <img src={track.artworkUrl} alt="" width={40} height={40} /> : <span className="agent__noart" />}
          <div>
            <p className="agent__title">{track.title}</p>
            <p className="agent__artist">{track.artist}</p>
          </div>
        </div>
      )}

      {!enabled ? (
        <p className="agent__off">
          Set <code>OPENROUTER_API_KEY</code> in <code>.env</code> and restart.
        </p>
      ) : (
        <>
          <form
            className="agent__form"
            onSubmit={(e) => {
              e.preventDefault();
              run(prompt);
            }}
          >
            <label htmlFor="agent-input" className="sr-only">
              How should this sound?
            </label>
            <textarea
              id="agent-input"
              ref={input}
              rows={2}
              value={prompt}
              placeholder="Make it sound like…"
              onChange={(e) => setPrompt(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && !e.shiftKey) {
                  e.preventDefault();
                  run(prompt);
                }
              }}
              disabled={busy}
            />
            {busy ? (
              <button type="button" className="agent__send" onClick={() => ctl.current?.abort()} aria-label="Stop">
                <Square size={14} />
              </button>
            ) : (
              <button type="submit" className="agent__send" disabled={!prompt.trim()} aria-label="Send">
                <CornerDownLeft size={15} />
              </button>
            )}
          </form>

          {stage !== "idle" && (
            <div className="agent__out" aria-live="polite">
              {busy && (
                <p className="agent__status">
                  <Loader2 size={13} className="spin" aria-hidden />
                  {stage === "thinking" ? "Thinking" : "Searching"}
                </p>
              )}
              {reply && (
                <p className="agent__reply">
                  {reply}
                  {stage === "thinking" && <span className="agent__caret" aria-hidden />}
                </p>
              )}
              {plan && <PlanChips plan={plan} />}
              {stage === "done" && (
                <div className="agent__done">
                  <span>
                    {found} songs{plan?.label ? ` · ${plan.label}` : ""}
                  </span>
                  <button className="btn btn--ghost" onClick={() => (setStage("idle"), setPrompt(""), input.current?.focus())}>
                    Refine
                  </button>
                </div>
              )}
              {error && (
                <div className="agent__error" role="alert">
                  {error}
                  <button className="btn btn--ghost" onClick={() => run(lastPrompt.current)}>
                    <RotateCcw size={13} aria-hidden /> Retry
                  </button>
                </div>
              )}
            </div>
          )}
        </>
      )}
    </div>
  );
}

const FEATURE_LABEL: Record<string, string> = {
  energy: "energy",
  valence: "mood",
  danceability: "dance",
  acousticness: "acoustic",
  instrumentalness: "instrumental",
  speechiness: "speech",
  liveness: "live",
  tempo: "tempo",
  loudness: "loudness",
};

export function PlanChips({ plan }: { plan: AgentPlan }) {
  const chips: string[] = [];
  for (const s of plan.steer ?? []) chips.push(`${s.direction} ${Math.round(s.intensity * 100)}%`);
  for (const [k, v] of Object.entries(plan.targets ?? {})) {
    if (typeof v !== "number") continue;
    chips.push(k === "tempo" ? `${Math.round(v)} bpm` : k === "loudness" ? `${v} dB` : `${FEATURE_LABEL[k] ?? k} ${v.toFixed(2)}`);
  }
  if (plan.reference) {
    const r = plan.reference;
    chips.push(`like ${r.query ?? `${r.artist} – ${r.title}`}`);
  }
  if (!chips.length) return null;
  return (
    <ul className="plan" aria-label="Agent plan">
      {chips.map((c) => (
        <li key={c} className="plan__chip">
          {c}
        </li>
      ))}
    </ul>
  );
}
