import { Loader2, Search } from "lucide-react";
import { useEffect, useId, useRef, useState } from "react";
import { api } from "../api/client";
import type { Track } from "../api/types";
import { art, useDebounced } from "../hooks/hooks";

interface Props {
  onPick: (t: Track) => void;
  onFreeText: (q: string) => void;
  compact?: boolean;
}

/** Accessible combobox: type, arrow through suggestions, Enter to pick. */
export function SearchBar({ onPick, onFreeText, compact }: Props) {
  const [q, setQ] = useState("");
  const [results, setResults] = useState<Track[]>([]);
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(-1);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const debounced = useDebounced(q.trim(), 220);
  const listId = useId();
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (debounced.length < 2) {
      setResults([]);
      setError(null);
      return;
    }
    const ctl = new AbortController();
    setLoading(true);
    api
      .search(debounced, ctl.signal)
      .then((r) => {
        setResults(r.results);
        setError(null);
        setActive(-1);
      })
      .catch((e: Error) => {
        if (e.name !== "AbortError") {
          setResults([]);
          setError(e.message);
        }
      })
      .finally(() => setLoading(false));
    return () => ctl.abort();
  }, [debounced]);

  // "/" focuses search from anywhere.
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      if (e.key === "/" && document.activeElement?.tagName !== "INPUT") {
        e.preventDefault();
        inputRef.current?.focus();
      }
    };
    window.addEventListener("keydown", on);
    return () => window.removeEventListener("keydown", on);
  }, []);

  const pick = (t: Track) => {
    onPick(t);
    setQ(`${t.artist} — ${t.title}`);
    setOpen(false);
  };

  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setOpen(true);
      setActive((a) => Math.min(results.length - 1, a + 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setActive((a) => Math.max(-1, a - 1));
    } else if (e.key === "Enter") {
      if (active >= 0 && results[active]) pick(results[active]);
      else if (q.trim().length >= 2) {
        onFreeText(q.trim());
        setOpen(false);
      }
    } else if (e.key === "Escape") {
      setOpen(false);
    }
  };

  const showList = open && (results.length > 0 || error);

  return (
    <div className={`search ${compact ? "search--compact" : ""}`} role="search">
      <label htmlFor="seed-search" className="sr-only">
        Search for a song
      </label>
      <Search className="search__icon" size={compact ? 16 : 18} aria-hidden />
      <input
        id="seed-search"
        ref={inputRef}
        className="search__input"
        type="text"
        autoComplete="off"
        spellCheck={false}
        placeholder="Search a song"
        value={q}
        onChange={(e) => {
          setQ(e.target.value);
          setOpen(true);
        }}
        onFocus={() => setOpen(true)}
        onBlur={() => setTimeout(() => setOpen(false), 120)}
        onKeyDown={onKey}
        role="combobox"
        aria-expanded={!!showList}
        aria-controls={listId}
        aria-autocomplete="list"
        aria-activedescendant={active >= 0 ? `${listId}-${active}` : undefined}
      />
      {loading ? (
        <Loader2 className="search__spin" size={16} aria-label="Searching" />
      ) : (
        <kbd className="search__kbd" aria-hidden>
          /
        </kbd>
      )}
      {showList && (
        <ul id={listId} role="listbox" className="search__list">
          {error && <li className="search__error">{error}</li>}
          {results.map((t, i) => (
            <li
              key={t.key + i}
              id={`${listId}-${i}`}
              role="option"
              aria-selected={i === active}
              className="search__option"
              onMouseDown={(e) => e.preventDefault()}
              onClick={() => pick(t)}
              onMouseEnter={() => setActive(i)}
            >
              {t.artworkUrl ? (
                <img src={art(t.artworkUrl, 120)} alt="" width={36} height={36} loading="lazy" />
              ) : (
                <span className="search__noart" aria-hidden />
              )}
              <span className="search__text">
                <span className="search__title">{t.title}</span>
                <span className="search__artist">
                  {t.artist}
                  {t.album ? ` · ${t.album}` : ""}
                </span>
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
