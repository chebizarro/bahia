# Consolidated Navigation Design

> **Bead:** bahia-2j1z  
> **Date:** 2026-05-30  
> **Status:** Draft

---

## 1. Problem Statement

The current navigation has two overlapping layers that create cognitive friction:

| Layer | What it shows | Problem |
|-------|--------------|---------|
| **Topbar** `PRIMARY_NAV_LINKS` | Dashboard, Services, Packages, Environments | Cherry-picks 4 of 20 routes with no clear rationale |
| **Mega menu** `NAV_SECTIONS` | All 4 sections, all links | Duplicates every topbar item, making the topbar feel redundant |

Users see the same items in two places, wonder which is "real," and still need to open the menu to reach most destinations. The topbar links also hide at `≤960px`, meaning mobile users get none of them — proving they aren't essential.

Additionally, two route groups (**DNS** and **Notifications**) exist in the codebase but are absent from `nav-model.js` entirely.

---

## 2. Navigation Philosophy

### Principles

1. **Single source of truth.** Every navigable route lives in `NAV_SECTIONS`. Nothing else renders navigation links. No duplication.
2. **Topbar is chrome, not nav.** The topbar's job is: brand identity, show where you are, surface quick actions, and give access to user/system controls. It does *not* list page links.
3. **Menu is the map.** One menu, opened explicitly, gives organized access to everything. Users learn one pattern.
4. **Context over shortcuts.** Instead of pinning arbitrary "primary" links, the topbar shows a breadcrumb or section indicator so users always know where they are.

### What changes

| Concern | Current | Proposed |
|---------|---------|----------|
| Page navigation | Topbar links + Menu | Menu only |
| "Where am I?" | Active link highlight (if visible) | Breadcrumb / section label in topbar |
| Quick access | 4 hardcoded links | Context-aware actions (deploy, create, etc.) |
| Route coverage | 15 of ~21 routes | All routes represented |

---

## 3. Information Architecture

### 3.1 Revised Section Groupings

The current 4 sections are a solid start. Proposed changes:

| Section | Current links | Proposed links | Changes |
|---------|--------------|----------------|---------|
| **Workspace** | Dashboard, Orgs, Souls | Dashboard, Orgs, Souls | *No change* |
| **Delivery** | Services, Artifacts, Packages, Deployments, Pending Approvals | Services, Artifacts, Packages, Deployments, Pending Approvals | *No change* — grouping is clean |
| **Operations** | Environments, Workers, Backup, Continuity, Inference, LLM, Payments, Policies, Events | Environments, Workers, Backup, Continuity, DNS, Notifications, Events | Split into two sub-groups (see below) |
| **Intelligence** | *(new)* | Inference, LLM | Extracted from Operations — these are conceptually distinct |
| **Admin** | Settings | Settings, Payments, Policies | Payments and Policies are administrative concerns, not operational |

#### Why split Operations?

Operations currently has **9 items** — the largest section by far. It mixes infrastructure concerns (Environments, Workers, Backup) with AI/ML concerns (Inference, LLM) and governance concerns (Payments, Policies). Splitting improves scannability:

- **Operations** (6 items): Infra + observability — things you monitor and maintain
- **Intelligence** (2 items): AI/ML workloads — a growing category that deserves its own home
- **Admin** (3 items): Governance, billing, configuration

### 3.2 Icons per Section

Each section gets an icon for visual anchoring in the menu:

| Section | Icon concept | Rationale |
|---------|-------------|-----------|
| Workspace | `LayoutDashboard` / grid | Home base, overview |
| Delivery | `Rocket` / package | Shipping software |
| Operations | `Server` / activity | Infrastructure |
| Intelligence | `Brain` / sparkles | AI/ML workloads |
| Admin | `Shield` / cog | Governance & config |

Individual links can optionally carry icons too, but section-level icons are the priority for v1.

### 3.3 Updated `nav-model.js` Structure

```javascript
export const NAV_SECTIONS = [
  {
    title: 'Workspace',
    icon: 'layout-dashboard',
    links: [
      { href: '/', label: 'Dashboard' },
      { href: '/orgs', label: 'Orgs' },
      { href: '/souls', label: 'Souls' }
    ]
  },
  {
    title: 'Delivery',
    icon: 'rocket',
    links: [
      { href: '/services', label: 'Services' },
      { href: '/artifacts', label: 'Artifacts' },
      { href: '/packages', label: 'Packages' },
      { href: '/deployments', label: 'Deployments' },
      { href: '/deployments/pending', label: 'Pending Approvals', badge: true }
    ]
  },
  {
    title: 'Operations',
    icon: 'server',
    links: [
      { href: '/environments', label: 'Environments' },
      { href: '/workers', label: 'Workers' },
      { href: '/backup', label: 'Backup' },
      { href: '/continuity', label: 'Continuity' },
      { href: '/dns', label: 'DNS' },
      { href: '/notifications', label: 'Notifications' },
      { href: '/events', label: 'Events' }
    ]
  },
  {
    title: 'Intelligence',
    icon: 'brain',
    links: [
      { href: '/ml', label: 'Inference' },
      { href: '/llm', label: 'LLM' }
    ]
  },
  {
    title: 'Admin',
    icon: 'shield',
    links: [
      { href: '/payments', label: 'Payments' },
      { href: '/policies', label: 'Policies' },
      { href: '/settings', label: 'Settings' }
    ]
  }
];

// PRIMARY_NAV_LINKS is removed entirely.
// Breadcrumb logic derives the current section from NAV_SECTIONS.
```

