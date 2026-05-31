# Floating Chat Bubble — Design Document

**Bead:** bahia-2wbp  
**Status:** Design  
**Date:** 2026-05-30

---

## 1. Problem Summary

The current `AssistantSidebar` has several UX and implementation bugs:

| # | Issue | Root Cause |
|---|-------|-----------|
| 1 | **Cannot re-expand collapsed sidebar** | The toggle button lives inside `{#if assistantUi.open}`, so when closed there's no way to reopen |
| 2 | **64px ghost artifact** | `collapsed` sets width to 64px; layout's `margin-right` shifts main content even when the user wants the sidebar hidden |
| 3 | **Permanent screen real-estate cost** | Fixed sidebar with `height: 100vh` always occupies space, even idle |
| 4 | **Dated UX** | Full-height sidebars feel like 2018 admin dashboards, not modern chat |

**Files affected:**
- `web/src/lib/components/assistant/AssistantSidebar.svelte` — 158-line sidebar component
- `web/src/lib/stores/assistant.svelte.js` — `assistantUi` state (`open`, `collapsed`, `activeSessionId`)
- `web/src/routes/+layout.svelte` — mounts sidebar, computes `assistantWidth` for `margin-right`

---

## 2. Proposed Architecture: Floating Bubble + Overlay Panel

Replace the fixed sidebar with two distinct visual elements:

```
┌─────────────────────────────────┐
│           Main Content          │
│  (no margin-right adjustment)   │
│                                 │
│                                 │
│                                 │
│                           ┌───┐ │
│                           │ 💬│ │  ← Bubble (closed state)
│                           └───┘ │
└─────────────────────────────────┘

┌─────────────────────────────────┐
│           Main Content          │
│                      ┌────────┐ │
│                      │ Header │ │
│                      ├────────┤ │
│                      │Sessions│ │  ← Panel (open state)
│                      │Transcrp│ │    Floating overlay, no layout shift
│                      │        │ │
│                      ├────────┤ │
│                      │Composer│ │
│                      └────────┘ │
│                           ┌───┐ │
│                           │ 💬│ │
│                           └───┘ │
└─────────────────────────────────┘
```

**Key principle:** The bubble and panel are `position: fixed` overlays. Main content never shifts.

---

## 3. State Simplification

### Current state (problematic)

```js
// Three booleans create 4 possible states, 2 of which are broken
assistantUi = {
  open: true,        // sidebar visible at all
  collapsed: false,  // sidebar narrow (64px) — BUT toggle unreachable when !open
  activeSessionId: ''
}
```

### Proposed state

```js
assistantUi = {
  panelOpen: false,         // panel visible (overlay)
  activeSessionId: '',      // which session is displayed
  hasUnread: false,          // badge indicator on bubble
  lastDismissedAt: 0        // timestamp — suppress badge for events before this
}
```

**Changes:**
- **Remove `collapsed`** — no intermediate state; the panel is either open or closed
- **Rename `open` → `panelOpen`** — clearer intent
- **Add `hasUnread`** — drives the notification badge on the bubble
- **Add `lastDismissedAt`** — when user closes panel, mark the timestamp so we only badge genuinely new events

**Persistence** stays in `localStorage` under `bahia_assistant_sidebar` (same key, new shape). Migration: if old shape detected, map `open → panelOpen`, drop `collapsed`.

---

## 4. Bubble Design (Closed State)

### Position & Size

| Property | Value | Rationale |
|----------|-------|-----------|
| Position | `fixed`, bottom-right | Industry standard (Intercom, Drift, Crisp) |
| Inset | `bottom: 32px; right: 32px` | Comfortable clearance from edges and scrollbars |
| Size | `56px` circle | Large enough to tap on mobile, small enough to not occlude content |
| Z-index | `9000` | Above content, below modals (`9999`) |

### Visual Design

```
     ┌──────────┐
     │          │
     │   💬 ●  │   ← 56px circle, chat icon centered
     │          │     red dot = unread badge
     └──────────┘
```

- **Background:** `var(--primary)` (`#6366f1` dark / `#4f46e5` light)
- **Icon:** SVG chat bubble icon, white, 24px
- **Shadow:** `0 4px 12px rgba(0,0,0,0.25)` — elevated floating feel
- **Border-radius:** `50%` (perfect circle)

### Notification Badge

