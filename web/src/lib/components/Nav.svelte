<script>
  import { tick } from 'svelte';
  import { page } from '$app/stores';
  import { theme, toggleTheme } from '$lib/stores/theme.js';
  import { authState, isAuthenticated, login, logout } from '$lib/stores/auth.js';
  import {
    DeploymentIcon,
    LlmIcon,
    LoginIcon,
    MoonIcon,
    ProtectedIcon,
    ServiceIcon,
    SunIcon,
    WarningIcon,
    WorkspaceIcon
  } from '$lib/icons/domain-icons.js';
  import ConnectionStatus from '$lib/components/ConnectionStatus.svelte';
  import {
    NAV_SECTIONS,
    authPresentation,
    currentLocation,
    truncatePubkey,
    isActiveNavLink,
    isActiveNavSection
  } from '$lib/components/nav-model.js';

  // Derive auth UI state — read all relevant $state properties directly so
  // Svelte 5 tracks each one and re-runs when profile loads asynchronously.
  let authUi = $derived.by(() => {
    const authenticated = isAuthenticated();
    const base = authPresentation(authState, authenticated);
    if (authenticated) {
      // Re-read profile directly here to guarantee fine-grained tracking
      const profile = authState.profile || null;
      return {
        ...base,
        profile,
        displayLabel: profile?.displayName || profile?.name || truncatePubkey(authState.pubkey || ''),
        nip05: profile?.nip05 || '',
        avatarUrl: profile?.picture || ''
      };
    }
    return base;
  });
  const SECTION_ICONS = {
    'layout-dashboard': WorkspaceIcon,
    rocket: DeploymentIcon,
    server: ServiceIcon,
    brain: LlmIcon,
    shield: ProtectedIcon
  };

  let menuOpen = $state(false);
  let menuButton = $state();
  let drawerCloseButton = $state();
  let previousMenuOpen = false;
  let location = $derived(currentLocation($page.url.pathname));

  $effect(() => {
    $page.url.pathname;
    menuOpen = false;
  });

  $effect(() => {
    const currentlyOpen = menuOpen;

    queueMicrotask(async () => {
      if (currentlyOpen) {
        await tick();
        drawerCloseButton?.focus();
      } else if (previousMenuOpen) {
        menuButton?.focus();
      }
      previousMenuOpen = currentlyOpen;
    });
  });

  async function handleLogin() {
    try {
      await login();
    } catch (error) {
      console.error('Login failed:', error);
    }
  }

  function handleLogout() {
    logout();
  }

  function profileInitials(label) {
    const text = String(label || '').trim();
    if (!text) return 'N';
    const parts = text.split(/\s+/).filter(Boolean).slice(0, 2);
    const initials = parts.map((part) => part[0]?.toUpperCase() || '').join('');
    return initials || text.slice(0, 1).toUpperCase();
  }

  function toggleMenu() {
    menuOpen = !menuOpen;
  }

  function closeMenu() {
    menuOpen = false;
  }

  function handleWindowKeydown(event) {
    if (event.key === 'Escape') {
      closeMenu();
    }
  }
</script>

<svelte:window onkeydown={handleWindowKeydown} />

