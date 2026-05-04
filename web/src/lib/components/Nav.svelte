<script>
  import { page } from '$app/stores';
  import { theme, toggleTheme } from '$lib/stores/theme.js';
  import { authState, isAuthenticated, login, logout } from '$lib/stores/auth.js';
  import { NAV_LINKS, authPresentation, isActiveNavLink } from '$lib/components/nav-model.js';

  let authUi = $derived(authPresentation(authState, isAuthenticated()));

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
</script>

<nav>
  <div class="logo">
    <span class="logo-icon">⚡</span>
    <span class="logo-text">Bahia</span>
  </div>
  
  <ul class="nav-links">
    {#each NAV_LINKS as link}
      <li>
        <a href={link.href} class:active={isActiveNavLink($page.url.pathname, link.href)} class:with-badge={link.badge}>
          {link.label}
          {#if link.badge}
            <span class="badge">{link.badge}</span>
          {/if}
        </a>
      </li>
    {/each}
  </ul>
  
  <div class="nav-actions">
    <div class="auth-section">
      {#if authUi.mode === 'loading'}
        <span class="auth-loading">
          <span class="spinner"></span>
          {authUi.label}
        </span>
      {:else if authUi.mode === 'authenticated'}
        <div class="user-info">
          <span class="user-pubkey" title={authUi.pubkey}>
            {#if authUi.backendAuthenticated}
              ✅
            {:else}
              🔑
            {/if}
            {authUi.truncatedPubkey}
          </span>
          {#if authUi.showWarning}
            <span class="auth-warning" title={authUi.warning}>⚠️</span>
          {/if}
          <button class="logout-btn" onclick={handleLogout}>
            Logout
          </button>
        </div>
      {:else}
        <button
          class="login-btn"
          onclick={handleLogin}
          disabled={!authUi.extensionAvailable}
          title={authUi.buttonTitle}
        >
          {authUi.buttonLabel}
        </button>
        {#if authUi.showError}
          <span class="auth-error" title={authUi.error}>⚠️</span>
        {/if}
      {/if}
    </div>

    <button class="theme-toggle" onclick={toggleTheme} aria-label="Toggle theme">
      {#if theme.value === 'dark'}
        ☀️
      {:else}
        🌙
      {/if}
    </button>
  </div>
</nav>

<style>
  nav {
    display: flex;
    align-items: center;
    gap: 2rem;
    padding: 1rem 2rem;
    background: var(--nav-bg, #0f0f1a);
    border-bottom: 1px solid var(--border-color, #2a2a4a);
  }
  .logo {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    font-weight: bold;
    font-size: 1.25rem;
  }
  .logo-icon { font-size: 1.5rem; }
  .nav-links {
    display: flex;
    gap: 0.5rem;
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .nav-links a {
    padding: 0.5rem 1rem;
    border-radius: 6px;
    color: var(--text-muted, #888);
    text-decoration: none;
    font-size: 0.875rem;
    transition: all 0.15s;
  }
  .nav-links a:hover {
    background: var(--hover-bg, #1a1a2e);
    color: var(--text-primary, #fff);
  }
  .nav-links a.active {
    background: var(--primary, #6366f1);
    color: #fff;
  }
  
  .nav-actions {
    display: flex;
    align-items: center;
    gap: 1rem;
    margin-left: auto;
  }
  
  .auth-section {
    display: flex;
    align-items: center;
    gap: 0.5rem;
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
    to { transform: rotate(360deg); }
  }
  
  .user-info {
    display: flex;
    align-items: center;
    gap: 0.75rem;
  }
  
  .user-pubkey {
    font-family: monospace;
    font-size: 0.8rem;
    color: var(--text-muted);
    background: var(--card-bg);
    padding: 0.25rem 0.5rem;
    border-radius: 4px;
    border: 1px solid var(--border-color);
  }
  
  .login-btn {
    background: var(--primary);
    color: #fff;
    border: none;
    padding: 0.5rem 1rem;
    border-radius: 6px;
    font-size: 0.875rem;
    cursor: pointer;
    transition: all 0.15s;
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  
  .login-btn:hover:not(:disabled) {
    filter: brightness(1.1);
    transform: translateY(-1px);
  }
  
  .login-btn:disabled {
    background: var(--text-muted);
    cursor: not-allowed;
    opacity: 0.7;
  }
  
  .logout-btn {
    background: transparent;
    color: var(--text-muted);
    border: 1px solid var(--border-color);
    padding: 0.375rem 0.75rem;
    border-radius: 6px;
    font-size: 0.8rem;
    cursor: pointer;
    transition: all 0.15s;
  }
  
  .logout-btn:hover {
    background: var(--error);
    color: #fff;
    border-color: var(--error);
  }
  
  .auth-error {
    cursor: help;
  }
  
  .auth-warning {
    cursor: help;
    color: var(--warning);
  }
  
  .theme-toggle {
    background: transparent;
    border: 1px solid var(--border-color);
    border-radius: 6px;
    padding: 0.5rem 0.75rem;
    font-size: 1.25rem;
    cursor: pointer;
    transition: all 0.15s;
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .theme-toggle:hover {
    background: var(--hover-bg);
    transform: scale(1.05);
  }
  .with-badge {
    position: relative;
  }
  .badge {
    display: inline-block;
    background: var(--warning, #f59e0b);
    color: #fff;
    font-size: 0.65rem;
    font-weight: bold;
    padding: 0.125rem 0.375rem;
    border-radius: 10px;
    margin-left: 0.25rem;
    vertical-align: middle;
  }
</style>