- **When shown:** `hasUnread === true` (new result/status events since `lastDismissedAt`)
- **Appearance:** 12px red (`var(--error)`) circle, top-right of bubble, white `2px` border ring
- **Content:** No count — just a dot (simpler, less distracting)

### Interaction States

| State | Visual |
|-------|--------|
| Default | `var(--primary)` background, subtle shadow |
| Hover | Scale to `1.08`, shadow intensifies to `0 6px 20px rgba(0,0,0,0.3)` |
| Active/pressed | Scale to `0.95`, brief press feedback |
| Panel open | Slight opacity reduction (`0.85`) or icon swaps to `✕` to indicate "close" |
| Has unread | Badge dot + gentle pulse animation (see §8) |

### Accessibility

- `role="button"`, `aria-label="Open assistant chat"` / `"Close assistant chat"`
- `aria-live="polite"` region announces unread count changes
- Keyboard focusable, activates on Enter/Space

---

## 5. Chat Panel Design (Open State)

### Dimensions & Position

| Property | Desktop | Mobile (≤640px) | Tablet (641–900px) |
|----------|---------|-----------------|---------------------|
| Width | `400px` | `calc(100vw - 24px)` | `380px` |
| Height | `min(560px, calc(100vh - 120px))` | `calc(100vh - 80px)` | `min(520px, calc(100vh - 100px))` |
| Position | Anchored above bubble: `bottom: 100px; right: 32px` | Centered: `bottom: 12px; left: 12px; right: 12px` | Same as desktop |

### Anatomy

```
┌──────────────────────────────────────┐
│ ■ Assistant              — ✕        │  ← Header (48px)
├──────────────────────────────────────┤
│ [Session 1] [Session 2] [Session 3] │  ← Session tabs (scrollable, 44px)
├──────────────────────────────────────┤
│                                      │
│  ┌─ You ─────────────────────────┐  │
│  │ How do I add a new relay?     │  │
│  └───────────────────────────────┘  │
│                                      │  ← Transcript (flex: 1, scrollable)
│  ┌─ Assistant ───────────────────┐  │
│  │ You can add relays in the     │  │
│  │ Settings page under ...       │  │
│  └───────────────────────────────┘  │
│                                      │
├──────────────────────────────────────┤
│ ┌──────────────────────────┐ [Send] │  ← Composer (auto-height, max 120px)
│ │ Ask the Bahia assistant… │        │
│ └──────────────────────────┘        │
└──────────────────────────────────────┘
```

### Header (48px)

