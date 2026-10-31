// A fixture for scripts/test_tripwire_hash.py, not a test anything runs: it pins the vitest half
// of the body_sha256 definition (TP-134). Two blocks hold a case of the same name on purpose, so
// the test proves the `describe` in the test_ref is what picks between them.
import { describe, expect, it } from 'vitest';

describe('the wrong block', () => {
  it('holds a case of the same name', () => {
    expect(1).toBe(1);
  });
});

describe('the sample block', () => {
  it('holds a case of the same name', () => {
    expect(2).toBe(2);
    expect([1, 2].map((n) => {
      return n + 1;
    })).toEqual([2, 3]);
  });

  it('is a sibling that stays out of the slice', () => {
    expect(3).toBe(3);
  });
});
