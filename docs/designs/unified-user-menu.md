# Unified User Menu — Design Document

**Bead:** bahia-s6vu
**Date:** 2026-05-30
**Status:** Draft

---

## Context & Motivation

The current auth UI in `Nav.svelte` scatters user-facing controls across the topbar:

| State | Current Elements | Problem |
|---|---|---|
| Anonymous | `login-btn` + error `WarningIcon` | Single NIP-07 path; no NIP-46 option without visiting `/settings` |
| Loading | Inline spinner + text | Fine, but visually disconnected |
| Authenticated | `user-profile` pill + `auth-method` badge + `WarningIcon` + `logout-btn` | 3–4 separate elements; no path to Edit Profile or Manage Relays |

**Goal:** Collapse all auth-related UI into a single `UserMenu` component that occupies one slot in `nav-actions` and exposes login options, profile management, relay management, and logout through a dropdown.

---

## 1. Component Architecture

### File Structure

```
src/lib/components/
  UserMenu.svelte          ← new: top-level menu component
  user-menu-model.js       ← new: menu item definitions, keyboard helpers
```

No new sub-components needed initially — the dropdown is small enough for a single file. If Edit Profile or Manage Relays grow complex, they live at route level (`/settings/profile`, `/settings/relays`), not inside the dropdown.

### Component API

```svelte
<!-- Nav.svelte usage — replaces the entire auth-section div -->
<UserMenu />
```

`UserMenu` reads directly from the existing `authState` reactive store and the `authPresentation()` helper. No props needed; it is self-contained.

### State

```js
// Internal to UserMenu.svelte
let open = $state(false);        // dropdown visibility
let activeIndex = $state(-1);    // keyboard-navigated item index
let triggerEl = $state();        // button ref for focus return
let menuEl = $state();           // dropdown ref for outside-click detection
```

### Interaction with Auth Store

- **Reads:** `authState`, `isAuthenticated()` — via existing imports from `$lib/stores/auth.js`
- **Calls:** `login()`, `loginWithNostrConnect(uri)`, `logout()` — existing exports
- **Derives:** `authPresentation(authState, isAuthenticated())` — existing helper from `nav-model.js`

No auth store changes required.

---

## 2. Anonymous State Design

### Trigger Button

```
┌──────────────────────┐
│  ⚡  Sign In    ▾    │   ← primary-colored button, LoginIcon + chevron
└──────────────────────┘
```

- Uses `--primary` background, white text (same style as current `login-btn`)
- Chevron (▾) indicates dropdown availability
- If neither NIP-07 nor NIP-46 is available: icon switches to `WarningIcon`, label becomes "No Signer", button is `disabled`, title tooltip explains

### Dropdown — Anonymous

```
┌─────────────────────────────┐
│  Sign in with Nostr         │  ← section header (non-interactive)
├─────────────────────────────┤
│  🔌  Browser Extension      │  ← NIP-07 login
│      Use NIP-07 signer      │     greyed + "(not detected)" if unavailable
├─────────────────────────────┤
│  🔗  Nostr Connect          │  ← navigates to /settings (NIP-46 flow)
│      Use NIP-46 remote      │     always available
│      signer                 │
└─────────────────────────────┘
```

**Behavior:**
- **Browser Extension** item calls `login()` directly. If `!authState.extensionAvailable`, the item is visually muted with helper text "(not detected)" and disabled.
- **Nostr Connect** item navigates to `/settings#nostr-connect` where the QR/URI flow already exists. This avoids duplicating the QR scanner inside the dropdown.

### Error States

- If `authState.status === 'error'`, the dropdown shows an inline error banner above the options:
  ```
  ┌─────────────────────────────┐
  │  ⚠ Login failed: <message>  │  ← warning-colored bar
  ├─────────────────────────────┤
  │  🔌  Browser Extension      │
  │  ...                        │
  ```
- The trigger button itself does NOT show a separate warning icon — the dropdown contains it.

### Loading State

While `status === 'checking'` or `'authenticating'`:
- Trigger button shows spinner + "Signing in…"
- Dropdown is not openable (click is a no-op)

---

## 3. Authenticated State Design

### Trigger Button

```
┌────────────────────────────────┐
│  [avatar]  Display Name   ▾   │   ← pill shape, same as current user-profile
└────────────────────────────────┘
```

- Reuses existing `user-profile` styling (pill, avatar, name)
- Removes the inline `auth-method` badge and `logout-btn` — those move into the dropdown
- Chevron (▾) replaces the auth-method badge position
- `cursor: pointer` (currently `cursor: default`)
- If no avatar: fallback initials circle (existing `profileInitials()`)

### Dropdown — Authenticated

```
┌──────────────────────────────────┐
│  [avatar]  Display Name          │  ← profile summary header
│            npub1abc…wxyz         │
│            user@nip05.example    │
├──────────────────────────────────┤
│  👤  Edit Profile                │  → /settings/profile
│  📡  Manage Relays              │  → /settings/relays
├──────────────────────────────────┤
│  🔐  NIP-07 Extension           │  ← auth method indicator (read-only)
├──────────────────────────────────┤
│  🚪  Log out                    │  ← calls logout()
└──────────────────────────────────┘
```

