import { Check, Languages } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import type { LanguageOption } from "../api/types";

interface Props {
  options: LanguageOption[];
  value: string | null; // null = any language
  onChange: (code: string | null) => void;
}

/** Header control: filter results to one language (or instrumental). */
export function LanguageMenu({ options, value, onChange }: Props) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const current = options.find((o) => o.code === value);

  useEffect(() => {
    if (!open) return;
    const close = (e: Event) => {
      if (e instanceof KeyboardEvent && e.key !== "Escape") return;
      if (e.type === "pointerdown" && ref.current?.contains(e.target as Node)) return;
      setOpen(false);
    };
    window.addEventListener("pointerdown", close);
    window.addEventListener("keydown", close);
    return () => {
      window.removeEventListener("pointerdown", close);
      window.removeEventListener("keydown", close);
    };
  }, [open]);

  const pick = (code: string | null) => {
    onChange(code);
    setOpen(false);
  };

  return (
    <div className="langmenu" ref={ref}>
      <button
        className={`icon-btn ${value ? "is-on" : ""}`}
        onClick={() => setOpen((o) => !o)}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-label={`Language: ${current?.label ?? "any"}`}
        title={current?.label ?? "Any language"}
      >
        <Languages size={18} />
        {value && <span className="badge badge--text">{value === "zxx" ? "♪" : value.toUpperCase()}</span>}
      </button>
      {open && (
        <ul className="langmenu__list" role="listbox" aria-label="Language">
          {[{ code: "", label: "Any language" }, ...options].map((o) => {
            const sel = (o.code || null) === value;
            return (
              <li key={o.code || "any"} role="option" aria-selected={sel}>
                <button onClick={() => pick(o.code || null)}>
                  <span>{o.label}</span>
                  {sel && <Check size={14} aria-hidden />}
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
