import { Outlet, createRootRoute, createRoute, createRouter, useNavigate, useParams } from '@tanstack/solid-router';
import { Show, createSignal, onSettled } from 'solid-js';
import { needsLogin } from './api/client';
import { LoginGate } from './components/LoginGate';
import { Rail } from './components/Rail';
import { ShortcutsDialog } from './components/ShortcutsDialog';
import { Batches } from './routes/Batches';
import { BatchDetail } from './routes/BatchDetail';
import { Roadmaps } from './routes/Roadmaps';
import { Board } from './routes/Board';
import { Guidance } from './routes/Guidance';
import { TicketDetail } from './routes/TicketDetail';
import { Areas } from './routes/Areas';
import { NeedsYou } from './routes/NeedsYou';
import { parseSearch } from './lib/filters';

function isTypingTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  return ['INPUT', 'TEXTAREA', 'SELECT'].includes(target.tagName) || target.isContentEditable;
}

// The board renders a frame or two after navigation resolves, so retry briefly.
function focusSearch(attempts: number) {
  const input = document.getElementById('ticket-search');
  if (input) input.focus();
  else if (attempts > 0) requestAnimationFrame(() => focusSearch(attempts - 1));
}

function RootLayout() {
  const navigate = useNavigate();
  const [shortcuts, setShortcuts] = createSignal(false);
  onSettled(() => {
    // "/" focuses search from anywhere, jumping to the board first if the page has no search;
    // "?" lists every shortcut.
    const onKeyDown = async (event: KeyboardEvent) => {
      if (isTypingTarget(event.target) || event.ctrlKey || event.metaKey) return;
      if (event.key === '?') {
        event.preventDefault();
        setShortcuts((open) => !open);
        return;
      }
      if (event.key !== '/') return;
      event.preventDefault();
      if (!document.getElementById('ticket-search')) await navigate({ to: '/' });
      focusSearch(10);
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  });
  return (
    <Show when={!needsLogin()} fallback={<LoginGate />}>
      <div class="app">
        <Rail />
        <div class="main">
          <Outlet />
        </div>
        <Show when={shortcuts()}>
          <ShortcutsDialog onClose={() => setShortcuts(false)} />
        </Show>
      </div>
    </Show>
  );
}

const rootRoute = createRootRoute({ component: RootLayout });

const boardRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/',
  validateSearch: parseSearch,
  component: () => <Board />,
});

const aiRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/ai',
  validateSearch: parseSearch,
  component: () => <Board source="ai" />,
});

const humanRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/human',
  validateSearch: parseSearch,
  component: () => <Board source="human" />,
});

// An area is a board of its own: same four columns, only this area's work in them.
function AreaBoard() {
  const params = useParams({ strict: false });
  return <Board areaId={(params() as { areaId?: string }).areaId ?? ''} />;
}

const areaBoardRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/a/$areaId',
  validateSearch: parseSearch,
  component: AreaBoard,
});

const ticketRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/t/$key',
  validateSearch: parseSearch,
  component: TicketDetail,
});

const batchesRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/batches',
  validateSearch: parseSearch,
  component: Batches,
});

const batchRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/b/$batchId',
  validateSearch: parseSearch,
  component: BatchDetail,
});

const roadmapsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/roadmaps',
  validateSearch: parseSearch,
  component: Roadmaps,
});

const guidanceRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/guidance',
  validateSearch: parseSearch,
  component: Guidance,
});

const areasRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/areas',
  validateSearch: parseSearch,
  component: Areas,
});

const needsYouRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/needs-you',
  validateSearch: parseSearch,
  component: NeedsYou,
});

const routeTree = rootRoute.addChildren([
  boardRoute,
  aiRoute,
  humanRoute,
  areaBoardRoute,
  ticketRoute,
  batchesRoute,
  batchRoute,
  roadmapsRoute,
  guidanceRoute,
  areasRoute,
  needsYouRoute,
]);

export const router = createRouter({ routeTree });

declare module '@tanstack/solid-router' {
  interface Register {
    router: typeof router;
  }
}
