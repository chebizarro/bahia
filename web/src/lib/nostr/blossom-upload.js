import { signWithAuth } from '$lib/stores/auth.js';
import { currentSystemInfo } from '$lib/stores/system.svelte.js';

const sha256Pattern = /^[0-9a-f]{64}$/i;

export function blossomHashFromUrl(uri) {
  let url;
  try { url = new URL(uri); } catch { throw new Error('Enter a valid Blossom HTTP(S) URL'); }
  if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.search || url.hash) {
    throw new Error('Enter a valid Blossom HTTP(S) URL without credentials, query or fragment');
  }
  const segment = url.pathname.split('/').pop()?.split('.')[0] || '';
  if (!sha256Pattern.test(segment)) throw new Error('Blossom URL must end in a 64-character SHA-256 hash');
  return segment.toLowerCase();
}

export function blossomLocation(uri, sha256 = '', mediaType = '') {
  const hash = blossomHashFromUrl(uri);
  const expected = String(sha256 || '').trim().replace(/^sha256:/i, '').toLowerCase();
  if (expected && (!sha256Pattern.test(expected) || expected !== hash)) {
    throw new Error('SHA-256 must match the hash in the Blossom URL');
  }
  return { type: 'blossom', uri: String(uri).trim(), ...(mediaType ? { mediaType } : {}) };
}

export async function uploadSBOMToBlossom(file, {
  servers = currentSystemInfo()?.blossom?.servers,
  sign = signWithAuth,
  fetchImpl = globalThis.fetch
} = {}) {
  if (!Array.isArray(servers) || servers.length === 0) throw new Error('No Blossom server is configured in Bahia discovery');
  const bytes = await file.arrayBuffer();
  const hash = Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256', bytes)), value => value.toString(16).padStart(2, '0')).join('');
  const size = bytes.byteLength;
  let lastError;
  for (const server of servers) {
    try {
      const base = new URL(server);
      if (!['http:', 'https:'].includes(base.protocol) || (globalThis.location?.protocol === 'https:' && base.protocol !== 'https:')) {
        throw new Error('Blossom server must use HTTPS from this page');
      }
      const uploadUrl = new URL('upload', `${base.href.replace(/\/$/, '')}/`).href;
      const signed = await sign({ kind: 24242, created_at: Math.floor(Date.now() / 1000),
        tags: [['t', 'upload'], ['expiration', String(Math.floor(Date.now() / 1000) + 60)], ['x', hash]], content: 'Upload Blob' });
      const auth = btoa(String.fromCharCode(...new TextEncoder().encode(JSON.stringify(signed)))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
      const response = await fetchImpl(uploadUrl, { method: 'PUT', headers: {
        Authorization: `Nostr ${auth}`, 'Content-Type': file.type || 'application/json', 'X-SHA-256': hash
      }, body: bytes });
      if (!response.ok) throw new Error(`Blossom upload failed (HTTP ${response.status})`);
      const descriptor = await response.json();
      if (String(descriptor?.sha256 || '').toLowerCase() !== hash || Number(descriptor?.size) !== size || blossomHashFromUrl(descriptor?.url) !== hash) {
        throw new Error('Blossom upload descriptor does not match the SBOM bytes');
      }
      return blossomLocation(descriptor.url, hash, file.type || 'application/json');
    } catch (error) {
      lastError = error;
    }
  }
  throw lastError || new Error('Blossom upload failed');
}
