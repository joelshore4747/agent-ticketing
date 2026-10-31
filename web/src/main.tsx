import { render } from '@solidjs/web';
import { QueryClient, QueryClientProvider } from '@tanstack/solid-query';
import { RouterProvider } from '@tanstack/solid-router';
import { router } from './router';
// The app's two faces, bundled rather than fetched from a CDN so it looks the same offline (TP-22):
// Instrument Sans for everything, and JetBrains Mono for every numeral, key, path and timestamp.
// Both are variable on weight, so one file each covers every weight the type scale uses, and each
// stylesheet declares its files per script, so a browser loads only the ones it uses.
import '@fontsource-variable/instrument-sans/wght.css';
import '@fontsource-variable/jetbrains-mono/wght.css';
import './styles/global.css';

const queryClient = new QueryClient();

const root = document.getElementById('app');

if (root) {
  render(
    () => (
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    ),
    root,
  );
}
