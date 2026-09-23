import { describe, expect, it } from 'vitest';
import { renderDocumentationMarkdown } from '../../src/lib/docs/render.js';

function render(markdown, links = []) {
  const template = document.createElement('template');
  template.innerHTML = renderDocumentationMarkdown(markdown, links);
  return template.content;
}

describe('documentation sanitizer contract', () => {
  it('preserves headings, GFM tables, code and safe images', () => {
    const doc = render('# Guide\n\n| Name | Value |\n| --- | --- |\n| safe | **bold** |\n\n```html\n<script>alert(1)</script>\n```\n\n![Diagram](https://example.com/diagram.png)');

    expect(doc.querySelector('h1')?.textContent).toBe('Guide');
    expect(doc.querySelector('table strong')?.textContent).toBe('bold');
    expect(doc.querySelector('pre code')?.textContent).toContain('<script>alert(1)</script>');
    expect(doc.querySelector('script')).toBeNull();
    expect(doc.querySelector('img')?.getAttribute('src')).toBe('https://example.com/diagram.png');
  });

  it('preserves resolved and disabled link attributes used by the reader', () => {
    const doc = render('[Internal](guide.md) [External](https://example.com) [Missing](missing.md)', [
      { original: 'guide.md', href: '/docs/guide#setup', topic: 'guide', status: 'resolved' },
      { original: 'https://example.com', href: 'https://example.com', external: true, status: 'resolved' }
    ]);
    const [internal, external, missing] = doc.querySelectorAll('a');

    expect(internal.getAttribute('href')).toBe('/docs/guide#setup');
    expect(internal.dataset.docTopic).toBe('guide');
    expect(external.getAttribute('target')).toBe('_blank');
    expect(external.getAttribute('rel')).toBe('noreferrer noopener');
    expect(missing.getAttribute('href')).toBe('#');
    expect(missing.getAttribute('aria-disabled')).toBe('true');
    expect(missing.classList.contains('docs-link-unresolved')).toBe(true);
  });

  it.each([
    ['scripts and handlers', '<script>alert(1)</script><img src="x" onerror="alert(1)"><p onclick="alert(1)">safe</p>'],
    ['embedded documents', '<iframe srcdoc="<script>alert(1)</script>"></iframe><object data="https://evil.example"></object><embed src="https://evil.example">'],
    ['SVG handlers and links', '<svg onload="alert(1)"><a xlink:href="javascript:alert(1)"><text>unsafe</text></a><foreignObject><iframe src="https://evil.example"></iframe></foreignObject></svg>'],
    ['MathML mutation XSS', '<math><mtext><table><mglyph><style><!--</style><img title="--><img src=x onerror=alert(1)>">']
  ])('removes active content from %s after HTML reparsing', (_, markdown) => {
    const doc = render(markdown);

    expect(doc.querySelector('script, iframe, object, embed, foreignObject')).toBeNull();
    for (const element of doc.querySelectorAll('*')) {
      for (const attribute of element.attributes) {
        expect(attribute.name).not.toMatch(/^on/i);
        if (/^(?:href|src|xlink:href)$/i.test(attribute.name)) {
          expect(attribute.value).not.toMatch(/^\s*(?:javascript|vbscript):/i);
        }
      }
    }
  });

  it.each([
    'javascript:alert(1)',
    'JaVaScRiPt:alert(1)',
    'java&#x09;script:alert(1)',
    'data:text/html,<script>alert(1)</script>'
  ])('removes an unsafe raw link URL: %s', (href) => {
    const doc = render(`<a href="${href}">label</a>`);

    expect(doc.querySelector('a')?.textContent).toBe('label');
    expect(doc.querySelector('a')?.hasAttribute('href')).toBe(false);
  });

  it('sanitizes URLs supplied by link resolutions as well as Markdown', () => {
    const doc = render('[Resolved](guide.md) [Markdown](javascript:alert%281%29)', [
      { original: 'guide.md', href: 'javascript:alert(1)', topic: 'guide', status: 'resolved' }
    ]);

    expect(doc.querySelectorAll('a')).toHaveLength(2);
    for (const anchor of doc.querySelectorAll('a')) {
      expect(anchor.hasAttribute('href')).toBe(false);
    }
  });

  it('does not allow link metadata to inject attributes or elements', () => {
    const doc = render('[Guide](guide.md)', [
      { original: 'guide.md', href: '/docs/guide" onclick="alert(1)', topic: '"><img src=x onerror=alert(1)>', status: 'resolved' }
    ]);

    expect(doc.querySelector('a')?.hasAttribute('onclick')).toBe(false);
    expect(doc.querySelector('img')).toBeNull();
    expect(doc.querySelector('a')?.dataset.docTopic).toBe('"><img src=x onerror=alert(1)>');
  });

  it('strips DOM-clobbering names without removing safe content', () => {
    const doc = render('<form id="attributes"><input name="parentNode"><input name="safe-field"></form>');

    expect(doc.querySelector('[id="attributes"], [name="parentNode"]')).toBeNull();
    expect(doc.querySelector('[name="safe-field"]')).not.toBeNull();
  });

  it('preserves safe SVG while removing its executable attributes', () => {
    const doc = render('<svg viewBox="0 0 10 10"><circle cx="5" cy="5" r="4" onmouseover="alert(1)"></circle></svg>');

    expect(doc.querySelector('svg')?.getAttribute('viewBox')).toBe('0 0 10 10');
    expect(doc.querySelector('circle')?.getAttribute('r')).toBe('4');
    expect(doc.querySelector('circle')?.hasAttribute('onmouseover')).toBe(false);
  });
});
