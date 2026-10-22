# Space (main view) — Page Overrides

> **PROJECT:** Auris
> Rules here **override** `design-system/auris/MASTER.md` for the main view.
> Derived from ui-ux-pro-max queries: style "Exaggerated Minimalism" /
> "Minimalism & Swiss", typography pairing "Minimal Swiss" (Inter).
> The skill's suggested indigo palette was replaced at the product owner's
> request (true black, no blue).

## Style

- Exaggerated minimal / Swiss: black and white, generous negative space,
  one accent (white), no decoration that doesn't carry meaning.
- The song is the only hero. Copy is limited to song titles, artists and
  direction labels (1–3 words). No taglines, hints or marketing text.

## Color (tokens in `web/src/styles/tokens.css`)

| Role | Value |
|------|-------|
| Background | `#000000` |
| Surfaces | `#0a0a0a`, `#121212`, `#1a1a1a` |
| Lines | `#1c1c1c`, `#2a2a2a`, `#3d3d3d` |
| Text | `#fafafa` |
| Secondary text | `#a3a3a3` (8.3:1) |
| Tertiary text | `#737373` (4.6:1, small labels only) |
| Accent | `#ffffff` (active direction, selection, focus) |
| Status (telemetry only) | ok `#4ade80`, warn `#fbbf24`, bad `#f87171` |

Neutral greys only: no blue/slate cast anywhere.

## Typography

- Inter 400/500/600/700, `cv11 ss01 ss03`.
- Song title 20px/600, tracking -0.025em. Labels 13px/500. Captions 11–12px.

## Layout: spatial search

- Album cover centred; 4–6 direction branches radiate to labels placed on an
  ellipse (uses horizontal space on wide screens).
- No direction taken: nearest neighbours fill the gaps between branches,
  distance from centre = match score.
- Direction taken: its results form a grid along that line, best match
  nearest the centre; other branches dim.
- Header and dock are obstacles; nothing is placed under them or over the
  title block.
- A list below the fold mirrors what's on screen (accessible alternative to
  the spatial view).

## Motion

- Results fly out from the centre (640ms, ease-out-expo), staggered 35ms.
- Active/loading direction: dashed "flow" moving outward along the line.
- Hover: cover scales 1.18 with a white hairline; selection 1.22 + glow.
- `prefers-reduced-motion`: no fly-out, no flow, static reveal.

## Interaction

- Click a cover: select + play preview (pre-buffered on hover).
  Double-click or ↗: go to that song. Right-click / ⋯: menu.
- Direction labels are buttons (keyboard reachable); pressing the active one
  returns to the neighbourhood.
- Free-form "Make it sound like…" adds a custom direction (AI agent).
