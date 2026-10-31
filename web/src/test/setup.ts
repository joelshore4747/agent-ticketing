import '@testing-library/jest-dom/vitest';
import { cleanup } from '@solidjs/testing-library';
import { afterEach } from 'vitest';

// jsdom doesn't implement layout/scrolling; the router calls this on navigation.
window.scrollTo = () => {};

afterEach(() => {
  cleanup();
});
