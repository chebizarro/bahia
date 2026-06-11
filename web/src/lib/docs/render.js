import DOMPurify from 'isomorphic-dompurify';
import { Marked, Renderer } from 'marked';

function escapeAttribute(value = '') {
  return String(value)
    .replaceAll('&', '&amp;')
    .replaceAll('"', '&quot;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;');
}

function linkMapFromResolutions(links = []) {
  const map = new Map();
  for (const link of Array.isArray(links) ? links : []) {
    if (!link?.original) continue;
    map.set(link.original, link);
  }
  return map;
}

function internalMarkdownHref(href = '') {
  const value = String(href || '').trim();
  if (!value) return false;
  if (value.startsWith('#')) return true;
  if (value.startsWith('/') || value.startsWith('//')) return false;
  if (/^[a-z][a-z0-9+.-]*:/i.test(value)) return false;
  const pathOnly = value.split(/[?#]/, 1)[0];
  return pathOnly.toLowerCase().endsWith('.md');
}

function unresolvedLink(label, reason) {
  return `<a href="#" class="docs-link-unresolved" aria-disabled="true" title="${escapeAttribute(reason)}">${label}</a>`;
}

export function renderDocumentationMarkdown(markdown = '', links = []) {
  const renderer = new Renderer();
  const linkMap = linkMapFromResolutions(links);

  renderer.link = function renderDocsLink({ href, title, tokens }) {
    const label = this.parser.parseInline(tokens);
    const resolution = linkMap.get(href);
    if (resolution?.status === 'resolved' && resolution.href) {
      const safeHref = escapeAttribute(resolution.href);
      const safeTitle = title ? ` title="${escapeAttribute(title)}"` : '';
      if (resolution.external) {
        return `<a href="${safeHref}"${safeTitle} target="_blank" rel="noreferrer noopener">${label}</a>`;
      }
      return `<a href="${safeHref}"${safeTitle} data-doc-topic="${escapeAttribute(resolution.topic || '')}">${label}</a>`;
    }

    if (resolution && resolution.status !== 'resolved') {
      const reason = resolution.error || `Documentation link is ${resolution.status}`;
      return unresolvedLink(label, reason);
    }

    if (!resolution && internalMarkdownHref(href)) {
      return unresolvedLink(label, 'Documentation topic not found in catalog');
    }

    return Renderer.prototype.link.call(this, { href, title, tokens });
  };

  const marked = new Marked({ renderer, gfm: true, breaks: false });
  const html = marked.parse(String(markdown || ''));
  return DOMPurify.sanitize(html, {
    ADD_ATTR: ['target', 'rel', 'data-doc-topic', 'aria-disabled'],
    ADD_TAGS: []
  });
}

export function topicFromHref(href = '') {
  const match = String(href).match(/^\/docs\/([^#?]+)/);
  return match ? decodeURIComponent(match[1]) : '';
}
