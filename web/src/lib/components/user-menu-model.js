export const anonymousMenuItems = [
  {
    id: 'nip07',
    label: 'Browser Extension',
    description: 'Use NIP-07 signer',
    action: 'login-nip07'
  },
  {
    id: 'nip46',
    label: 'Nostr Connect',
    description: 'Use NIP-46 remote signer',
    action: 'settings',
    href: '/settings#nostr-connect'
  }
];

export const authenticatedMenuItems = [
  {
    id: 'profile',
    label: 'Edit Profile',
    action: 'settings',
    href: '/settings/profile'
  },
  {
    id: 'relays',
    label: 'Manage Relays',
    action: 'settings',
    href: '/settings/relays'
  },
  {
    id: 'logout',
    label: 'Log out',
    action: 'logout'
  }
];

function isFocusable(item) {
  return item && !item.disabled;
}

function findEnabledIndex(items, startIndex, step) {
  if (!items.length) return -1;

  for (let offset = 0; offset < items.length; offset += 1) {
    const index = (startIndex + offset * step + items.length) % items.length;
    if (isFocusable(items[index])) return index;
  }

  return -1;
}

export function menuKeyHandler(event, { items = [], activeIndex = -1, close = () => {} } = {}) {
  switch (event.key) {
    case 'ArrowDown':
      event.preventDefault();
      return findEnabledIndex(items, activeIndex < 0 ? 0 : activeIndex + 1, 1);
    case 'ArrowUp':
      event.preventDefault();
      return findEnabledIndex(items, activeIndex < 0 ? items.length - 1 : activeIndex - 1, -1);
    case 'Home':
      event.preventDefault();
      return findEnabledIndex(items, 0, 1);
    case 'End':
      event.preventDefault();
      return findEnabledIndex(items, items.length - 1, -1);
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
