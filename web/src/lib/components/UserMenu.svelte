<script>
  import { tick } from 'svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/stores';
  import { authState, isAuthenticated, login, logout } from '$lib/stores/auth.js';
  import { LoginIcon, ProfileIcon, RelayIcon, WarningIcon } from '$lib/icons/domain-icons.js';
  import { authPresentation, truncatePubkey } from '$lib/components/nav-model.js';
  import { anonymousMenuItems, authenticatedMenuItems, menuKeyHandler } from '$lib/components/user-menu-model.js';

  let open = $state(false);
  let activeIndex = $state(-1);
  let triggerEl = $state();
  let menuEl = $state();
  let rootEl = $state();
  let ignoreNextWindowClick = $state(false);

  let authUi = $derived.by(() => {
    const authenticated = isAuthenticated();
    const base = authPresentation(authState, authenticated);
    if (!authenticated) return base;

    const profile = authState.profile || null;
    return {
      ...base,
      profile,
      displayLabel: profile?.displayName || profile?.name || truncatePubkey(authState.pubkey || ''),
      nip05: profile?.nip05 || '',
      avatarUrl: profile?.picture || ''
    };
  });

  let menuItems = $derived.by(() => {
    if (authUi.mode === 'authenticated') return authenticatedMenuItems;
    return anonymousMenuItems.map((item) => ({
      ...item,
      disabled: item.id === 'nip07' && !authUi.extensionAvailable,
      description: item.id === 'nip07' && !authUi.extensionAvailable
        ? 'Use NIP-07 signer (not detected)'
        : item.description
    }));
  });

  let authMethodLabel = $derived(authState.authMethod === 'nip46' ? 'NIP-46 Remote Signer' : 'NIP-07 Extension');
  let triggerLabel = $derived(authUi.mode === 'authenticated' ? authUi.displayLabel : 'Sign In');

  $effect(() => {
    $page.url.pathname;
    $page.url.hash;
    closeMenu(false);
  });

  $effect(() => {
    if (!open || activeIndex < 0) return;

    queueMicrotask(async () => {
      await tick();
      const items = menuEl?.querySelectorAll('[role="menuitem"]') || [];
      items[activeIndex]?.focus();
    });
  });

  function profileInitials(label) {
    const text = String(label || '').trim();
    if (!text) return 'N';
    const parts = text.split(/\s+/).filter(Boolean).slice(0, 2);
    const initials = parts.map((part) => part[0]?.toUpperCase() || '').join('');
    return initials || text.slice(0, 1).toUpperCase();
  }

  function canOpenMenu() {
    // Can always open when authenticated or anonymous with any available signer
    if (authUi.mode === 'loading') return false;
    if (authUi.mode === 'authenticated') return true;
    // Anonymous: allow if extension available or NIP-46 available
    return authUi.extensionAvailable || menuItems.some((item) => !item.disabled);
  }

  function openMenu(index = -1) {
    if (!canOpenMenu()) return;
    open = true;
    activeIndex = index;
  }

  function closeMenu(returnFocus = false) {
    if (!open) return;
    open = false;
    activeIndex = -1;
    if (returnFocus) {
      queueMicrotask(() => triggerEl?.focus());
    }
  }

  function toggleMenu(event) {
    event?.stopPropagation();
    if (open) {
      closeMenu(false);
    } else {
      openMenu(-1);
    }
  }

  function handleTriggerKeydown(event) {
    if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault();
      openMenu(firstEnabledIndex());
    } else if (event.key === 'ArrowDown') {
      event.preventDefault();
      openMenu(firstEnabledIndex());
    } else if (event.key === 'ArrowUp') {
      event.preventDefault();
      openMenu(lastEnabledIndex());
    }
  }

  function firstEnabledIndex() {
    return menuItems.findIndex((item) => !item.disabled);
  }

  function lastEnabledIndex() {
    for (let index = menuItems.length - 1; index >= 0; index -= 1) {
      if (!menuItems[index].disabled) return index;
    }
    return -1;
  }

  function handleMenuKeydown(event) {
    if ((event.key === 'Enter' || event.key === ' ') && activeIndex >= 0) {
      event.preventDefault();
      void activateItem(menuItems[activeIndex]);
      return;
    }

    activeIndex = menuKeyHandler(event, {
      items: menuItems,
      activeIndex,
      close: () => closeMenu(true)
    });
  }

  async function activateItem(item) {
    if (!item || item.disabled) return;

    try {
      if (item.action === 'login-nip07') {
        closeMenu(false);
        await login();
      } else if (item.action === 'settings') {
        closeMenu(false);
        await goto(item.href);
      } else if (item.action === 'logout') {
        closeMenu(false);
        logout();
      }
    } catch (error) {
      console.error('User menu action failed:', error);
    }
  }

  function handleWindowClick(event) {
    if (ignoreNextWindowClick) {
      ignoreNextWindowClick = false;
      return;
    }
    if (!open || rootEl?.contains(event.target)) return;
    closeMenu(false);
  }

  function handleWindowKeydown(event) {
    if (event.key === 'Escape') {
      closeMenu(true);
    }
  }
