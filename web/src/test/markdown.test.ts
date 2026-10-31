import { describe, expect, it } from 'vitest';
import { renderMarkdown, sanitizeHtml } from '../lib/markdown';

describe('sanitizeHtml', () => {
  it('drops script-like elements whole and unwraps unknown tags', () => {
    const out = sanitizeHtml(
      '<p>hi</p><script>x()</script><iframe srcdoc="<script>1</script>"></iframe><object data="javascript:1"></object><marquee>text</marquee>',
    );
    expect(out).toBe('<p>hi</p>text');
  });

  it('strips event handlers and unsafe urls, keeps safe links with rel', () => {
    const out = sanitizeHtml(
      '<a href="javascript:alert(1)" onclick="x()">a</a><a href="https://x.y" target="_blank">b</a><img src="data:text/html,x"><a href="/t/TP-1">c</a>',
    );
    expect(out).toBe(
      '<a>a</a><a href="https://x.y" rel="noopener noreferrer nofollow">b</a><img><a href="/t/TP-1" rel="noopener noreferrer nofollow">c</a>',
    );
  });

  it('rejects urls with hidden control characters in the scheme', () => {
    expect(sanitizeHtml('<a href="java\tscript:alert(1)">x</a>')).toBe('<a>x</a>');
  });

  it('renders markdown', () => {
    expect(renderMarkdown('**bold** `code`')).toBe('<p><strong>bold</strong> <code>code</code></p>\n');
  });
});