**Profile summary header** is non-interactive but provides at-a-glance identity confirmation. Shows avatar (or initials fallback), display name, truncated npub, and NIP-05 if available.

**Menu items:**
| Item | Action | Notes |
|---|---|---|
| Edit Profile | `goto('/settings/profile')` | New route (see §3.1) |
| Manage Relays | `goto('/settings/relays')` | New route (see §3.1) |
| Auth method | — | Read-only badge, shows "NIP-07 Extension" or "NIP-46 Remote Signer" |
| Log out | `logout()` | Closes dropdown, resets to anonymous trigger |

**Warning state:** If `authUi.showWarning` is true (backend auth failed), a small warning bar appears below the profile header:
```
│  ⚠ Backend auth unavailable    │
```

### 3.1 New Routes

| Route | Purpose | Implementation |
|---|---|---|
| `/settings/profile` | Edit Nostr kind-0 metadata (display name, about, picture, NIP-05) | New `+page.svelte`; signs and publishes kind-0 event via `signWithAuth()` |
| `/settings/relays` | Manage relay list (add/remove/reorder) | Extract existing relay management from `/settings/+page.svelte` into its own route |

These routes are **out of scope** for the UserMenu component itself but are referenced by it. They should be tracked as separate implementation beads.

The existing `/settings` page already contains relay management UI and NIP-46 connect. The new routes would extract and refine those sections. `/settings` remains as an index/hub linking to sub-pages.

---

## 4. Interaction Patterns

### Open / Close

| Trigger | Behavior |
|---|---|
| Click trigger button | Toggle dropdown open/closed |
| Click outside dropdown | Close |
| Press `Escape` | Close, return focus to trigger |
| Navigate to a route (menu item click) | Close (existing `$page.url` effect) |
| Call `logout()` | Close implicitly (state change re-renders to anonymous trigger) |

### Keyboard Navigation

Follows WAI-ARIA Menu Button pattern:

| Key | Behavior |
|---|---|
| `Enter` / `Space` on trigger | Open dropdown, focus first item |
| `ArrowDown` | Move focus to next item (wraps) |
| `ArrowUp` | Move focus to previous item (wraps) |
| `Enter` / `Space` on item | Activate item |
| `Escape` | Close, return focus to trigger |
| `Tab` | Close dropdown, move focus naturally |
| `Home` | Focus first item |
| `End` | Focus last item |

Implementation in `user-menu-model.js`:

```js
export function menuKeyHandler(event, { items, activeIndex, close }) {
  switch (event.key) {
    case 'ArrowDown':
      event.preventDefault();
      return (activeIndex + 1) % items.length;
    case 'ArrowUp':
      event.preventDefault();
      return (activeIndex - 1 + items.length) % items.length;
    case 'Home':
      event.preventDefault();
      return 0;
    case 'End':
      event.preventDefault();
      return items.length - 1;
    case 'Escape':
      event.preventDefault();
      close();
      return -1;
    case 'Tab':
      close();
      return -1;
    default:
      return activeIndex;
  }
}
```

### ARIA Attributes

```html
<!-- Trigger -->
<button
  aria-haspopup="menu"
  aria-expanded={open}
  aria-controls="user-menu"
>

<!-- Dropdown -->
<div
  id="user-menu"
  role="menu"
  aria-label="User menu"
>
  <div role="menuitem" tabindex={activeIndex === 0 ? 0 : -1}>Edit Profile</div>
  <div role="menuitem" tabindex={activeIndex === 1 ? 0 : -1}>Manage Relays</div>
  <!-- separator -->
  <div role="menuitem" tabindex={activeIndex === 2 ? 0 : -1}>Log out</div>
</div>
```

### Mobile Considerations

At `≤560px`, the dropdown should shift to a **bottom sheet** pattern:

- Full-width, anchored to bottom of viewport
- Semi-transparent backdrop overlay (click to dismiss)
- Slightly taller touch targets (min 48px item height)
- Slide-up animation (200ms ease-out)

This avoids the dropdown clipping or overflowing on small screens. Implementation uses a CSS media query to switch from `position: absolute` (desktop) to `position: fixed; bottom: 0` (mobile).

---

## 5. Visual Specifications

### Design Tokens (existing CSS variables)

```css
--card-bg         /* dropdown background */
--border-color    /* dropdown border, separators */
--text-primary    /* item labels, profile name */
--text-muted      /* secondary text, descriptions */
--hover-bg        /* item hover/focus background */
--primary         /* accent color for active states, login button */
--warning         /* error/warning banners */
--nav-bg          /* trigger area background (inherited) */
```

No new CSS variables needed.

### Dropdown Panel

```css
.user-menu-dropdown {
  position: absolute;
  top: calc(100% + 8px);        /* 8px gap below trigger */
  right: 0;                     /* right-aligned to trigger */
  min-width: 280px;
  max-width: 360px;
  background: var(--card-bg);
  border: 1px solid var(--border-color);
  border-radius: 12px;          /* matches .nav-section */
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.25);
  z-index: 100;
  overflow: hidden;
}
```