</script>

<svelte:window onclick={handleWindowClick} onkeydown={handleWindowKeydown} />

<!-- DEBUG: mode={authUi.mode} open={open} -->
<div class="user-menu" bind:this={rootEl} data-mode={authUi.mode} data-open={open}>
  {#if authUi.mode === 'loading'}
    <button type="button" class="user-menu-trigger user-menu-loading" disabled aria-live="polite">
      <span class="spinner"></span>
      {authUi.label}
    </button>
  {:else if authUi.mode === 'authenticated'}
    <button
      type="button"
      class="user-menu-trigger user-menu-profile-trigger"
      aria-haspopup="menu"
      aria-expanded={open}
      aria-controls="user-menu"
      title="{authUi.pubkey}\n{authUi.nip05 ? authUi.nip05 : ''}"
      bind:this={triggerEl}
      onclick={(e) => { e.stopPropagation(); ignoreNextWindowClick = true; open = !open; alert('clicked! open=' + open); }}
      onkeydown={handleTriggerKeydown}
    >
      {#if authUi.avatarUrl}
        <img
          class="profile-avatar"
          src={authUi.avatarUrl}
          alt={authUi.displayLabel}
          onerror={(event) => (event.currentTarget.style.display = 'none')}
        />
      {:else}
        <span class="profile-avatar profile-avatar-fallback">{profileInitials(authUi.displayLabel)}</span>
      {/if}
      <span class="profile-copy">
        <span class="profile-name">{authUi.displayLabel}</span>
        <span class="profile-secondary">{authUi.nip05 || authUi.truncatedPubkey}</span>
      </span>
      <span class="chevron" aria-hidden="true">▾</span>
    </button>
  {:else}
    <button
      type="button"
      class="user-menu-trigger user-menu-signin"
      aria-haspopup="menu"
      aria-expanded={open}
      aria-controls="user-menu"
      title={authUi.extensionAvailable ? 'Sign in with Nostr' : 'NIP-07 not detected; Nostr Connect is available in settings'}
      bind:this={triggerEl}
      onclick={(e) => { e.stopPropagation(); ignoreNextWindowClick = true; open = !open; }}
      onkeydown={handleTriggerKeydown}
    >
      {#if authUi.extensionAvailable}
        <LoginIcon size={16} strokeWidth={1.75} ariaHidden="true" />
        Sign In
      {:else}
        <WarningIcon size={16} strokeWidth={1.75} ariaHidden="true" />
        No Signer
      {/if}
      <span class="chevron" aria-hidden="true">▾</span>
    </button>
  {/if}

  <!-- DEBUG: open state = {open} -->
  {#if open}
    <button type="button" class="user-menu-backdrop" aria-label="Close user menu" onclick={() => closeMenu(false)}></button>
    <div
      id="user-menu"
      class="user-menu-dropdown"
      role="menu"
      tabindex="-1"
      aria-label="User menu"
      bind:this={menuEl}
      onkeydown={handleMenuKeydown}
    >
      {#if authUi.mode === 'authenticated'}
        <div class="user-menu-profile" aria-label="Signed in profile">
          {#if authUi.avatarUrl}
            <img class="profile-avatar profile-avatar-large" src={authUi.avatarUrl} alt={authUi.displayLabel} />
          {:else}
            <span class="profile-avatar profile-avatar-large profile-avatar-fallback">{profileInitials(authUi.displayLabel)}</span>
          {/if}
          <span class="profile-copy">
            <span class="profile-name">{authUi.displayLabel}</span>
            <span class="profile-secondary">{authUi.truncatedPubkey}</span>
            {#if authUi.nip05}
              <span class="profile-secondary">{authUi.nip05}</span>
            {/if}
          </span>
        </div>
        {#if authUi.showWarning}
          <div class="user-menu-warning" role="status">
            <WarningIcon size={16} strokeWidth={1.75} ariaHidden="true" />
            {authUi.warning || 'Backend auth unavailable'}
          </div>
        {/if}
      {:else}
        <div class="user-menu-header">Sign in with Nostr</div>
        {#if authUi.showError}
          <div class="user-menu-warning" role="alert">
            <WarningIcon size={16} strokeWidth={1.75} ariaHidden="true" />
            Login failed: {authUi.error}
          </div>
        {/if}
      {/if}

      <div class="user-menu-items">
        {#each menuItems as item, index}
          {#if authUi.mode === 'authenticated' && item.id === 'logout'}
            <div class="user-menu-separator" role="separator"></div>
            <div class="auth-method-row" aria-label="Authentication method">
              <span aria-hidden="true">🔐</span>
              <span>{authMethodLabel}</span>
            </div>
          {/if}

          <button
            type="button"
            class="user-menu-item"
            role="menuitem"
            tabindex={activeIndex === index ? 0 : -1}
            aria-disabled={item.disabled ? 'true' : undefined}
            disabled={item.disabled}
            onclick={() => activateItem(item)}
          >
            {#if item.id === 'nip07'}
              <span aria-hidden="true">🔌</span>
            {:else if item.id === 'nip46'}
              <span aria-hidden="true">🔗</span>
            {:else if item.id === 'profile'}
              <ProfileIcon size={17} strokeWidth={1.75} ariaHidden="true" />
            {:else if item.id === 'relays'}
              <RelayIcon size={17} strokeWidth={1.75} ariaHidden="true" />
            {:else}
              <span aria-hidden="true">🚪</span>
            {/if}
            <span class="item-copy">
              <span class="item-label">{item.label}</span>
              {#if item.description}
                <span class="item-description">{item.description}</span>
              {/if}
            </span>
          </button>
        {/each}
      </div>
    </div>
  {/if}
</div>

<style>
  .user-menu {
    position: relative;
    display: inline-flex;
    align-items: center;
  }

  .user-menu-trigger {
    border-radius: 999px;
    transition: all 0.15s;
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    border: 1px solid var(--border-color);
    cursor: pointer;
    font-size: 0.875rem;
  }

  .user-menu-signin {
    background: var(--primary);
    color: #fff;
    border: none;
    padding: 0.5rem 1rem;
  }

  .user-menu-signin:hover:not(:disabled),
  .user-menu-signin:focus-visible:not(:disabled) {
    filter: brightness(1.08);
    transform: translateY(-1px);
  }

  .user-menu-profile-trigger,
  .user-menu-loading {
    background: var(--card-bg);
    color: var(--text-primary);
    padding: 0.35rem 0.7rem;
    max-width: min(320px, 50vw);
  }

  .user-menu-profile-trigger:hover,
  .user-menu-profile-trigger:focus-visible {
    background: var(--hover-bg);
    outline: none;
  }

  .user-menu-loading {
    color: var(--text-muted);
    cursor: wait;
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

  .profile-avatar-large {
    width: 40px;
    height: 40px;
  }

  .profile-avatar-fallback {
    background: color-mix(in srgb, var(--primary) 18%, var(--card-bg));
    color: var(--text-primary);
    font-size: 0.72rem;
    font-weight: 700;
    letter-spacing: 0.04em;
  }

  .profile-copy,
  .item-copy {
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 0.05rem;
  }

  .profile-name,
  .item-label {
    font-size: 0.82rem;
    font-weight: 600;
    color: var(--text-primary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .profile-secondary,
  .item-description {
    font-size: 0.72rem;
    color: var(--text-muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .chevron {
    color: currentColor;
    opacity: 0.8;
    font-size: 0.8rem;
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

  .user-menu-backdrop {
    display: none;
  }

  .user-menu-dropdown {
    position: absolute;
    top: calc(100% + 8px);
    right: 0;
    min-width: 280px;
    max-width: 360px;
    background: var(--card-bg);
    border: 1px solid var(--border-color);
    border-radius: 12px;
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.25);
    z-index: 100;
    overflow: hidden;
    animation: menu-enter 0.15s ease-out;
  }

  .user-menu-header {
    padding: 0.875rem 1rem;
    color: var(--text-primary);
    font-size: 0.875rem;
    font-weight: 700;
    border-bottom: 1px solid var(--border-color);
  }

  .user-menu-profile {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 0.875rem 1rem;
    border-bottom: 1px solid var(--border-color);
  }

  .user-menu-warning {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin: 0.75rem 1rem 0;
    padding: 0.55rem 0.7rem;
    border-radius: 8px;
    background: color-mix(in srgb, var(--warning) 16%, transparent);
    color: var(--warning);
    font-size: 0.78rem;
  }

  .user-menu-items {
    padding: 0.25rem 0;
  }

  .user-menu-item {
    width: 100%;
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 0.625rem 1rem;
    color: var(--text-primary);
    background: transparent;
    border: 0;
    text-align: left;
    font-size: 0.875rem;
    cursor: pointer;
    transition: background 0.1s;
  }

  .user-menu-item:hover:not(:disabled),
  .user-menu-item:focus-visible {
    background: var(--hover-bg);
    outline: none;
  }

  .user-menu-item[aria-disabled='true'] {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .user-menu-separator {
    height: 1px;
    background: var(--border-color);
    margin: 0.25rem 0;
  }

  .auth-method-row {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 0.625rem 1rem;
    color: var(--text-muted);
    font-size: 0.78rem;
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

  @media (max-width: 720px) {
    .profile-secondary {
      display: none;
    }
  }

  @media (max-width: 560px) {
    .user-menu-backdrop {
      display: block;
      position: fixed;
      inset: 0;
      background: rgba(0, 0, 0, 0.4);
      border: 0;
      z-index: 99;
      padding: 0;
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

    .user-menu-item {
      min-height: 48px;
      padding: 0.75rem 1.25rem;
    }
  }

  @keyframes menu-slide-up {
    from {
      transform: translateY(100%);
    }
    to {
      transform: translateY(0);
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .user-menu-dropdown,
    .spinner {
      animation: none;
    }
  }
</style>
