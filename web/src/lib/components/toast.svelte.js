import { writable } from 'svelte/store';

let nextId = 1;

export const toasts = writable([]);

export function addToast(toast) {
  const id = nextId++;
  const newToast = {
    id,
    type: toast.type || 'info',
    title: toast.title,
    message: toast.message,
    timeout: toast.timeout !== undefined ? toast.timeout : 5000,
    createdAt: Date.now()
  };

  toasts.update((items) => [newToast, ...items]);

  if (newToast.timeout > 0) {
    setTimeout(() => {
      removeToast(id);
    }, newToast.timeout);
  }

  return id;
}

export function removeToast(id) {
  toasts.update((items) => items.filter((toast) => toast.id !== id));
}

export function clearToasts() {
  toasts.set([]);
}

export const toast = {
  success: (message, options = {}) => {
    return addToast({ ...options, type: 'success', message });
  },
  error: (message, options = {}) => {
    return addToast({ ...options, type: 'error', message, timeout: options.timeout || 0 });
  },
  warning: (message, options = {}) => {
    return addToast({ ...options, type: 'warning', message });
  },
  info: (message, options = {}) => {
    return addToast({ ...options, type: 'info', message });
  }
};
