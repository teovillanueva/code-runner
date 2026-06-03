# Design

> code-runner — **Hardened instrument panel.** A high-contrast achromatic console for running untrusted code live. Pure monochrome surfaces, a single electric-iris accent rationed like a status light, flat planes with depth from contrast (never shadow), tight Geist display tracking, and Geist Mono carrying every technical label and code block. Light and dark are both first-class.

**Theme:** dual (light primary, dark first-class) — toggle via `next-themes` / Fumadocs `RootProvider`. Both modes hold WCAG 2.2 AA.

**Register:** `brand` on the landing (`/`), `product` on the docs (`/docs/*`). See `PRODUCT.md`.

**Lineage:** Privy's high-contrast achromatic architecture (light canvas, inverted dark emphasis cards, one accent, flat planes) + Linear/Axiom dark-mode instrument-panel discipline. Distinct from all of them through the iris accent and the Geist type system.

---

## Tokens — Colors

The system is **achromatic neutral + one accent**. No second chromatic hue except semantic status (error/warn/success), used only as small dots/text, never as decoration. The accent carries **two lightness values of one hue**: a brighter iris for dark surfaces and fills, a deeper iris for text/links on light.

### Accent — Electric Iris (the only brand chromatic)

| Name | Light value | Dark value | Token | Role |
|------|-------------|------------|-------|------|
| Iris | `#4F46E5` | `#6D63FF` | `--color-iris` | Links, active nav, ghost/secondary button border+text, the "running" dot, data-highlight strokes, selection. The single rationed accent. |
| Iris Hover | `#4338CA` | `#8B82FF` | `--color-iris-hover` | Hover/pressed state of any iris element. |
| Iris Wash | `#EEEDFD` | `rgba(109,99,255,0.14)` | `--color-iris-wash` | Subtle tinted background behind highlighted code lines, active sidebar item, focused callouts. Never a large fill. |
| Iris Ring | `rgba(79,70,229,0.45)` | `rgba(109,99,255,0.55)` | `--color-iris-ring` | Focus-visible ring (2px). |

> Iris is rationed: at most **one iris-filled or iris-bordered emphasis per view cluster**, plus links and the running indicator. It is never a page background, never a card fill, never a decorative gradient.

### Light mode neutrals

| Name | Value | Token | Role |
|------|-------|-------|------|
| Canvas | `#FFFFFF` | `--color-canvas` | Page background. Stark white — the high-contrast Privy base. |
| Section | `#F6F7F9` | `--color-section` | Alternate band / muted inset surface / code block bg on light. |
| Card | `#FBFBFC` | `--color-card` | Subtle raised card (lifts a hair above canvas; separation is mostly border). |
| Emphasis | `#0B0B0F` | `--color-emphasis` | **Inverted dark card on white** — the Privy signature. Used for feature spotlights, the quickstart panel, the running-output mock. Text on it flips to near-white. |
| Ink | `#0A0A0B` | `--color-ink` | Primary text, headings, primary button fill. |
| Ink Secondary | `#46484E` | `--color-ink-2` | Body secondary, subheads. |
| Ink Muted | `#6C6F77` | `--color-ink-3` | Captions, metadata, placeholder (≥4.5:1 on canvas). |
| Hairline | `#E6E7EA` | `--color-line` | Every border, divider, card edge. The most-used non-text color. |
| Line Strong | `#D3D5DA` | `--color-line-2` | Input outlines, hovered borders, stronger separators. |

### Dark mode neutrals

| Name | Value | Token | Role |
|------|-------|-------|------|
| Canvas | `#0A0B0D` | `--color-canvas` | Page background. Cool near-black, never pure `#000`. |
| Section | `#0F1114` | `--color-section` | Alternate band / nav surface / code block bg on dark. |
| Card | `#15171C` | `--color-card` | Elevated card / panel base. |
| Emphasis | `#1B1E24` | `--color-emphasis` | Deepest inset (input fields, nested rows, the output-stream mock). |
| Ink | `#F4F5F7` | `--color-ink` | Primary text, headings (near-white, not `#fff`). |
| Ink Secondary | `#A6AAB3` | `--color-ink-2` | Body secondary, subheads. |
| Ink Muted | `#71757E` | `--color-ink-3` | Captions, metadata, placeholder (≥4.5:1 on canvas). |
| Hairline | `#22252B` | `--color-line` | Every border, divider, card edge. |
| Line Strong | `#2E323A` | `--color-line-2` | Input outlines, hovered borders, stronger separators. |

