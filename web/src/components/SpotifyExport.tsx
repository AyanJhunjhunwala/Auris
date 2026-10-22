import { ExternalLink, Loader2 } from "lucide-react";
import { useEffect, useState } from "react";
import { api } from "../api/client";
import type { SpotifyExportResult, SpotifyStatus } from "../api/types";

/** Library footer: connect Spotify and export saved songs to a playlist. */
export function SpotifyExport({ count, refreshKey }: { count: number; refreshKey: unknown }) {
  const [status, setStatus] = useState<SpotifyStatus | null>(null);
  const [name, setName] = useState(() => `Auris · ${new Date().toLocaleDateString(undefined, { month: "short", day: "numeric" })}`);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<SpotifyExportResult | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api.spotify.status().then(setStatus, () => setStatus(null));
  }, [refreshKey]);

  if (!status) return null;

  if (!status.configured) {
    return (
      <div className="spx spx--muted">
        <SpotifyMark /> Set <code>SPOTIFY_CLIENT_ID</code> to export
      </div>
    );
  }

  if (!status.connected) {
    return (
      <div className="spx">
        <a className="btn spx__connect" href={api.spotify.loginUrl()}>
          <SpotifyMark /> Connect Spotify
        </a>
        {status.redirectUri && (
          <p className="spx__meta spx__redirect">
            Redirect URI in your Spotify app must be
            <code
              title="Click to copy"
              onClick={() => navigator.clipboard?.writeText(status.redirectUri!)}
            >
              {status.redirectUri}
            </code>
          </p>
        )}
      </div>
    );
  }

  const run = async () => {
    setBusy(true);
    setError(null);
    setResult(null);
    try {
      setResult(await api.spotify.export(name));
    } catch (e) {
      setError((e as Error).message);
      api.spotify.status().then(setStatus, () => {});
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="spx">
      <div className="spx__row">
        <SpotifyMark />
        <label htmlFor="spx-name" className="sr-only">
          Playlist name
        </label>
        <input id="spx-name" value={name} onChange={(e) => setName(e.target.value)} disabled={busy} />
        <button className="btn btn--primary" onClick={run} disabled={busy || count === 0}>
          {busy ? <Loader2 size={14} className="spin" aria-hidden /> : null}
          Export {count}
        </button>
      </div>
      {result && (
        <p className="spx__done" role="status">
          {result.added} added{result.missing?.length ? ` · ${result.missing.length} not on Spotify` : ""}
          {result.url && (
            <a href={result.url} target="_blank" rel="noreferrer">
              Open <ExternalLink size={12} aria-hidden />
            </a>
          )}
        </p>
      )}
      {error && (
        <p className="spx__error" role="alert">
          {error}
        </p>
      )}
      <p className="spx__meta">
        {status.user}
        <button className="spx__link" onClick={() => api.spotify.logout().then(setStatus)}>
          Disconnect
        </button>
      </p>
    </div>
  );
}

function SpotifyMark() {
  return (
    <svg className="spx__mark" viewBox="0 0 24 24" width="16" height="16" aria-hidden>
      <circle cx="12" cy="12" r="11" fill="currentColor" />
      <path d="M6.5 9.3c3.8-1.1 8-.8 11.2 1M7 12.4c3.2-.9 6.6-.6 9.4.9M7.6 15.3c2.6-.7 5.2-.5 7.4.7" stroke="#000" strokeWidth="1.6" fill="none" strokeLinecap="round" />
    </svg>
  );
}
