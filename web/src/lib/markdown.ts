import { marked } from 'marked';

// Allowlist sanitizer for marked's output. Anything not listed is unwrapped
// (its text is kept, the tag is dropped); script-like elements are removed whole.
const ALLOWED_TAGS = new Set([
  'p', 'br', 'hr', 'a', 'strong', 'em', 'b', 'i', 'del', 's', 'code', 'pre', 'kbd',
  'ul', 'ol', 'li', 'blockquote', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6',
  'table', 'thead', 'tbody', 'tr', 'th', 'td', 'img', 'input', 'span', 'div',
]);
const DROP_WHOLE = new Set(['script', 'style', 'iframe', 'object', 'embed', 'form', 'svg', 'math', 'template']);
const ALLOWED_ATTRS: Record<string, Set<string>> = {
  a: new Set(['href', 'title']),
  img: new Set(['src', 'alt', 'title']),
  input: new Set(['type', 'checked', 'disabled']),
  code: new Set(['class']),
  th: new Set(['align']),
  td: new Set(['align']),
};
// Absolute http(s)/mailto, fragment, or a relative path. Anything else with a scheme is rejected.
const SAFE_URL = /^(https?:|mailto:|#|\/(?!\/)|\.{0,2}\/)/i;
const HAS_SCHEME = /^[a-z][a-z0-9+.-]*:/i;

function isSafeUrl(value: string): boolean {
  // Browsers ignore control characters and whitespace inside URLs, so strip them before checking.
  const v = value.replace(/[\s\p{Cc}]/gu, '');
  return v === '' || SAFE_URL.test(v) || !HAS_SCHEME.test(v);
}

export function sanitizeHtml(html: string): string {
  const template = document.createElement('template');
  template.innerHTML = html;

  for (const el of [...template.content.querySelectorAll('*')]) {
    const tag = el.tagName.toLowerCase();
    if (DROP_WHOLE.has(tag)) {
      el.remove();
      continue;
    }
    if (!ALLOWED_TAGS.has(tag)) {
      el.replaceWith(...el.childNodes);
      continue;
    }
    const allowed = ALLOWED_ATTRS[tag];
    for (const attr of [...el.attributes]) {
      const name = attr.name.toLowerCase();
      const value = attr.value;
      const keep =
        allowed?.has(name) &&
        (name === 'href' || name === 'src' ? isSafeUrl(value) : true) &&
        (tag === 'input' && name === 'type' ? value === 'checkbox' : true) &&
        (name === 'class' ? /^language-[\w-]+$/.test(value) : true);
      if (!keep) el.removeAttribute(attr.name);
    }
    if (tag === 'input') el.setAttribute('disabled', '');
    if (tag === 'a' && el.hasAttribute('href')) el.setAttribute('rel', 'noopener noreferrer nofollow');
  }
  return template.innerHTML;
}

export function renderMarkdown(source: string): string {
  const html = marked.parse(source, { async: false, breaks: true }) as string;
  return sanitizeHtml(html);
}