### Menu Items

```css
.user-menu-item {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  padding: 0.625rem 1rem;       /* comfortable touch target */
  color: var(--text-primary);
  font-size: 0.875rem;
  cursor: pointer;
  transition: background 0.1s;
}

.user-menu-item:hover,
.user-menu-item:focus-visible {
  background: var(--hover-bg);
  outline: none;
}

.user-menu-item[aria-disabled="true"] {
  opacity: 0.5;
  cursor: not-allowed;
}
```

### Separator

```css
.user-menu-separator {
  height: 1px;
  background: var(--border-color);
  margin: 0.25rem 0;
}
```

### Profile Header (authenticated dropdown)

```css
.user-menu-profile {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  padding: 0.875rem 1rem;
  border-bottom: 1px solid var(--border-color);
}

.user-menu-profile .profile-avatar {
  width: 40px;                  /* slightly larger than topbar (30px) */
  height: 40px;
}
```

### Animation

```css
.user-menu-dropdown {
  animation: menu-enter 0.15s ease-out;
}

@keyframes menu-enter {
  from {
    opacity: 0;
    transform: translateY(-4px) scale(0.98);
  }
  to {
    opacity: 1;
    transform: translateY(0) scale(1);
  }
}
```

### Mobile Bottom Sheet Override

```css
@media (max-width: 560px) {
  .user-menu-backdrop {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.4);
    z-index: 99;
  }

  .user-menu-dropdown {
    position: fixed;
    bottom: 0;
    left: 0;
    right: 0;
    top: auto;
    min-width: unset;
    max-width: unset;
    border-radius: 16px 16px 0 0;
    animation: menu-slide-up 0.2s ease-out;
  }

  @keyframes menu-slide-up {
    from {
      transform: translateY(100%);
    }
    to {
      transform: translateY(0);
    }
  }

  .user-menu-item {
    min-height: 48px;           /* WCAG touch target */
    padding: 0.75rem 1.25rem;
  }
}
```

---

## 6. Integration Plan

### Changes to Existing Files

**`Nav.svelte`** — Remove the entire `<div class="auth-section">…</div>` block (lines ~146–186) and replace with:
```svelte
<UserMenu />
```

Also remove:
- `handleLogin()` function
- `handleLogout()` function
- `profileInitials()` function (moves into `UserMenu.svelte`)
- The `authUi` derived block (moves into `UserMenu.svelte`)
- Related CSS (`.auth-section`, `.user-info`, `.user-profile`, `.login-btn`, `.logout-btn`, `.auth-method`, `.profile-*`, `.auth-loading`, `.auth-error`, `.auth-warning`, `.spinner`)

**`nav-model.js`** — No changes. `authPresentation()` and `truncatePubkey()` continue to be consumed by `UserMenu`.

**`auth.svelte.js`** — No changes. All exports remain as-is.

### New Files

| File | Purpose |
|---|---|
| `src/lib/components/UserMenu.svelte` | Menu component |
| `src/lib/components/user-menu-model.js` | Menu items config, keyboard handler |
| `src/routes/settings/profile/+page.svelte` | Profile editing page (future bead) |
| `src/routes/settings/relays/+page.svelte` | Relay management page (future bead) |

### Migration Safety

The refactor is purely UI-structural. Auth state management is untouched. The `UserMenu` component consumes the same reactive state and calls the same functions. This can be shipped incrementally:

1. **Phase 1:** Ship `UserMenu.svelte` replacing the auth-section in Nav (this bead)
2. **Phase 2:** Add `/settings/profile` route (new bead)
3. **Phase 3:** Extract relay UI into `/settings/relays` (new bead)

Until Phase 2/3 land, the "Edit Profile" and "Manage Relays" items can link to `/settings` (existing page that already has relay management).

---

## 7. Accessibility Checklist

- [x] WAI-ARIA Menu Button pattern (`aria-haspopup`, `aria-expanded`, `role="menu"`, `role="menuitem"`)
- [x] Keyboard fully navigable (arrows, Enter, Escape, Home, End)
- [x] Focus management (focus first item on open, return focus on close)
- [x] Screen reader: trigger announces state ("User menu, expanded/collapsed")
- [x] Disabled items have `aria-disabled="true"` (not removed from DOM)
- [x] Color contrast: inherits from theme system (already WCAG AA)
- [x] Touch targets ≥48px on mobile
- [x] Reduced motion: respect `prefers-reduced-motion` (disable animations)

```css
@media (prefers-reduced-motion: reduce) {
  .user-menu-dropdown {
    animation: none;
  }
}
```

---

## 8. Open Questions

1. **Should "Switch Account" be a menu item?** — Nostr supports multiple keypairs. Deferring for now since `auth.svelte.js` only tracks one session.
2. **Notification badge on trigger?** — The `/notifications` route exists. Could show an unread count dot on the avatar. Out of scope for this design but worth noting.
3. **Dark/light mode toggle in the menu?** — Currently in `nav-actions` as a separate button. Could move into the authenticated dropdown as a toggle row. Recommend keeping it separate since it's useful for anonymous users too.