### Semantic status (small dots / inline text only)

| Name | Light | Dark | Token | Role |
|------|-------|------|-------|------|
| Running / Live | iris | iris | `--status-running` | Process alive, stdin open. Uses the accent, not green. |
| Success / Exit 0 | `#0E8F4F` | `#3FCB7A` | `--status-ok` | Clean exit, passing. |
| Warn / Timeout | `#B26A00` | `#E0A338` | `--status-warn` | Clock expiry, soft limit. |
| Error / Killed | `#C2362B` | `#F0685C` | `--status-err` | Non-zero exit, OOM kill, denied syscall. |

Status colors are paired with a label or icon — never color-alone (AA, color-blind safe).

---

## Tokens — Typography

Two families. **Geist** carries everything human-readable; **Geist Mono** carries everything technical — labels, eyebrows, tags, IDs, keyboard keys, and all code. The split is the voice: Geist reads, Geist Mono signals "this is a tool."

Install via the `geist` package (`import { GeistSans, GeistMono } from 'geist/font'`) or `next/font/google` (`Geist`, `Geist_Mono`). Replace the current `Inter` import in `apps/docs/app/layout.tsx`.

### Geist — display, headings, body, UI
- **Substitute:** Inter (acceptable fallback if Geist unavailable)
- **Weights:** 400 (body), 500 (UI / emphasis), 600 (headings/display)
- **Tracking:** tightens as size grows — `0` at ≤16px, down to `-0.03em` at display. Never looser than `0`.
- **Features:** `"cv01" on` (optional, single-story a), `"ss01" on`
- **Wrap:** `text-wrap: balance` on h1–h3, `text-wrap: pretty` on prose. Body line length capped 68–72ch.

### Geist Mono — chrome, code, data
- **Substitute:** JetBrains Mono
- **Weights:** 400, 500
- **Use:** section eyebrows (e.g. `HOW IT WORKS`), `BETA` tags, status pills, file paths, env keys, keyboard shortcuts, inline code, and all code blocks. UPPERCASE for eyebrows/tags with `+0.04em` tracking; normal case for code.
- **Never** body or headings.

### Type Scale

| Role | Size | Line Height | Tracking | Family | Token |
|------|------|-------------|----------|--------|-------|
| eyebrow | 12px | 1.0 | +0.04em (UPPER) | Mono | `--text-eyebrow` |
| caption | 12px | 1.5 | 0 | Geist | `--text-caption` |
| body-sm | 14px | 1.55 | 0 | Geist | `--text-body-sm` |
| body | 16px | 1.6 | 0 | Geist | `--text-body` |
| body-lg | 18px | 1.6 | -0.01em | Geist | `--text-body-lg` |
| subheading | 20px | 1.45 | -0.012em | Geist | `--text-subheading` |
| heading-sm | 24px | 1.33 | -0.016em | Geist | `--text-heading-sm` |
| heading | 32px | 1.22 | -0.02em | Geist | `--text-heading` |
| heading-lg | 44px | 1.12 | -0.026em | Geist | `--text-heading-lg` |
| display | 60px | 1.05 | -0.03em | Geist | `--text-display` |

Display ceiling is **60px** (clamp max). Headlines are 600 weight, tight, engineered — not 700+ shouting. Use `clamp()` so display scales down on mobile without overflow; test heading copy at every breakpoint.

---

## Tokens — Spacing & Shapes

**Base unit:** 4px. **Density:** compact / technical.

### Spacing Scale
`4, 8, 12, 16, 20, 24, 32, 40, 48, 64, 80, 96, 128` → `--spacing-*`

- Section gap: 80–96px (landing), 48–64px (docs).
- Card padding: 20–28px.
- Element gap: 8–12px.
- Page max-width: 1180px content; docs content column 720–760px.

### Border Radius — signature: pill CTAs on flat rectilinear surfaces

| Element | Value | Token |
|---------|-------|-------|
| chips / tags / status pills | 6px | `--radius-chip` |
| inputs | 8px | `--radius-input` |
| cards / panels / code blocks | 10px | `--radius-card` |
| buttons (primary, secondary, ghost) | 9999px | `--radius-button` |
| modals / dialogs | 14px | `--radius-modal` |

