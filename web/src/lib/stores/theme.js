import { writable } from 'svelte/store';
import { browser } from '$app/environment';

// Get initial theme from localStorage or system preference
function getInitialTheme() {
  if (!browser) return 'dark';
  
  const stored = localStorage.getItem('bahia_theme');
  if (stored) return stored;
  
  // Check system preference
  if (window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches) {
    return 'light';
  }
  
  return 'dark';
}

// Create theme store
export const theme = writable(getInitialTheme());

// Subscribe to theme changes and update localStorage + document
if (browser) {
  theme.subscribe(value => {
    localStorage.setItem('bahia_theme', value);
    document.documentElement.setAttribute('data-theme', value);
  });
}

// Toggle function
export function toggleTheme() {
  theme.update(current => current === 'dark' ? 'light' : 'dark');
}