<div class="nav-shell">
  <nav class="topbar" aria-label="Primary">
    <button
      type="button"
      class="menu-toggle"
      aria-controls="navigation-drawer"
      aria-expanded={menuOpen}
      aria-label={menuOpen ? 'Close navigation menu' : 'Open navigation menu'}
      bind:this={menuButton}
      onclick={toggleMenu}
    >
      <span aria-hidden="true">☰</span>
    </button>

    <a class="brand" href="/" aria-label="Bahia home">
      <img
        class="brand-logo"
        src={theme.value === 'dark' ? '/branding/logo_wide_dm.png' : '/branding/logo_wide_lm.png'}
        alt="Bahia"
      />
    </a>

    <div class="breadcrumb" aria-label="Current location">
      {#if location.section && location.page}
        <span class="breadcrumb-section">{location.section}</span>
        <span class="breadcrumb-separator" aria-hidden="true">›</span>
        <span class="breadcrumb-page">{location.page}</span>
      {:else}
        <span class="breadcrumb-page">Bahia</span>
      {/if}
    </div>

    <div class="nav-actions">
      <ConnectionStatus />

      <div class="auth-section">
        {#if authUi.mode === 'loading'}
          <span class="auth-loading">
            <span class="spinner"></span>
            {authUi.label}
          </span>
        {:else if authUi.mode === 'authenticated'}
          <div class="user-info">
            <div class="user-profile" title="{authUi.pubkey}\n{authUi.nip05 ? authUi.nip05 : ''}">
              {#if authUi.avatarUrl}
                <img
                  class="profile-avatar"
                  src={authUi.avatarUrl}
                  alt={authUi.displayLabel}
                  onerror={(e) => e.currentTarget.style.display='none'}
                />
              {:else}
                <span class="profile-avatar profile-avatar-fallback">{profileInitials(authUi.displayLabel)}</span>
              {/if}
              <span class="profile-copy">
                <span class="profile-name">{authUi.displayLabel}</span>
                <span class="profile-secondary">{authUi.nip05 || authUi.truncatedPubkey}</span>
              </span>
              <span class="auth-method">{authState.authMethod === 'nip46' ? 'NIP-46' : 'NIP-07'}</span>
            </div>
            {#if authUi.showWarning}
              <span class="auth-warning" title={authUi.warning} aria-label={authUi.warning || 'Authentication warning'}>
                <WarningIcon size={18} strokeWidth={1.75} ariaHidden="true" />
              </span>
            {/if}
            <button class="logout-btn" onclick={handleLogout}>
              Log out
            </button>
          </div>
        {:else}
          <button
            class="login-btn"
            onclick={handleLogin}
            disabled={!authUi.extensionAvailable}
            title={authUi.buttonTitle}
          >
            {#if authUi.extensionAvailable}
              <LoginIcon size={16} strokeWidth={1.75} ariaHidden="true" />
            {:else}
              <WarningIcon size={16} strokeWidth={1.75} ariaHidden="true" />
            {/if}
            {authUi.buttonLabel}
          </button>
          {#if authUi.showError}
            <span class="auth-error" title={authUi.error} aria-label={authUi.error || 'Authentication error'}>
              <WarningIcon size={18} strokeWidth={1.75} ariaHidden="true" />
            </span>
          {/if}
        {/if}
      </div>

      <button class="theme-toggle" onclick={toggleTheme} aria-label="Toggle theme">
        {#if theme.value === 'dark'}
          <SunIcon size={18} strokeWidth={1.75} ariaHidden="true" />
        {:else}
          <MoonIcon size={18} strokeWidth={1.75} ariaHidden="true" />
        {/if}
      </button>
    </div>
  </nav>

  {#if menuOpen}
    <button type="button" class="drawer-backdrop" aria-label="Close navigation menu" onclick={closeMenu}></button>
    <nav id="navigation-drawer" class="navigation-drawer open" aria-label="All navigation links">
      <div class="drawer-header">
        <div>
          <p class="drawer-eyebrow">Browse</p>
          <p class="drawer-title">All destinations</p>
        </div>
        <button type="button" class="drawer-close" bind:this={drawerCloseButton} onclick={closeMenu}>Close</button>
      </div>

      <div class="nav-sections">
        {#each NAV_SECTIONS as section}
          {@const SectionIcon = SECTION_ICONS[section.icon]}
          <section class:active-section={isActiveNavSection($page.url.pathname, section)} class="nav-section">
            <h2>
              {#if SectionIcon}
                <SectionIcon size={16} strokeWidth={1.8} ariaHidden="true" />
              {/if}
              <span>{section.title}</span>
            </h2>
            <ul>
              {#each section.links as link}
                <li>
                  <a
                    href={link.href}
                    class:active={isActiveNavLink($page.url.pathname, link.href)}
                    class:with-badge={link.badge}
                    aria-current={isActiveNavLink($page.url.pathname, link.href) ? 'page' : undefined}
                    onclick={closeMenu}
                  >
                    <span>{link.label}</span>
                    {#if link.badge}
                      <span class="badge">{link.badge}</span>
                    {/if}
                  </a>
                </li>
              {/each}
            </ul>
          </section>
        {/each}
      </div>
    </nav>
  {/if}
</div>

<style>
  .nav-shell {
    background: var(--nav-bg, #0f0f1a);
    border-bottom: 1px solid var(--border-color, #2a2a4a);
  }

  .topbar {
    display: flex;
    align-items: center;
    gap: 0.875rem;
    padding: 0.5rem 2rem;
    min-height: 56px;
  }

  .brand {
    display: inline-flex;
    align-items: center;
    flex-shrink: 0;
  }

  .brand-logo {
    display: block;
    height: 40px;
    width: auto;
    max-width: min(220px, 100%);
  }

  .breadcrumb {
    display: inline-flex;
    align-items: center;
    min-width: 0;
    gap: 0.45rem;
    color: var(--text-muted, #888);
    font-size: 0.9rem;
    white-space: nowrap;
  }

  .breadcrumb-section {
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .breadcrumb-separator {
    color: color-mix(in srgb, var(--text-muted, #888) 70%, transparent);
  }

  .breadcrumb-page {
    color: var(--text-primary, #fff);
    font-weight: 700;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .nav-section a {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    padding: 0.5rem 0.875rem;
    border-radius: 8px;
    color: var(--text-muted, #888);
    text-decoration: none;
    font-size: 0.875rem;
    transition: all 0.15s;
  }

  .nav-section a:hover,
  .nav-section a:focus-visible {
    background: var(--hover-bg, #1a1a2e);
    color: var(--text-primary, #fff);
  }

  .nav-section a.active {
    background: color-mix(in srgb, var(--primary, #6366f1) 18%, transparent);
    color: var(--text-primary, #fff);
    font-weight: 700;
  }

  .nav-actions {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    margin-left: auto;
    flex-wrap: wrap;
    justify-content: flex-end;
  }

  .menu-toggle,
  .drawer-close,
  .theme-toggle,
  .logout-btn,
  .login-btn {
    border-radius: 8px;
    transition: all 0.15s;
  }

  .menu-toggle,
  .drawer-close,
  .theme-toggle,
  .logout-btn {
    background: transparent;
    color: var(--text-muted);
    border: 1px solid var(--border-color);
    padding: 0.5rem 0.875rem;
    cursor: pointer;
  }

  .menu-toggle {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 40px;
    height: 40px;
    padding: 0;
    flex-shrink: 0;
    font-size: 1.1rem;
  }

  .menu-toggle:hover,
  .menu-toggle:focus-visible,
  .drawer-close:hover,
  .drawer-close:focus-visible,
  .theme-toggle:hover,
  .theme-toggle:focus-visible,
  .logout-btn:hover,
  .logout-btn:focus-visible {
    background: var(--hover-bg);
    color: var(--text-primary);
  }

  .auth-section {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
    justify-content: flex-end;
  }

  .auth-loading {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    color: var(--text-muted);
    font-size: 0.875rem;
  }

  .spinner {
    width: 14px;
    height: 14px;
    border: 2px solid var(--border-color);
    border-top-color: var(--primary);
    border-radius: 50%;
    animation: spin 0.8s linear infinite;
  }

  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }

  .user-info {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    flex-wrap: wrap;
    justify-content: flex-end;
  }

  .user-profile {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    background: var(--card-bg);
    padding: 0.35rem 0.7rem;
    border-radius: 999px;
    border: 1px solid var(--border-color);
    cursor: default;
    max-width: min(320px, 50vw);
  }

  .profile-avatar {
    width: 30px;
    height: 30px;
    border-radius: 50%;
    object-fit: cover;
    flex-shrink: 0;
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }

  .profile-avatar-fallback {
    background: color-mix(in srgb, var(--primary) 18%, var(--card-bg));
    color: var(--text-primary);
    font-size: 0.72rem;
    font-weight: 700;
    letter-spacing: 0.04em;
  }

  .profile-copy {
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 0.05rem;
  }

  .auth-method {
    flex-shrink: 0;
    border: 1px solid var(--border-color);
    border-radius: 999px;
    padding: 0.1rem 0.35rem;
    color: var(--text-muted);
    font-size: 0.62rem;
    font-weight: 700;
    letter-spacing: 0.04em;
  }

  .profile-name {
    font-size: 0.82rem;
    font-weight: 600;
    color: var(--text-primary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .profile-secondary {
    font-size: 0.72rem;
    color: var(--text-muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .login-btn {
    background: var(--primary);
    color: #fff;
    border: none;
    padding: 0.5rem 1rem;
    font-size: 0.875rem;
    cursor: pointer;
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }

  .login-btn:hover:not(:disabled),
  .login-btn:focus-visible:not(:disabled) {
    filter: brightness(1.08);
    transform: translateY(-1px);
  }

  .login-btn:disabled {
    background: var(--text-muted);
    cursor: not-allowed;
    opacity: 0.7;
  }

  .auth-error,
  .auth-warning {
    cursor: help;
    display: inline-flex;
    align-items: center;
  }

  .auth-warning,
  .auth-error {
    color: var(--warning);
  }

  .theme-toggle {
    padding-inline: 0.75rem;
    display: flex;
    align-items: center;
    justify-content: center;
  }

  .drawer-backdrop {
    position: fixed;
    inset: 0;
    z-index: 80;
    border: 0;
    padding: 0;
    background: rgba(0, 0, 0, 0.5);
    cursor: pointer;
    animation: backdrop-in 200ms ease;
  }

  .navigation-drawer {
    position: fixed;
    inset: 0 auto 0 0;
    z-index: 90;
    width: 320px;
    max-width: 100vw;
    overflow-y: auto;
    border-right: 1px solid var(--border-color);
    padding: 1rem 1rem 1.5rem;
    background: var(--nav-bg, #0f0f1a);
    box-shadow: 24px 0 48px rgba(0, 0, 0, 0.28);
    transform: translateX(-100%);
    transition: transform 250ms cubic-bezier(0.4, 0, 0.2, 1);
  }

  .navigation-drawer.open {
    transform: translateX(0);
  }

  @keyframes backdrop-in {
    from {
      opacity: 0;
    }
    to {
      opacity: 1;
    }
  }

  .drawer-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    margin-bottom: 1rem;
  }

  .drawer-eyebrow {
    color: var(--text-muted);
    font-size: 0.75rem;
    text-transform: uppercase;
    letter-spacing: 0.08em;
  }

  .drawer-title {
    color: var(--text-primary);
    font-size: 1rem;
    font-weight: 600;
  }

  .nav-sections {
    display: grid;
    gap: 1rem;
  }

  .nav-section {
    border-top: 1px solid var(--border-color);
    padding-top: 1rem;
  }

  .nav-section.active-section h2 {
    color: var(--primary, #6366f1);
  }

  .nav-section h2 {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 0.75rem;
    color: var(--text-muted);
    font-size: 0.75rem;
    font-weight: 800;
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }

  .nav-section ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 0.25rem;
  }

  .nav-section a {
    position: relative;
    width: 100%;
    justify-content: flex-start;
  }

  .nav-section a.active::before {
    content: '';
    position: absolute;
    left: 0;
    top: 0.45rem;
    bottom: 0.45rem;
    width: 3px;
    border-radius: 999px;
    background: var(--primary, #6366f1);
  }

  .with-badge {
    justify-content: space-between;
  }

  .badge {
    display: inline-block;
    background: var(--warning, #f59e0b);
    color: #fff;
    font-size: 0.65rem;
    font-weight: bold;
    padding: 0.125rem 0.375rem;
    border-radius: 10px;
  }

  @media (max-width: 960px) {
    .topbar {
      padding: 0.5rem 1rem;
    }

    .breadcrumb-page,
    .breadcrumb-separator {
      display: none;
    }

    .navigation-drawer {
      padding: 1rem;
    }
  }

  @media (max-width: 720px) {
    .brand-logo {
      height: 32px;
    }

    .profile-secondary,
    .auth-method {
      display: none;
    }
  }

  @media (max-width: 560px) {
    .topbar {
      gap: 0.75rem;
    }

    .nav-actions {
      gap: 0.5rem;
    }

    .menu-toggle,
    .theme-toggle,
    .logout-btn,
    .login-btn,
    .drawer-close {
      padding: 0.5rem 0.75rem;
    }

    .breadcrumb {
      display: none;
    }

    .navigation-drawer {
      width: 100vw;
    }
  }
</style>
