let nextId = 1;

export const toasts = $state([]);

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

  toasts.unshift(newToast);

  // Auto-remove after timeout if timeout > 0
  if (newToast.timeout > 0) {
    setTimeout(() => {
      removeToast(id);
    }, newToast.timeout);
  }

  return id;
}

export function removeToast(id) {
  const idx = toasts.findIndex((toast) => toast.id === id);
  if (idx >= 0) {
    toasts.splice(idx, 1);
  }
}

export function clearToasts() {
  toasts.length = 0;
}

// Convenience methods
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