- **Left:** Status indicator dot (colored by `assistantConnection.status`) + "Assistant" label
- **Right:** Minimize button (`—`) and Close button (`✕`)
  - **Minimize** = close panel, keep session alive (same as clicking bubble again)
  - **Close** = close panel (identical behavior — we don't need a "destroy session" close)
  - On reflection: **one button is enough** — just `✕` to close the panel. Clicking the bubble reopens it. Simpler.

### Session Tabs (44px)

- Horizontal scroll, same as current `.sessions` nav
- Active session has `border-bottom: 2px solid var(--primary)`
- "+" button at end to start a new session (calls `createAssistantSessionId()`)
- If only one session, tabs row still shows (but less prominent)

### Transcript Area

- `flex: 1; overflow-y: auto`
- Auto-scrolls to bottom on new messages (with "scroll to bottom" pill if user has scrolled up)
- Reuses existing `AssistantTurn` component
- `AssistantPlanApproval` inline for blocked sessions
- Empty state: centered illustration + "Ask the Bahia assistant for help"

### Composer

- Single-line `<textarea>` that grows to max 3 lines (120px)
- Send button right-aligned, primary color
- Disabled states: while submitting, while `waiting_auth`
- Enter to send, Shift+Enter for newline
- Cancel button appears when session state is `executing` or `blocked`

### Panel Chrome

- **Background:** `var(--card-bg)`
- **Border:** `1px solid var(--border-color)`
- **Border-radius:** `16px` (modern, rounded)
- **Shadow:** `0 8px 32px rgba(0,0,0,0.3)` — strong elevation to separate from page
- **Backdrop:** None (no dimming of main content — this isn't a modal)

---

## 6. Component Architecture

### New component tree

```
+layout.svelte
  └── AssistantChat.svelte          (new — replaces AssistantSidebar)
        ├── AssistantBubble.svelte   (new — the floating button)
        └── AssistantPanel.svelte    (new — the overlay chat panel)
              ├── AssistantSessionTabs.svelte  (new — horizontal session picker)
              ├── AssistantTurn.svelte          (existing — reuse as-is)
              ├── AssistantPlanApproval.svelte  (existing — reuse as-is)
              └── AssistantComposer.svelte      (new — extracted input form)
```

### Layout integration changes

**`+layout.svelte`** simplifications:
```diff
- import AssistantSidebar from '$lib/components/assistant/AssistantSidebar.svelte';
+ import AssistantChat from '$lib/components/assistant/AssistantChat.svelte';

- const assistantWidth = $derived(assistantUi.open ? (assistantUi.collapsed ? '64px' : '360px') : '0px');

  <div class="app">  <!-- no more style:--assistant-sidebar-width -->
    <Nav />
    <main>...</main>
-   <AssistantSidebar routeContext={assistantRouteContext} />
+   <AssistantChat routeContext={assistantRouteContext} />
  </div>
```

**`main` CSS** — remove `margin-right`:
```diff
  main {
    padding: 2rem;
    max-width: 1400px;
    margin: 0 auto;
-   margin-right: var(--assistant-sidebar-width, 0px);
  }
```

This is the single biggest UX win — main content reclaims the full viewport width.

---

## 7. Store Changes

### `assistant.svelte.js` modifications

```js
// BEFORE
export const assistantUi = $state({
  open: true,
  collapsed: false,
  activeSessionId: ''
});

// AFTER
export const assistantUi = $state({
  panelOpen: false,        // default closed — bubble visible on load
  activeSessionId: '',
  hasUnread: false,
  lastDismissedAt: 0
});
```

### New/modified exports

```js
// Replace setAssistantSidebarOpen + toggleAssistantCollapsed with:
export function toggleAssistantPanel() {
  assistantUi.panelOpen = !assistantUi.panelOpen;
  if (assistantUi.panelOpen) {
    assistantUi.hasUnread = false;
    assistantUi.lastDismissedAt = nowSeconds();
  }
  persistState();
}

export function openAssistantPanel() {
  assistantUi.panelOpen = true;
  assistantUi.hasUnread = false;
  assistantUi.lastDismissedAt = nowSeconds();
  persistState();
}

export function closeAssistantPanel() {
  assistantUi.panelOpen = false;
  assistantUi.lastDismissedAt = nowSeconds();
  persistState();
}

// Remove: setAssistantSidebarOpen, toggleAssistantCollapsed
```

### Unread detection

In `applyAssistantEvent`, after a new result/status event is applied:

```js
if (!assistantUi.panelOpen && event.created_at > assistantUi.lastDismissedAt) {
  assistantUi.hasUnread = true;
}
```

### Migration

```js
function loadState() {
  const stored = JSON.parse(localStorage.getItem(SIDEBAR_STORAGE_KEY) || '{}');
  // Migration from old shape
  if ('open' in stored && !('panelOpen' in stored)) {
    stored.panelOpen = stored.open && !stored.collapsed;
    delete stored.open;
    delete stored.collapsed;
  }
  // Apply...
}
```

---

## 8. Animation & Transitions

### Bubble Pulse (unread notification)

```css
@keyframes bubble-pulse {
  0%, 100% { box-shadow: 0 4px 12px rgba(0,0,0,0.25); }
  50% { box-shadow: 0 4px 12px rgba(0,0,0,0.25), 0 0 0 8px rgba(99,102,241,0.15); }
}

.bubble.has-unread {
  animation: bubble-pulse 2s ease-in-out infinite;
}
```

Pulse stops as soon as `hasUnread` becomes false (panel opened).

### Panel Open/Close

Use CSS transitions (not JS animation) for performance:

```css
.panel {
  transform-origin: bottom right;
  transition: transform 200ms cubic-bezier(0.32, 0.72, 0, 1),
              opacity 200ms ease;
}

.panel[data-state="closed"] {
  transform: scale(0.92) translateY(8px);
  opacity: 0;
  pointer-events: none;
}

.panel[data-state="open"] {
  transform: scale(1) translateY(0);
  opacity: 1;
}
```

**Why `transform-origin: bottom right`:** The panel grows *out of* the bubble position, creating a natural spatial connection.

### Bubble → Panel connection

When panel opens, the bubble icon cross-fades from 💬 to ✕ over 150ms. This signals "click again to close" without needing a separate close button (though we keep one in the header too).

---

## 9. Responsive Behavior

### Breakpoints

| Breakpoint | Bubble | Panel |
|------------|--------|-------|
| **Desktop** (>900px) | Bottom-right, 56px | 400×560px, above bubble |
| **Tablet** (641–900px) | Bottom-right, 52px | 380×520px, above bubble |
| **Mobile** (≤640px) | Bottom-right, 48px, inset 16px | Near-fullscreen with 12px margin all sides |

### Mobile-specific behavior

- Panel gets `border-radius: 12px` (slightly less rounded for more content space)
- Composer textarea is single-line by default (saves keyboard space)
- Session tabs become a dropdown/select if >3 sessions
- Optional: light backdrop dim (`rgba(0,0,0,0.3)`) behind panel on mobile since it covers most of the screen

### Keyboard handling (mobile)

When the virtual keyboard opens, the panel should:
1. Shrink its height to fit above the keyboard (`visualViewport` API)
2. Keep the composer visible (it's at the bottom, closest to the keyboard)
3. Transcript scrolls up naturally

```js
if (browser && 'visualViewport' in window) {
  visualViewport.addEventListener('resize', () => {
    const keyboardHeight = window.innerHeight - visualViewport.height;
    panelEl.style.setProperty('--keyboard-offset', `${keyboardHeight}px`);
  });
}
```

---

## 10. Accessibility

| Requirement | Implementation |
|-------------|---------------|
| Focus management | On panel open: focus moves to composer textarea. On close: focus returns to bubble |
| Escape to close | `keydown` handler on panel closes it |
| Screen reader | Bubble: `aria-label="Open assistant chat"` with live region for unread. Panel: `role="dialog" aria-label="Assistant chat"` |
| Reduced motion | `@media (prefers-reduced-motion: reduce)` disables pulse animation and uses instant open/close |
| Color contrast | Badge red dot has white border ring for visibility on primary background |
| Focus trap | Panel is NOT a focus trap (it's an overlay, not a modal). Users can tab past it to the page. |

---

## 11. Edge Cases & Decisions

| Scenario | Behavior |
|----------|----------|
| Page navigation | Panel stays open (it's in `+layout.svelte`). `routeContext` updates automatically. |
| Auth not ready | Bubble still visible but shows a "connecting..." tooltip on hover. Panel disabled. |
| Connection lost | Bubble shows orange dot instead of badge. Panel shows reconnecting banner at top. |
| Multiple rapid open/close | CSS transitions handle this naturally — interrupting a transition just reverses it. |
| Very long transcript | Virtual scrolling not needed initially (existing `overflow-y: auto` is fine for `TRANSCRIPT_LIMIT=300`). Can add later if performance requires it. |
| Panel overlaps important content | User can close the panel. Future: consider drag-to-reposition the bubble. |

---

## 12. Migration Plan

### Phase 1: New components (non-breaking)
1. Create `AssistantBubble.svelte`, `AssistantPanel.svelte`, `AssistantChat.svelte`, `AssistantComposer.svelte`, `AssistantSessionTabs.svelte`
2. Update `assistantUi` state shape with migration logic
3. Wire up in `+layout.svelte` behind a feature flag or just replace

### Phase 2: Layout cleanup
1. Remove `--assistant-sidebar-width` CSS variable from layout
2. Remove `margin-right` from `main`
3. Delete `AssistantSidebar.svelte`

### Phase 3: Polish
1. Add animations and transitions
2. Add unread badge logic
3. Mobile responsive testing
4. Accessibility audit

**Estimated effort:** ~4-6 hours for a working implementation, ~2 hours for polish.

---

## 13. Inspiration & References

| Product | What to borrow |
|---------|---------------|
| **Intercom Messenger** | Bubble position, panel anchor point, open/close animation |
| **GitHub Copilot Chat (VS Code)** | Session management, transcript layout, inline plan approval |
| **Linear Command Palette** | Clean typography, keyboard-first interaction, minimal chrome |
| **ChatGPT mobile** | Composer auto-grow, mobile keyboard handling |

---

## 14. Open Questions

1. **Should the bubble be hideable?** Some users may never use the assistant. Consider a setting to hide the bubble entirely.
2. **Drag-to-reposition?** Allow users to move the bubble to a different corner. Adds complexity — defer to v2.
3. **Sound/haptic on new message?** Probably not for v1, but the unread system makes it easy to add later.
4. **Multiple panels?** One panel at a time is simpler and matches all reference products. Multi-panel is a v2 consideration.
