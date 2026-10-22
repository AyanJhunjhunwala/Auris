import type {
  AgentEvent,
  AgentRequest,
  Direction,
  Health,
  LibraryEntry,
  LanguageOption,
  LearnerSnapshot,
  SpotifyExportResult,
  SpotifyStatus,
  SuggestResult,
  SimilarRequest,
  SimilarResponse,
  TelemetrySnapshot,
  Track,
} from "./types";

// In dev, Vite proxies /api to the Go server (see vite.config.ts). Set
// VITE_API_BASE to call a backend on another origin.
const BASE = import.meta.env.VITE_API_BASE ?? "";

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(BASE + path, {
    ...init,
    headers: { "Content-Type": "application/json", ...(init?.headers ?? {}) },
  });
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw new ApiError(res.status, body.error ?? res.statusText);
  return body as T;
}

export const api = {
  search: (q: string, signal?: AbortSignal) =>
    request<{ query: string; source: string; results: Track[] }>(
      `/api/search?q=${encodeURIComponent(q)}&limit=8`,
      { signal },
    ),

  similar: (req: SimilarRequest, signal?: AbortSignal) =>
    request<SimilarResponse>("/api/similar", { method: "POST", body: JSON.stringify(req), signal }),

  directions: () => request<{ directions: Direction[] }>("/api/directions"),

  feedback: (requestId: string, trackKey: string, signal: "like" | "skip") =>
    request<{ ok: boolean; model: LearnerSnapshot }>("/api/feedback", {
      method: "POST",
      body: JSON.stringify({ requestId, trackKey, signal }),
    }),

  telemetry: () => request<TelemetrySnapshot>("/api/telemetry"),

  resetModel: () => request<{ ok: boolean; model: LearnerSnapshot }>("/api/model/reset", { method: "POST" }),

  health: () => request<Health>("/api/health"),

  languages: () => request<{ languages: LanguageOption[] }>("/api/languages"),

  spotify: {
    status: () => request<SpotifyStatus>("/api/spotify/status"),
    /** Full-page navigation: Spotify's login can't run in a fetch. */
    loginUrl: () => `${BASE}/api/spotify/login?return=${encodeURIComponent(window.location.origin + "/")}`,
    logout: () => request<SpotifyStatus>("/api/spotify/logout", { method: "POST" }),
    export: (name: string, keys?: string[]) =>
      request<SpotifyExportResult>("/api/spotify/export", { method: "POST", body: JSON.stringify({ name, keys }) }),
  },

  suggest: (track: Track, signal?: AbortSignal) =>
    request<SuggestResult>("/api/suggest", { method: "POST", body: JSON.stringify({ track }), signal }),

  /** Fills artwork/previews the main response didn't wait for. */
  enrich: (tracks: Track[]) =>
    request<{ tracks: Track[] }>("/api/enrich", { method: "POST", body: JSON.stringify({ tracks }) }),

  library: {
    list: () => request<{ entries: LibraryEntry[] }>("/api/library"),
    add: (e: Omit<LibraryEntry, "savedAt">) =>
      request<LibraryEntry>("/api/library", { method: "POST", body: JSON.stringify(e) }),
    remove: (key: string) =>
      fetch(`${BASE}/api/library/${encodeURIComponent(key)}`, { method: "DELETE" }).then((r) => {
        if (!r.ok && r.status !== 404) throw new ApiError(r.status, r.statusText);
      }),
  },

  /**
   * Runs one agent turn, calling onEvent for each server-sent event. POST
   * bodies rule out EventSource, so the stream is parsed by hand.
   */
  agent: async (req: AgentRequest, onEvent: (e: AgentEvent) => void, signal?: AbortSignal) => {
    const res = await fetch(BASE + "/api/agent", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(req),
      signal,
    });
    if (!res.ok || !res.body) {
      const body = await res.json().catch(() => ({}));
      throw new ApiError(res.status, body.error ?? res.statusText);
    }
    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buf = "";
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      buf += decoder.decode(value, { stream: true });
      let sep: number;
      while ((sep = buf.indexOf("\n\n")) >= 0) {
        const frame = buf.slice(0, sep);
        buf = buf.slice(sep + 2);
        let type = "message";
        const data: string[] = [];
        for (const line of frame.split("\n")) {
          if (line.startsWith("event:")) type = line.slice(6).trim();
          else if (line.startsWith("data:")) data.push(line.slice(5).trimStart());
        }
        if (!data.length) continue;
        try {
          onEvent({ type, data: JSON.parse(data.join("\n")) } as AgentEvent);
        } catch {
          // ignore malformed frames
        }
      }
    }
  },
};
