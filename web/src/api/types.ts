// Mirrors backend/internal/model and the API payloads. Keep in sync with Go.

export type SourceName = "lastfm" | "reccobeats" | "deezer" | "listenbrainz" | "musicbrainz";

export interface Features {
  acousticness?: number;
  danceability?: number;
  energy?: number;
  instrumentalness?: number;
  liveness?: number;
  loudness?: number;
  speechiness?: number;
  tempo?: number;
  valence?: number;
  key?: number;
  mode?: number;
}

export interface TrackIDs {
  isrc?: string;
  mbid?: string;
  spotifyId?: string;
  reccobeatsId?: string;
  deezerId?: number;
  deezerArtistId?: number;
  releaseMbid?: string;
}

export interface Track {
  key: string;
  title: string;
  artist: string;
  artistId?: string;
  album?: string;
  artworkUrl?: string;
  previewUrl?: string;
  url?: string;
  durationMs?: number;
  popularity?: number;
  ids: TrackIDs;
  features?: Features;
  language?: string;
}

export interface Evidence {
  source: SourceName;
  rank: number;
  score: number;
  reason?: string;
}

export interface Recommendation {
  track: Track;
  score: number;
  evidence: Evidence[];
  breakdown: { signals: Record<string, number>; logit: number };
  reasons: string[];
}

export type SourceStatus = "ok" | "empty" | "error" | "disabled" | "skipped" | "timeout";

export interface SourceReport {
  source: SourceName;
  stage: "resolve" | "reference" | "candidates" | "features" | "enrich";
  status: SourceStatus;
  latencyMs: number;
  count: number;
  error?: string;
}

export interface SeedQuery {
  query?: string;
  artist?: string;
  title?: string;
  deezerId?: number;
  isrc?: string;
}

export interface Steer {
  direction: string;
  intensity: number;
}

export interface SimilarRequest {
  seed: SeedQuery;
  limit?: number;
  steer?: Steer[];
  sources?: SourceName[];
  targets?: Features;
  reference?: SeedQuery;
  referenceWeight?: number;
  languages?: string[];
}

export interface SimilarResponse {
  requestId: string;
  seed: Track;
  target?: Features;
  reference?: Track;
  recommendations: Recommendation[];
  sources: SourceReport[];
  candidatePool: number;
  tookMs: number;
  generatedAt: string;
}

export interface Direction {
  id: string;
  label: string;
  description: string;
  deltas: Record<string, number>;
}

export interface SourceSnapshot {
  source: SourceName;
  enabled: boolean;
  requests: number;
  errors: number;
  cacheHits: number;
  rateLimits: number;
  p50Ms: number;
  p95Ms: number;
  lastError?: string;
  lastErrorAt?: string;
  lastOkAt?: string;
}

export interface RequestSample {
  at: string;
  tookMs: number;
  seed: string;
  results: number;
  pool: number;
  steered: boolean;
  sourcesOk: number;
}

export interface LearnerSnapshot {
  weights: { signal: string; weight: number; prior: number }[];
  bias: number;
  updates: number;
  likes: number;
  skips: number;
  updatedAt: string;
}

export interface TelemetrySnapshot {
  startedAt: string;
  uptimeSec: number;
  sources: SourceSnapshot[];
  recent: RequestSample[];
  cache: { hits: number; misses: number; size: number };
  learner?: LearnerSnapshot;
}

// --- agent ---------------------------------------------------------------

export interface AgentPlan {
  label?: string;
  seed?: SeedQuery;
  steer?: Steer[];
  targets?: Features;
  reference?: SeedQuery;
  referenceWeight?: number;
}

export interface AgentRequest {
  prompt: string;
  track?: Track;
  steer?: Steer[];
  limit?: number;
}

/** Events streamed by POST /api/agent (server-sent events). */
export type AgentEvent =
  | { type: "status"; data: { stage: "thinking" | "searching" } }
  | { type: "thinking"; data: { chars: number } }
  | { type: "delta"; data: { text: string } }
  | { type: "plan"; data: AgentPlan }
  | { type: "result"; data: SimilarResponse }
  | { type: "error"; data: { message: string } }
  | { type: "done"; data: { ok: boolean } };

// --- library -------------------------------------------------------------

export interface LibraryEntry {
  track: Track;
  savedAt: string;
  from?: string;
  vibe?: string;
  steer?: Steer[];
}

export interface Health {
  status: string;
  sources: Record<string, boolean>;
  agent: { enabled: boolean; model?: string };
}

// --- suggestions (the branches around a song) -----------------------------

export interface Suggestion {
  id: string;
  label: string;
  kind: "direction" | "reference" | "custom";
  plan: AgentPlan;
}

export interface SuggestResult {
  suggestions: Suggestion[];
  source: "llm" | "heuristic";
}

// --- language filter & Spotify --------------------------------------------

export interface LanguageOption {
  code: string;
  label: string;
}

export interface SpotifyStatus {
  configured: boolean;
  connected: boolean;
  user?: string;
  redirectUri?: string;
}

export interface SpotifyExportResult {
  playlistId: string;
  url: string;
  added: number;
  missing: string[] | null;
}
