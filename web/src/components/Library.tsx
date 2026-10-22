import { Compass, Pause, Play, Trash2 } from "lucide-react";
import { useMemo, useState } from "react";
import type { LibraryEntry, Track } from "../api/types";

interface Props {
  entries: LibraryEntry[];
  playing: string | null;
  onPlay: (t: Track) => void;
  onExplore: (t: Track) => void;
  onRemove: (key: string) => void;
  onMenu: (t: Track, x: number, y: number) => void;
}

/** Saved songs, filterable, each one a jumping-off point. */
export function Library({ entries, playing, onPlay, onExplore, onRemove, onMenu }: Props) {
  const [q, setQ] = useState("");
  const shown = useMemo(() => {
    const n = q.trim().toLowerCase();
    if (!n) return entries;
    return entries.filter((e) =>
      [e.track.title, e.track.artist, e.vibe, e.from].some((s) => s?.toLowerCase().includes(n)),
    );
  }, [entries, q]);

  if (!entries.length) {
    return (
      <div className="lib__empty">
        <p>Nothing saved yet.</p>
        <p>Tap the heart on any card, or right-click a song and choose “Save to library”.</p>
      </div>
    );
  }
  return (
    <div className="lib">
      <label className="sr-only" htmlFor="lib-filter">
        Filter library
      </label>
      <input
        id="lib-filter"
        className="lib__filter"
        placeholder={`Filter ${entries.length} songs…`}
        value={q}
        onChange={(e) => setQ(e.target.value)}
      />
      <ul className="lib__list">
        {shown.map((e) => (
          <li
            key={e.track.key}
            className="lib__row"
            onContextMenu={(ev) => {
              ev.preventDefault();
              onMenu(e.track, ev.clientX, ev.clientY);
            }}
          >
            <button className="lib__art" onClick={() => onPlay(e.track)} aria-label={`Play ${e.track.title}`}>
              {e.track.artworkUrl ? <img src={e.track.artworkUrl} alt="" loading="lazy" /> : <span />}
              <span className="lib__play" aria-hidden>
                {playing === e.track.key ? <Pause size={14} /> : <Play size={14} />}
              </span>
            </button>
            <div className="lib__text">
              <p className="lib__title">{e.track.title}</p>
              <p className="lib__artist">{e.track.artist}</p>
              {(e.vibe || e.from) && (
                <p className="lib__meta">
                  {e.vibe && <span className="lib__vibe">{e.vibe}</span>}
                  {e.from && <span>from {e.from}</span>}
                </p>
              )}
            </div>
            <button className="icon-btn" onClick={() => onExplore(e.track)} aria-label={`Explore from ${e.track.title}`}>
              <Compass size={15} />
            </button>
            <button className="icon-btn" onClick={() => onRemove(e.track.key)} aria-label={`Remove ${e.track.title}`}>
              <Trash2 size={15} />
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}