The **pill button on flat achromatic** is the brand signature (Privy lineage). Everything else stays tight rectilinear (6–14px). Radius never varies by button variant — only fill/border do.

---

## Components

### Primary Button (ink fill)
Fill `--color-ink`, text inverse (`--color-canvas`), pill radius, padding `10px 20px`, Geist 500 @ 15px. Hover: 90% opacity / slight lift in tone, no shadow. This is the highest-contrast CTA (Privy "Deep Midnight" move). One per cluster.

### Secondary Button (ghost iris)
Transparent fill, 1px `--color-iris` border, `--color-iris` text, pill radius, same padding. The accent's main button home. Hover: `--color-iris-wash` fill.

### Tertiary / Nav Ghost Button
No border, no fill, `--color-ink-2` text @ 14px Geist 500; hover → `--color-ink`. Zero visual weight.

### Eyebrow Label
Geist Mono 500, 12px, UPPERCASE, `+0.04em`, `--color-ink-3`. Sits 12–16px above a heading. **Use sparingly** — one or two per page max, where a section genuinely benefits. Not above every section (banned scaffold).

### Card (subtle)
`--color-card` fill, 1px `--color-line` border, 10px radius, 20–28px padding, **no shadow**. Separation reads through border + tone, not elevation. Never nest cards.

### Emphasis Panel (inverted)
`--color-emphasis` fill, no border, 10px radius. On light mode this is the dark-on-white Privy signature (quickstart panel, feature spotlight, output-stream mock); text flips to near-white. On dark mode it's the deepest inset.

