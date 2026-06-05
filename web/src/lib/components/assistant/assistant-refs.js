export function assistantRefValue(ref) {
  if (typeof ref === 'string') return ref.trim();
  if (!ref || typeof ref !== 'object') return '';
  return String(ref.ref || ref.value || '').trim();
}

export function assistantRefLabel(ref, value = assistantRefValue(ref)) {
  if (ref && typeof ref === 'object' && ref.label) return String(ref.label);
  if (value.startsWith('docs:')) return `Documentation: ${value.slice('docs:'.length)}`;
  return value;
}

export function safeAssistantRefHref(href) {
  const value = String(href || '').trim();
  if (!value) return '';
  if (value === '/docs' || value.startsWith('/docs/')) return value;
  if (/^https?:\/\//i.test(value)) return value;
  return '';
}

export function assistantRefHref(ref) {
  if (ref && typeof ref === 'object' && ref.href) return safeAssistantRefHref(ref.href);
  const value = assistantRefValue(ref);
  if (value.startsWith('docs:')) return `/docs/${encodeURIComponent(value.slice('docs:'.length))}`;
  return '';
}

export function normalizeAssistantRef(ref, options = {}) {
  const value = assistantRefValue(ref);
  if (!value) return null;
  const defaultRef = Boolean(options.defaultRef);
  return {
    ref: value,
    label: assistantRefLabel(ref, value),
    href: assistantRefHref(ref),
    type: value.startsWith('docs:') ? 'docs' : 'operational',
    source: ref && typeof ref === 'object' && ref.source ? String(ref.source) : (defaultRef ? 'default' : 'selected'),
    dismissible: defaultRef && value.startsWith('docs:')
  };
}

export function mergeAssistantRefs({ selectedRefs = [], defaultSelectedRefs = [], dismissedRefs = [] } = {}) {
  const dismissed = new Set((Array.isArray(dismissedRefs) ? dismissedRefs : []).map(String));
  const refs = [];
  const seen = new Set();

  for (const candidate of Array.isArray(selectedRefs) ? selectedRefs : []) {
    const ref = normalizeAssistantRef(candidate, { defaultRef: false });
    if (!ref || dismissed.has(ref.ref) || seen.has(ref.ref)) continue;
    refs.push(ref);
    seen.add(ref.ref);
  }

  for (const candidate of Array.isArray(defaultSelectedRefs) ? defaultSelectedRefs : []) {
    const ref = normalizeAssistantRef(candidate, { defaultRef: true });
    if (!ref || dismissed.has(ref.ref) || seen.has(ref.ref)) continue;
    refs.push(ref);
    seen.add(ref.ref);
  }

  return refs;
}
