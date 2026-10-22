import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { LucideIcon } from "lucide-react";

export interface MenuItem {
  id: string;
  label: string;
  icon?: LucideIcon;
  hint?: string;
  accent?: boolean; // highlight (the agent entry)
  disabled?: boolean;
  onSelect: () => void;
}

interface Props {
  x: number;
  y: number;
  title?: string;
  subtitle?: string;
  items: MenuItem[];
  onClose: () => void;
}

/**
 * Right-click menu. Opened by contextmenu (mouse, the Menu key, Shift+F10)
 * or the visible ⋯ buttons, so it never depends on right-click alone.
 * Arrow keys move, Enter selects, Escape closes; focus returns to where it
 * came from.
 */
export function ContextMenu({ x, y, title, subtitle, items, onClose }: Props) {
  const ref = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState({ left: x, top: y });
  const [active, setActive] = useState(() => items.findIndex((i) => !i.disabled));

  // Keep the menu on-screen.
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const r = el.getBoundingClientRect();
    setPos({
      left: Math.max(8, Math.min(x, window.innerWidth - r.width - 8)),
      top: Math.max(8, Math.min(y, window.innerHeight - r.height - 8)),
    });
  }, [x, y]);

  useEffect(() => {
    const prev = document.activeElement as HTMLElement | null;
    ref.current?.focus();
    const close = (e: Event) => {
      if (e.type === "pointerdown" && ref.current?.contains(e.target as Node)) return;
      onClose();
    };
    window.addEventListener("pointerdown", close);
    window.addEventListener("resize", close);
    window.addEventListener("scroll", close, true);
    return () => {
      window.removeEventListener("pointerdown", close);
      window.removeEventListener("resize", close);
      window.removeEventListener("scroll", close, true);
      prev?.focus?.();
    };
  }, [onClose]);

  const move = (dir: 1 | -1) => {
    if (!items.length) return;
    let i = active;
    for (let n = 0; n < items.length; n++) {
      i = (i + dir + items.length) % items.length;
      if (!items[i].disabled) break;
    }
    setActive(i);
  };

  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === "ArrowDown") (e.preventDefault(), move(1));
    else if (e.key === "ArrowUp") (e.preventDefault(), move(-1));
    else if (e.key === "Escape" || e.key === "Tab") (e.preventDefault(), onClose());
    else if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      const it = items[active];
      if (it && !it.disabled) {
        onClose();
        it.onSelect();
      }
    }
  };

  return (
    <div
      ref={ref}
      className="ctx"
      role="menu"
      tabIndex={-1}
      aria-label={title ? `Actions for ${title}` : "Actions"}
      aria-activedescendant={active >= 0 ? `ctx-${items[active]?.id}` : undefined}
      style={{ left: pos.left, top: pos.top }}
      onKeyDown={onKey}
      onContextMenu={(e) => e.preventDefault()}
    >
      {title && (
        <div className="ctx__head">
          <span className="ctx__title">{title}</span>
          {subtitle && <span className="ctx__sub">{subtitle}</span>}
        </div>
      )}
      {items.map((it, i) => {
        const Icon = it.icon;
        return (
          <div
            key={it.id}
            id={`ctx-${it.id}`}
            role="menuitem"
            aria-disabled={it.disabled || undefined}
            className={`ctx__item ${i === active ? "is-active" : ""} ${it.accent ? "is-accent" : ""}`}
            onPointerEnter={() => !it.disabled && setActive(i)}
            onClick={() => {
              if (it.disabled) return;
              onClose();
              it.onSelect();
            }}
          >
            {Icon && <Icon size={15} aria-hidden />}
            <span>{it.label}</span>
            {it.hint && <kbd className="ctx__hint">{it.hint}</kbd>}
          </div>
        );
      })}
    </div>
  );
}