---

## 4. Topbar Design

### 4.1 Layout (left → right)

```
┌──────────────────────────────────────────────────────────────────┐
│  [≡]  Logo   Section / Page Title          🔔  👤 User   ◐     │
└──────────────────────────────────────────────────────────────────┘
```

| Slot | Element | Behavior |
|------|---------|----------|
| 1 | **Menu trigger** | Hamburger icon `≡` — always visible, all breakpoints |
| 2 | **Logo** | `brand-logo`, links to `/`. Sized down from 63px → 40px height to save vertical space |
| 3 | **Location indicator** | Section name + page title as a lightweight breadcrumb (e.g., "Delivery › Services") |
| 4 | **Quick actions** | *(Future)* Context-aware buttons (e.g., "New deployment" when on `/deployments`) — omit for v1 |
| 5 | **Notifications bell** | Badge count for unread notifications (links to `/notifications`) |
| 6 | **User chip** | Avatar + name, click to expand auth actions (logout, profile) |
| 7 | **Theme toggle** | Sun/moon icon |

### 4.2 Key Decisions

- **Menu trigger moves to the left**, next to the logo. This is the universal convention (GitHub, Slack, Linear) and frees the right side for user-centric controls.
- **`PRIMARY_NAV_LINKS` is deleted.** No more inline page links in the topbar.
- **Breadcrumb replaces inline links.** The section/page indicator gives spatial awareness without duplicating navigation. Derived from `NAV_SECTIONS` + current `$page.url.pathname`.
- **ConnectionStatus stays** in the actions area, near the user chip.
- **Topbar height is reduced.** Removing the link row and shrinking the logo yields a more compact bar (~56px vs current ~95px).

### 4.3 Breadcrumb Derivation

```javascript
export function currentLocation(pathname) {
  for (const section of NAV_SECTIONS) {
    const match = section.links.find(link => isActiveNavLink(pathname, link.href));
    if (match) {
      return { section: section.title, page: match.label, icon: section.icon };
    }
  }
  return { section: null, page: null, icon: null };
}
```

Renders as: **Operations › Workers** with muted section name and bold page name.

---

## 5. Menu Design

### 5.1 Interaction Pattern: Slide-out Drawer

| Option | Pros | Cons | Verdict |
|--------|------|------|---------|
| **Dropdown** | Lightweight, no overlay | Limited space, awkward for 5 sections | ❌ |
| **Full overlay** | Maximum space | Heavy, covers content entirely | ❌ |
| **Slide-out drawer** | Familiar pattern, preserves partial content view, animates naturally | Needs backdrop handling | ✅ |

The drawer slides in from the **left** (aligned with the menu trigger), covers ~320px on desktop, full-width on mobile. A semi-transparent backdrop covers the rest.

### 5.2 Drawer Structure

```
┌─────────────────────────┐
│  ✕ Close        [logo]  │  ← Header
│─────────────────────────│
│  🔍 Search...           │  ← Optional: filter links (v2)
│─────────────────────────│
│  ◫ Workspace            │  ← Section header with icon
│    Dashboard             │
│    Orgs                  │
│    Souls                 │
│─────────────────────────│
│  🚀 Delivery            │
│    Services              │
│    Artifacts             │
│    Packages              │
│    Deployments           │
│    Pending Approvals [●] │  ← Badge
│─────────────────────────│
│  ⊞ Operations           │
│    Environments          │
│    Workers               │
│    ...                   │
│─────────────────────────│
│  🧠 Intelligence        │
│    Inference             │
│    LLM                   │
│─────────────────────────│
│  🛡 Admin               │
│    Payments              │
│    Policies              │
│    Settings              │
└─────────────────────────┘
```

### 5.3 Visual Design

- **Section headers**: Uppercase eyebrow text with section icon, muted color. Separators between sections.
- **Active item**: Left accent bar (3px, `--primary` color) + bold text + subtle background highlight.
- **Active section**: Section header text uses `--primary` color.
- **Hover state**: Background fill, smooth transition (150ms).
- **Badges**: Small pill (existing `.badge` style) for items like Pending Approvals.

### 5.4 Keyboard Navigation

| Key | Action |
|-----|--------|
| `Escape` | Close drawer (already implemented) |
| `Tab` / `Shift+Tab` | Move through links sequentially |
| `ArrowDown` / `ArrowUp` | Move between links within a section |
| `Home` / `End` | Jump to first/last link |
| `Enter` | Navigate to focused link, close drawer |

