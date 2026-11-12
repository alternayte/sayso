# Sayso UI design

This file is the binding design for the UI. `src/index.css` holds the tokens and component classes. `src/motion/` holds the Motion v2 classes and hooks. A token check (`src/lib/tokens.test.ts`) fails when a token in `src/index.css` differs from the table below.

## Tokens

```yaml
colors:
  canvas: "#0b0d0e"        sidebar: "#090b0c"       panel: "#0f1315"        raised: "#131a1d"
  hairline: "#1a2427"      hairline-strong: "#24343a"
  ink: "#e8fbfe"           body: "#b9cdd2"          mute: "#6f878d"
  accent: "#00e5ff"        accent-ink: "#00191d"    accent-soft: "rgba(0,229,255,0.10)"   accent-text: "#5ff0ff"
  success: "#3dffa8"       success-soft: "rgba(61,255,168,0.10)"
  warning: "#ffd84d"       warning-soft: "rgba(255,216,77,0.10)"
  danger: "#ff5d73"        danger-soft: "rgba(255,93,115,0.10)"
effects:
  glow: "0 0 0 1px rgba(0,229,255,0.55), 0 0 22px rgba(0,229,255,0.35)"
  glow-focus: "0 0 0 1px #00e5ff, 0 0 16px rgba(0,229,255,0.45)"
typography:
  sans: "Geist, system-ui, sans-serif"      mono: "Geist Mono, ui-monospace, monospace"
  title:   mono 22px 600, tracking -0.01em
  section: mono 14px 500
  body:    sans 14px 400, line-height 1.5
  hint:    sans 12px, colour mute
  label:   mono 12px 500, upper case, tracking 0.03em
  badge:   mono 11px 500
  value:   mono 12.5px
shape:   radius 4px, radius-control 2px, radius-pill 999px
motion:  fast 120ms, base 180ms, slow 260ms
         ease-out cubic-bezier(0.16, 1, 0.3, 1), ease-in-out cubic-bezier(0.65, 0, 0.35, 1)
```

Rules (Factoree `ui/DESIGN.md`):

- Dark only, `color-scheme: dark`. No light mode, no second accent, no gradient.
- Cyan marks four things only: the primary action, the focused control, the current step and a running state. One primary action per view. Other buttons are `raised` with a `hairline-strong` border, or ghost.
- Glow only on the primary action, the current step marker and the brand mark. Focus uses `glow-focus`. Nothing else glows.
- Cyan is never body text. Links and code use `accent-text`.
- Status: success for ready or done, accent for running, warning for queued, danger for failed. Each status is a pill badge with a dot on its `-soft` background.
- Geist Sans for every sentence. Geist Mono for identity: titles, section headings, labels, table headers, badges, IDs, SHAs, keys and URLs. Upper case only for labels and table headers.
- Fonts are bundled from `@fontsource/geist-sans` and `@fontsource/geist-mono`. The page loads nothing from another origin.
- Layout: a 200 px sidebar with the brand and navigation; below 640 px the navigation becomes a horizontal scroll row. Content padding 28 px 32 px, 20 px on narrow screens. Forms are at most 640 px wide. A card is a `panel` with a `hairline` border and `radius`; a card header has a `hairline` bottom border. Tables scroll inside their card.
- Component classes with the same names and behaviour as Factoree: `t-title`, `t-section`, `t-label`, `t-hint`, `t-value`, `t-crumb`, `link`, `card`, `card-head`, `btn` (`btn-primary`, `btn-ghost`, `btn-danger`, `btn-sm`), `segmented`, `segment`, `control`, `control-mono`, `switch`, `badge` (`badge-accent`, `badge-success`, `badge-warning`, `badge-danger`), `note` (and its variants), `help-trigger`, `popover`, `table`, `nav-link`, `step-marker`, `brand-mark`, `skeleton`, `enter`, `fade-in`.
- A save confirms in place next to its button, never with a toast for a form on the same screen.

## Motion v2

