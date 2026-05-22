<script>
  import { tick } from 'svelte';
  import { page } from '$app/stores';
  import { theme, toggleTheme } from '$lib/stores/theme.js';
  import { authState, isAuthenticated, login, logout } from '$lib/stores/auth.js';
  import { LoginIcon, MoonIcon, SunIcon, WarningIcon } from '$lib/icons/domain-icons.js';
  import {
    NAV_SECTIONS,
    PRIMARY_NAV_LINKS,
    authPresentation,
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
  let menuOpen = $state(false);
  let menuButton = $state();
  let drawerCloseButton = $state();
  let previousMenuOpen = false;

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
    <a class="brand" href="/" aria-label="Bahia home">
      <img
        class="brand-logo"
        src={theme.value === 'dark' ? '/branding/logo_wide_dm.png' : '/branding/logo_wide_lm.png'}
        alt="Bahia"
      />
    </a>

    <ul class="primary-links" aria-label="Primary shortcuts">
      {#each PRIMARY_NAV_LINKS as link}
        <li>
          <a
            href={link.href}
            class:active={isActiveNavLink($page.url.pathname, link.href)}
            aria-current={isActiveNavLink($page.url.pathname, link.href) ? 'page' : undefined}
          >
            {link.label}
          </a>
        </li>
      {/each}
    </ul>

    <div class="nav-actions">
      <button
        type="button"
        class="menu-toggle"
        aria-controls="navigation-drawer"
        aria-expanded={menuOpen}
        bind:this={menuButton}
        onclick={toggleMenu}
      >
        {menuOpen ? 'Close' : 'Menu'}
      </button>

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
    <nav id="navigation-drawer" class="navigation-drawer" aria-label="All navigation links">
      <div class="drawer-header">
        <div>
          <p class="drawer-eyebrow">Browse</p>
          <p class="drawer-title">All destinations</p>
        </div>
        <button type="button" class="drawer-close" bind:this={drawerCloseButton} onclick={closeMenu}>Close</button>
      </div>

      <div class="nav-sections">
        {#each NAV_SECTIONS as section}
          <section class:active-section={isActiveNavSection($page.url.pathname, section)} class="nav-section">
            <h2>{section.title}</h2>
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
    gap: 1rem;
    padding: 1rem 2rem;
  }

  .brand {
    display: inline-flex;
    align-items: center;
    flex-shrink: 0;
  }

  .brand-logo {
    display: block;
    height: 63px;
    width: auto;
    max-width: min(360px, 100%);
  }

  .primary-links {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    list-style: none;
    margin: 0;
    padding: 0;
  }

  .primary-links a,
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

  .primary-links a:hover,
  .primary-links a:focus-visible,
  .nav-section a:hover,
  .nav-section a:focus-visible {
    background: var(--hover-bg, #1a1a2e);
    color: var(--text-primary, #fff);
  }

  .primary-links a.active,
  .nav-section a.active {
    background: color-mix(in srgb, var(--primary, #6366f1) 18%, transparent);
    color: var(--text-primary, #fff);
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

  .navigation-drawer {
    border-top: 1px solid var(--border-color);
    padding: 1rem 2rem 1.5rem;
    background: var(--nav-bg, #0f0f1a);
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
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    gap: 1rem;
  }

  .nav-section {
    background: var(--card-bg);
    border: 1px solid var(--border-color);
    border-radius: 12px;
    padding: 1rem;
  }

  .nav-section.active-section {
    border-color: color-mix(in srgb, var(--primary, #6366f1) 50%, var(--border-color));
  }

  .nav-section h2 {
    margin-bottom: 0.75rem;
    color: var(--text-primary);
    font-size: 0.95rem;
  }

  .nav-section ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 0.375rem;
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
      padding: 1rem;
    }

    .primary-links {
      display: none;
    }

    .navigation-drawer {
      padding: 1rem;
    }
  }

  @media (max-width: 720px) {
    .brand-logo {
      height: 51px;
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

    .nav-sections {
      grid-template-columns: 1fr;
    }
  }
</style>
