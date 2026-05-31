export function stableJsonValue(value) {
  if (Array.isArray(value)) return value.map(stableJsonValue);
  if (value && typeof value === 'object') {
    return Object.fromEntries(
      Object.keys(value)
        .sort()
        .map((key) => [key, stableJsonValue(value[key])])
    );
  }
  return value;
}

export function parseJsonContent(event, fallback = {}) {
  if (!event || !event.content) return fallback;
  try {
    return JSON.parse(event.content);
  } catch {
    return fallback;
  }
}
