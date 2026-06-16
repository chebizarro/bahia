import { describe, expect, it } from 'vitest';
import { NAV_SECTIONS, isActiveNavLink } from '../../src/lib/components/nav-model.js';

// T-9: Nav model includes Security link under Operations
describe('security nav integration', () => {
  it('includes Security link under Operations section', () => {
    const operations = NAV_SECTIONS.find((s) => s.title === 'Operations');
    expect(operations).toBeDefined();

    const securityLink = operations.links.find((l) => l.href === '/security');
    expect(securityLink).toBeDefined();
    expect(securityLink.label).toBe('Security');
    expect(securityLink.docTopic).toBe('features-security');
  });

  it('isActiveNavLink matches /security route', () => {
    expect(isActiveNavLink('/security', '/security')).toBe(true);
    expect(isActiveNavLink('/security/run-123', '/security')).toBe(true);
    expect(isActiveNavLink('/', '/security')).toBe(false);
  });
});