Goal: every click answers at once, and the app feels alive without feeling slow. The rules extend the motion tokens above. Write them as reusable CSS classes and small hooks in `src/motion/`, with no Sayso-specific names, so Factoree can copy the folder.

**Principles**
- Feedback starts on `pointerdown`, not on `click`. Nothing waits for an animation: input is never blocked, and a new action interrupts a running animation.
- Animate only `transform`, `opacity` and `filter`. No layout properties.
- Durations: press 60–90 ms, hover and focus `fast` (120 ms), enter and exit `base` (180 ms), larger moves `slow` (260 ms). Nothing longer than 320 ms, except the live pulse.
- Exits are faster than entries (about 70 % of the entry time).
- Springs use the CSS `linear()` easing function with a precomputed curve (`--spring-snappy`: settles in about 220 ms, at most 4 % overshoot; `--spring-soft`: about 320 ms, no overshoot). Browsers without `linear()` fall back to `--ease-out`.
- `prefers-reduced-motion: reduce` removes movement, scale, blur and shimmer. Colour and opacity changes stay, at most 120 ms.

**Patterns**

| Pattern | Behaviour |
|---|---|
| Press | Buttons, rows, cards and nav items scale to 0.97 (buttons) or shift background (rows) on `pointerdown` in 60 ms. Release returns with `--spring-snappy`. Keyboard activation shows the same state. |
| Async button | Factoree's busy and done states, plus: the label cross-fades into the spinner (no jump), and the check draws its stroke in 220 ms. |
| Optimistic actions | Dismiss, mode change, pause, resume, cancel, toggles and guidance save update the UI at once (TanStack Query optimistic update). On error, the change rolls back, the control shakes 4 px twice in 240 ms, and an inline error shows. |
| Sliding indicators | The active nav item, tabs and segmented controls share one indicator element that moves with a FLIP transform (`--spring-soft`), not a per-item background. |
| Route change | View Transitions: the main area cross-fades with a 6 px slide in the navigation direction (deeper: from the right; back: from the left), `base`. The sidebar does not move. |
| Shared elements | List to detail: the row title, status badge and repository name morph into the detail header with named view transitions (`view-transition-name` set only on the clicked row). |
| Enter | Lists and cards enter with a 4 px rise and fade, staggered 30 ms, at most 6 items (Factoree rule). New items that arrive live (run events, rounds, findings) slide in from 6 px below and get an accent-soft background that fades out over 900 ms. |
| Status change | A badge that changes state cross-fades, and its dot shows one ring pulse (scale 1 to 2.2, fade out, 600 ms). Running state: the dot breathes (opacity 0.5 to 1, 2 s, infinite). |
| Numbers | Counts, tokens and cost tween to the new value over 400 ms with tabular figures. No tween on first render. |
| Live run view | Auto-scroll follows new events with smooth scroll while the user is at the bottom. When the user scrolls up, a "N new events" pill rises in; a click scrolls down and the pill fades. |
| Secret reveal | The eye button swaps icons with a rotate and fade (120 ms). The value goes from `blur(4px)` to clear in 160 ms. Hiding is instant. |
| Copy | The icon morphs to a check (scale and fade), and the check stays 1.2 s. |
| Dialogs and popovers | Enter from scale 0.98 and fade, with the transform origin at the trigger for popovers. The backdrop fades. Exit in about 120 ms. |
| Skeletons | Factoree shimmer. Content replaces the skeleton with a 120 ms cross-fade in the same box, so nothing shifts. |
| Drawers | The evidence and event detail drawer slides 16 px with fade, `base`. The list behind stays interactive. |

**Perceived speed**
- Links preload route code and data on hover and focus (TanStack Router `preload: "intent"`, 50 ms delay).
- Lists keep the previous data visible while new data loads (`placeholderData: keepPreviousData`), with a thin 2 px progress line at the top of the card instead of a skeleton.
- Optimistic updates as in the table above.
- Interaction budget: Interaction to Next Paint p75 of 100 ms or less on the main flows with 4x CPU slowdown. Animations keep 60 frames per second on the run page with 5,000 events.
