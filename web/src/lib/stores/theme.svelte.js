import { browser } from '$app/environment';

const THEME_KEY = 'bahia_theme';

// Get initial theme from localStorage or system preference
function getInitialTheme() {
  if (!browser) return 'dark';

  const stored = localStorage.getItem(THEME_KEY);
  if (stored) return stored;

  // Check system preference
  if (window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches) {
    return 'light';
  }

  return 'dark';
}

// Create theme state
export const theme = $state({ value: getInitialTheme() });

// Keep localStorage + document in sync
if (browser) {
  $effect.root(() => {
    $effect(() => {
      localStorage.setItem(THEME_KEY, theme.value);
      document.documentElement.setAttribute('data-theme', theme.value);
    });
  });
}

// Toggle function
export function toggleTheme() {
  theme.value = theme.value === 'dark' ? 'light' : 'dark';
}