Focus is trapped inside the drawer when open (add `inert` attribute to main content). On open, focus moves to the close button (already implemented). On close, focus returns to the menu trigger (already implemented).

### 5.5 Animation

```css
.navigation-drawer {
  transform: translateX(-100%);
  transition: transform 250ms cubic-bezier(0.4, 0, 0.2, 1);
}

.navigation-drawer.open {
  transform: translateX(0);
}

.drawer-backdrop {
  opacity: 0;
  transition: opacity 200ms ease;
}

.drawer-backdrop.visible {
  opacity: 1;
  background: rgba(0, 0, 0, 0.5);
}
```

---

## 6. Responsive Behavior

### 6.1 Breakpoint Summary

| Breakpoint | Topbar | Menu | Notes |
|------------|--------|------|-------|
| **Desktop** (>960px) | Full: trigger + logo + breadcrumb + actions | 320px side drawer | Backdrop dims content |
| **Tablet** (561–960px) | Compact: trigger + logo (smaller) + actions | 320px side drawer | Breadcrumb hidden or collapsed to section icon only |
| **Mobile** (≤560px) | Minimal: trigger + logo + theme + user avatar | Full-width drawer | User chip collapses to avatar only; auth actions move into drawer footer |

### 6.2 Desktop (>960px)

- Topbar shows all elements at full fidelity.
- Drawer is 320px wide, positioned fixed/absolute on the left.
- Content behind drawer is dimmed but partially visible.
- Hover states on all interactive elements.

### 6.3 Tablet (561–960px)

- Logo shrinks (40px → 32px height).
- Breadcrumb shows section name only (no page name), or collapses to icon.
- Auth section hides secondary text (NIP-05, auth method) — already implemented at 720px.
- Drawer remains 320px, doesn't change.

### 6.4 Mobile (≤560px)

- Topbar is single row: `[≡] [Logo] ........... [🔔] [👤] [◐]`
- User chip collapses to avatar-only (no name text).
- Drawer goes full-width (`width: 100vw`), feels like a full-screen takeover.
- Sections stack vertically (already implemented with `grid-template-columns: 1fr`).
- Add a footer inside the drawer for logout button and connection status (moves out of topbar).

---

## 7. Migration Plan

### Phase 1: Data Model (Low risk)

1. Add missing routes to `nav-model.js`: DNS (`/dns`), Notifications (`/notifications`).
2. Restructure sections: extract Intelligence, move Payments/Policies to Admin.
3. Add `icon` field to each section.
4. Remove `PRIMARY_NAV_LINKS` export.
5. Add `currentLocation()` helper function.

### Phase 2: Topbar Refactor

1. Remove `.primary-links` list from `Nav.svelte`.
2. Add menu trigger (hamburger icon) to the left of the logo.
3. Add breadcrumb component after the logo.
4. Reposition `.nav-actions` (move ConnectionStatus, compact auth, keep theme toggle).
5. Reduce logo height.

### Phase 3: Drawer Conversion

1. Change drawer from dropdown panel to left-side slide-out.
2. Add backdrop overlay.
3. Render section icons next to section headers.
4. Add left-accent active indicator.
5. Implement focus trapping (`inert` on main content).

### Phase 4: Responsive Polish

1. Implement tablet breakpoint adjustments (breadcrumb collapse).
2. Implement mobile full-width drawer.
3. Move auth overflow items into drawer footer on mobile.
4. Test keyboard navigation across breakpoints.

---

## 8. Files Affected

| File | Changes |
|------|---------|
| `web/src/lib/components/nav-model.js` | Restructure sections, remove `PRIMARY_NAV_LINKS`, add icons, add `currentLocation()` |
| `web/src/lib/components/Nav.svelte` | Remove inline links, add hamburger + breadcrumb, convert drawer to slide-out |
| `web/src/lib/icons/domain-icons.js` | Add section icons (if not already available via Lucide) |
| `web/src/routes/+layout.svelte` | May need `inert` attribute toggling for focus trap |

---

## 9. Open Questions

1. **Collapsible sections in drawer?** For power users who want to hide sections they don't use. Adds complexity — defer to v2?
2. **Keyboard shortcut to open menu?** e.g., `Cmd+K` or `Ctrl+/` — could double as a command palette later.
3. **Pinned favorites?** Let users pin frequently-used links to the top of the drawer. Nice-to-have, not v1.
4. **Quick actions in topbar?** Context-aware buttons like "Deploy" or "Create Service" based on current page. Useful but adds complexity — recommend deferring.

---

## 10. Summary

The core change is simple: **delete the topbar nav links, keep the menu as the single navigation surface, and use the freed topbar space for a breadcrumb that tells users where they are.** The menu becomes a proper side drawer with better visual hierarchy and complete route coverage. The data model gets cleaner (one export instead of two), and every route in the app gets a home.