### Code Block
`--color-section` bg, 1px `--color-line` border, 10px radius, Geist Mono 14px, `1.6` line-height. Header row (optional): file path in Geist Mono 12px `--color-ink-3` + a copy button (ghost). Highlighted lines use `--color-iris-wash` with a 2px `--color-iris` left marker **inside** the block only (this is the one place a left accent is allowed — it's a code gutter, not a card stripe).

### Status Pill
Geist Mono 11–12px, 6px radius, `2px 8px` padding, a 6px leading dot in the status color + label. `● running` (iris), `✓ exit 0` (ok), `⏱ timeout` (warn), `✕ killed` (err).

### Inline Code / Kbd
Geist Mono, `--color-section` bg, 1px `--color-line` border, 4–6px radius, `0.1em 0.35em` padding, `0.92em` size.

### Top Navigation
`--color-canvas` (or `--color-section` when stuck/scrolled), height 56px, 1px bottom `--color-line` only when scrolled. Logo (terminal-square glyph in `--color-ink`) + wordmark Geist 600 left; ghost links center/left in Geist 500 14px; theme toggle + GitHub + primary/secondary button right.

### Feature Row (replaces the icon-card grid)
**Do not** ship the 6-identical-icon-card grid. Use an asymmetric 2-up or alternating text/visual split with generous gaps, where each feature carries a real artifact (a code snippet, a status pill, a tiny diagram) rather than a generic lucide icon + heading + paragraph. If a compact summary grid is needed, make cells visually distinct (vary span, lead with a mono label or a small inline data element), bordered not filled.

### Customer / Language Logo Strip
Monochrome marks in `--color-ink-3` at ~60% opacity on the canvas, single row, no container, ~48px gaps. For code-runner this can be the supported-language wordmarks/glyphs.

---

## Do's and Don'ts

### Do
- Build on `--color-canvas` (white in light, near-black in dark). Use `--color-section` for bands and code, `--color-emphasis` for the inverted spotlight panels.
- Ration iris to one emphasis per cluster + links + the running dot. Reach for the **ink-fill** primary button as the loud CTA, iris-ghost as the accent CTA.
- Pill radius on buttons, 10px on cards/code, 6px on chips. Keep it consistent — radius is a signature.
- Use Geist Mono for every technical token (eyebrows, tags, paths, keys, code) and Geist for everything else.
- Separate surfaces with 1px hairline borders + tone shifts. Depth = contrast, never shadow.
- Tighten display tracking (`-0.02` to `-0.03em` at 32–60px); keep headlines at 600, not 700+.
- Hold AA in both themes; pair every status color with a label/icon; give every animation a reduced-motion fallback.

### Don't
- Don't introduce a second chromatic accent. Iris + achromatic + small semantic status, nothing else.
- Don't use drop shadows or glass/blur for elevation. (One subtle shadow is allowed only for floating popovers/menus — see Elevation.)
- Don't fill a content block, card, or button background with iris. Iris is strokes, text, borders, dots, and the focus ring.
- Don't ship the identical-icon-card feature grid, the hero-metric template, gradient text, or a tracked uppercase eyebrow above every section.
- Don't use pure `#000` as the dark canvas (`#0A0B0D`) or rely on cream/warm-tinted off-white in light (`#FFFFFF`/cool only).
- Don't set body in Geist Mono or code/labels in Geist — the split is the identity.
- Don't exceed the 60px display ceiling or let headlines touch (`-0.03em` floor).

---

## Surfaces

| Level | Light | Dark | Purpose |
|-------|-------|------|---------|
| 0 Canvas | `#FFFFFF` | `#0A0B0D` | Page background |
| 1 Section | `#F6F7F9` | `#0F1114` | Bands, code blocks, nav-on-scroll |
| 2 Card | `#FBFBFC` | `#15171C` | Cards, panels |
| 3 Emphasis | `#0B0B0F` (inverted) | `#1B1E24` (deepest) | Spotlight panels / inputs / output mock |

## Elevation

The system is **flat**. Depth comes from tone shifts across the 4 surface levels + 1px hairline borders. No card shadows, no elevation lift on hover (hover shifts tone or border, not z).

The **only** permitted shadow is for genuinely floating overlays (dropdowns, command menu, tooltips), so they read as above the page:
- Light: `0 8px 28px -6px rgba(10,10,11,0.16), 0 0 0 1px rgba(10,10,11,0.04)`
- Dark: `0 8px 28px -6px rgba(0,0,0,0.6), 0 0 0 1px rgba(255,255,255,0.04)`

## Motion

The product's promise is real-time and low-latency; the site embodies it. Motion is fast, snappy, and purposeful — never decorative.

- **Easing:** ease-out-quart `cubic-bezier(0.25, 1, 0.5, 1)`. No bounce, no elastic.
- **Durations:** 120ms (hover/press), 180ms (enter), 240ms (section reveal). Tokens `--ease-out`, `--dur-1/2/3`.
- **Materials:** transform + opacity by default. The live-output motif may use a typing/stream cadence and a pulsing iris running-dot.
- **Reveals** enhance an already-visible default (no content gated on a class transition). Stagger list items where it fits the content; don't apply one uniform entrance to every section.
- **Reduced motion:** every animation has a `@media (prefers-reduced-motion: reduce)` path → crossfade or instant. The running-dot pulse becomes a static dot.

## Imagery

No stock photography, no people, no decorative 3D, no mascots. Imagery **is** the product: real terminal/output streams, code blocks, the wire-contract JSON, and dark-themed product UI mockups inside Emphasis panels. The recurring motif is a **live stdin/stdout stream** — columns of Geist Mono output with a pulsing iris running-dot — used in the hero and echoed in a footer band. Icons are small (16–20px), single-color, stroke-based (lucide is fine), inline with text, never floating decoration. Language logos appear as monochrome glyphs.

## Layout

Max-width 1180px centered; docs reading column 720–760px. Landing rhythm: nav → asymmetric hero (left: eyebrow + 44–60px headline + subtext + ink-primary & iris-ghost CTAs; right: Emphasis panel with a live output-stream mock) → language logo strip → alternating feature splits (not an icon grid) with 80–96px gaps → quickstart Emphasis panel with a real 4-command block → footer with a dark stream band. Dark canvas continues throughout (no alternating colored bands; separation is whitespace + hairlines). Docs: left sidebar (Fumadocs), content column, right TOC; sidebar active item uses `--color-iris-wash` + iris text. Navigation is a single 56px sticky bar; no mega-menu.

---

## Quick Start

### Fumadocs integration (this project)

The docs site loads `fumadocs-ui/css/neutral.css` + `preset.css`, which expose `--fd-*` tokens. **Replace the neutral preset's brand variables** by mapping our tokens onto the `--fd-*` variables in `apps/docs/app/global.css`, and drop the stock `Inter` for Geist in `app/layout.tsx`. Minimum mapping:

```css
/* apps/docs/app/global.css — after the fumadocs imports */
:root {
  --fd-background: 255 255 255;        /* canvas (light) — fd expects channel triplets */
  --fd-foreground: 10 10 11;           /* ink */
  --fd-muted: 246 247 249;             /* section */
  --fd-muted-foreground: 108 111 119;  /* ink-3 */
  --fd-card: 251 251 252;              /* card */
  --fd-border: 230 231 234;            /* line */
  --fd-primary: 79 70 229;             /* iris (light) */
  --fd-accent: 238 237 253;            /* iris-wash */
  --fd-ring: 79 70 229;
}
.dark {
  --fd-background: 10 11 13;
  --fd-foreground: 244 245 247;
  --fd-muted: 15 17 20;
  --fd-muted-foreground: 113 117 126;
  --fd-card: 21 23 28;
  --fd-border: 34 37 43;
  --fd-primary: 109 99 255;            /* iris (dark) */
  --fd-accent: 27 30 36;
  --fd-ring: 109 99 255;
}
```
> Note: confirm whether the installed Fumadocs build expects `--fd-*` as space-separated RGB channels (`R G B`) or hex; the snippet uses channels (the neutral preset convention). Adjust if `make`/build shows otherwise. Keep the full token system below as the source of truth and feed Fumadocs from it.

### Tailwind v4 `@theme`

```css
@theme {
  /* Accent — iris (defaults = light; .dark overrides via :root below) */
  --color-iris: #4F46E5;
  --color-iris-hover: #4338CA;
  --color-iris-wash: #EEEDFD;

  /* Neutrals (light) */
  --color-canvas: #FFFFFF;
  --color-section: #F6F7F9;
  --color-card: #FBFBFC;
  --color-emphasis: #0B0B0F;
  --color-ink: #0A0A0B;
  --color-ink-2: #46484E;
  --color-ink-3: #6C6F77;
  --color-line: #E6E7EA;
  --color-line-2: #D3D5DA;

  /* Status */
  --status-running: #4F46E5;
  --status-ok: #0E8F4F;
  --status-warn: #B26A00;
  --status-err: #C2362B;

  /* Type */
  --font-geist: 'Geist', ui-sans-serif, system-ui, -apple-system, 'Inter', sans-serif;
  --font-geist-mono: 'Geist Mono', ui-monospace, 'JetBrains Mono', SFMono-Regular, Menlo, monospace;

  --text-eyebrow: 12px;
  --text-caption: 12px;
  --text-body-sm: 14px;
  --text-body: 16px;
  --text-body-lg: 18px;
  --text-subheading: 20px;
  --text-heading-sm: 24px;
  --text-heading: 32px;
  --text-heading-lg: 44px;
  --text-display: 60px;

  /* Spacing */
  --spacing-4: 4px;  --spacing-8: 8px;   --spacing-12: 12px; --spacing-16: 16px;
  --spacing-20: 20px; --spacing-24: 24px; --spacing-32: 32px; --spacing-40: 40px;
  --spacing-48: 48px; --spacing-64: 64px; --spacing-80: 80px; --spacing-96: 96px;
  --spacing-128: 128px;

  /* Radius */
  --radius-chip: 6px;
  --radius-input: 8px;
  --radius-card: 10px;
  --radius-button: 9999px;
  --radius-modal: 14px;

  /* Motion */
  --ease-out: cubic-bezier(0.25, 1, 0.5, 1);
  --dur-1: 120ms;
  --dur-2: 180ms;
  --dur-3: 240ms;
}

.dark {
  --color-iris: #6D63FF;
  --color-iris-hover: #8B82FF;
  --color-iris-wash: rgba(109,99,255,0.14);
  --color-canvas: #0A0B0D;
  --color-section: #0F1114;
  --color-card: #15171C;
  --color-emphasis: #1B1E24;
  --color-ink: #F4F5F7;
  --color-ink-2: #A6AAB3;
  --color-ink-3: #71757E;
  --color-line: #22252B;
  --color-line-2: #2E323A;
  --status-running: #6D63FF;
  --status-ok: #3FCB7A;
  --status-warn: #E0A338;
  --status-err: #F0685C;
}
```

### Similar brands (lineage, for reference)
Privy (high-contrast achromatic, inverted dark cards, one accent, pill CTAs, flat planes) · Linear / Axiom / Better Stack (dark instrument-panel discipline, mono for chrome, depth via contrast) · Vercel (Geist, near-black canvas, restraint). code-runner departs via the **iris** accent + **Geist/Geist Mono** + the **live-stream** imagery motif.
