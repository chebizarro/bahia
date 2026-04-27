<script>
  import { page } from '$app/stores';
</script>

<nav>
  <div class="logo">
    <span class="logo-icon">⚡</span>
    <span class="logo-text">Bahia</span>
  </div>
  
  <ul class="nav-links">
    <li><a href="/" class:active={$page.url.pathname === '/'}>Dashboard</a></li>
    <li><a href="/souls" class:active={$page.url.pathname.startsWith('/souls')}>Souls</a></li>
    <li><a href="/services" class:active={$page.url.pathname.startsWith('/services')}>Services</a></li>
    <li><a href="/environments" class:active={$page.url.pathname.startsWith('/environments')}>Environments</a></li>
    <li><a href="/workers" class:active={$page.url.pathname.startsWith('/workers')}>Workers</a></li>
    <li><a href="/policies" class:active={$page.url.pathname.startsWith('/policies')}>Policies</a></li>
    <li><a href="/events" class:active={$page.url.pathname === '/events'}>Events</a></li>
  </ul>
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
</style>
