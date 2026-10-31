// Drives the built web app in headless Chromium and compares screenshots of its main pages
// against committed baselines (scripts/render-baselines/../TP-133). Not part of the app bundle —
// run through `npm run render-check`, from scripts/render-check.sh, which builds web/dist, seeds a
// throwaway API with fixed data, and passes this script the ticket and batch it created.
//
// Neither Playwright nor a browser is a project dependency, and that is deliberate (TP-141): the web
// app's dependency budget is one markdown renderer, and the npm package would still leave the
// ~170MB browser to a separate `npx playwright install chromium`. So this resolves, in order,
// RENDER_CHECK_PLAYWRIGHT, then web/node_modules (should anyone ever declare it, no edit needed
// here), then whatever npx has cached. Missing tooling exits non-zero naming the install command —
// it never passes quietly, because `just gate` runs this and a silent skip would be a green gate
// over an unchecked stylesheet. RENDER_CHECK_CHROMIUM points at a browser elsewhere;
// RENDER_CHECK_THRESHOLD (fraction of pixels, default 0.001) changes how much a page may drift.
import fs from 'node:fs';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import zlib from 'node:zlib';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const webRoot = path.resolve(here, '..');
const distDir = path.join(webRoot, 'dist');
const baselineDir = path.resolve(webRoot, '..', 'scripts', 'render-baselines');
const THRESHOLD = Number(process.env.RENDER_CHECK_THRESHOLD || 0.001);

function arg(name) {
  const i = process.argv.indexOf(`--${name}`);
  return i !== -1 ? process.argv[i + 1] : undefined;
}

const apiBase = arg('api');
const ticketKey = arg('ticket');
const batchId = arg('batch');
const update = process.argv.includes('--update');
if (!apiBase || !ticketKey || !batchId) {
  console.error('usage: render-check.mjs --api <url> --ticket <key> --batch <id> [--update]');
  process.exit(2);
}

function findPlaywright() {
  // An override that points at nothing is a typo, not a reason to fall back and hide it.
  if (process.env.RENDER_CHECK_PLAYWRIGHT) {
    return fs.existsSync(process.env.RENDER_CHECK_PLAYWRIGHT) ? process.env.RENDER_CHECK_PLAYWRIGHT : null;
  }
  const declared = path.join(webRoot, 'node_modules', 'playwright', 'index.mjs');
  if (fs.existsSync(declared)) return declared;
  // A cache, not an install: `npm cache clean` takes this away, hence the message below.
  const npxDir = path.join(os.homedir(), '.npm', '_npx');
  if (!fs.existsSync(npxDir)) return null;
  for (const hash of fs.readdirSync(npxDir)) {
    const candidate = path.join(npxDir, hash, 'node_modules', 'playwright', 'index.mjs');
    if (fs.existsSync(candidate)) return candidate;
  }
  return null;
}

const playwrightPath = findPlaywright();
if (!playwrightPath) {
  console.error('render-check: no Playwright found, so the stylesheet went unchecked.');
  console.error('  install it with:  cd web && npx playwright install chromium');
  console.error('  looked at RENDER_CHECK_PLAYWRIGHT, web/node_modules/playwright,');
  console.error('  then ~/.npm/_npx/*/node_modules/playwright/index.mjs');
  process.exit(2);
}
const { chromium } = await import(playwrightPath);

function findChromium() {
  if (process.env.RENDER_CHECK_CHROMIUM) {
    return fs.existsSync(process.env.RENDER_CHECK_CHROMIUM) ? process.env.RENDER_CHECK_CHROMIUM : null;
  }
  const cacheDir = path.join(os.homedir(), '.cache', 'ms-playwright');
  // 1228 first on purpose: the committed baselines were rendered by that build, and a newer
  // Chromium moves enough pixels to fail pages nobody touched.
  const preferred = path.join(cacheDir, 'chromium-1228', 'chrome-linux64', 'chrome');
  if (fs.existsSync(preferred)) return preferred;
  if (fs.existsSync(cacheDir)) {
    const dirs = fs
      .readdirSync(cacheDir)
      .filter((d) => d.startsWith('chromium-'))
      .sort()
      .reverse();
    for (const d of dirs) {
      const candidate = path.join(cacheDir, d, 'chrome-linux64', 'chrome');
      if (fs.existsSync(candidate)) return candidate;
    }
  }
  // Last resort: let Playwright name the browser it manages itself.
  try {
    const own = chromium.executablePath();
    if (own && fs.existsSync(own)) return own;
  } catch {
    // Playwright has no browser of its own either — fall through to the message below.
  }
  return null;
}

const chromiumPath = findChromium();
if (!chromiumPath) {
  console.error('render-check: no Chromium found, so the stylesheet went unchecked.');
  console.error('  install it with:  cd web && npx playwright install chromium');
  console.error('  looked at RENDER_CHECK_CHROMIUM, then');
  console.error('  ~/.cache/ms-playwright/chromium-*/chrome-linux64/chrome');
  process.exit(2);
}

// --- a static server for web/dist, proxying the one endpoint the app calls at runtime ---

const CONTENT_TYPES = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.woff': 'font/woff',
  '.woff2': 'font/woff2',
  '.json': 'application/json',
  '.ico': 'image/x-icon',
  '.png': 'image/png',
};

function startStaticServer() {
  return new Promise((resolve) => {
    const server = http.createServer(async (req, res) => {
      try {
        const url = new URL(req.url, 'http://localhost');
        if (url.pathname === '/graphql') {
          const chunks = [];
          for await (const chunk of req) chunks.push(chunk);
          const upstream = await fetch(`${apiBase}/graphql`, {
            method: req.method,
            headers: { 'content-type': 'application/json' },
            body: Buffer.concat(chunks),
          });
          res.writeHead(upstream.status, { 'content-type': upstream.headers.get('content-type') || 'application/json' });
          res.end(Buffer.from(await upstream.arrayBuffer()));
          return;
        }
        // Anything that is not a file in dist is a client-side route: index.html and the router
        // take it from there, same as any other static host in front of an SPA.
        let filePath = path.join(distDir, decodeURIComponent(url.pathname));
        if (!filePath.startsWith(distDir)) filePath = distDir; // no escaping dist via ../
        if (!fs.existsSync(filePath) || fs.statSync(filePath).isDirectory()) {
          filePath = path.join(distDir, 'index.html');
        }
        res.writeHead(200, { 'content-type': CONTENT_TYPES[path.extname(filePath)] || 'application/octet-stream' });
        fs.createReadStream(filePath).pipe(res);
      } catch (err) {
        res.writeHead(500);
        res.end(String(err));
      }
    });
    server.listen(0, '127.0.0.1', () => resolve(server));
  });
}

// --- a minimal PNG decoder (8-bit, non-interlaced — exactly what page.screenshot() writes), so
// comparing two screenshots needs nothing beyond Node's own zlib ---

function paeth(a, b, c) {
  const p = a + b - c;
  const pa = Math.abs(p - a);
  const pb = Math.abs(p - b);
  const pc = Math.abs(p - c);
  if (pa <= pb && pa <= pc) return a;
  return pb <= pc ? b : c;
}

function decodePNG(buf) {
  if (buf.readUInt32BE(0) !== 0x89504e47) throw new Error('not a PNG');
  let offset = 8;
  let width = 0;
  let height = 0;
  let channels = 0;
  const idat = [];
  while (offset < buf.length) {
    const len = buf.readUInt32BE(offset);
    const type = buf.toString('ascii', offset + 4, offset + 8);
    const data = buf.subarray(offset + 8, offset + 8 + len);
    if (type === 'IHDR') {
      width = data.readUInt32BE(0);
      height = data.readUInt32BE(4);
      const bitDepth = data[8];
      const colorType = data[9];
      if (bitDepth !== 8) throw new Error(`unsupported PNG bit depth ${bitDepth}`);
      if (data[12] !== 0) throw new Error('interlaced PNG not supported');
      channels = { 0: 1, 2: 3, 4: 2, 6: 4 }[colorType];
      if (!channels) throw new Error(`unsupported PNG color type ${colorType}`);
    } else if (type === 'IDAT') {
      idat.push(data);
    } else if (type === 'IEND') {
      break;
    }
    offset += 8 + len + 4;
  }
  const raw = zlib.inflateSync(Buffer.concat(idat));
  const stride = width * channels;
  const pixels = Buffer.alloc(height * stride);
  let pos = 0;
  for (let y = 0; y < height; y++) {
    const filterType = raw[pos];
    pos++;
    for (let x = 0; x < stride; x++) {
      const rawByte = raw[pos + x];
      const a = x >= channels ? pixels[y * stride + x - channels] : 0;
      const b = y > 0 ? pixels[(y - 1) * stride + x] : 0;
      const c = x >= channels && y > 0 ? pixels[(y - 1) * stride + x - channels] : 0;
      let value;
      if (filterType === 0) value = rawByte;
      else if (filterType === 1) value = rawByte + a;
      else if (filterType === 2) value = rawByte + b;
      else if (filterType === 3) value = rawByte + Math.floor((a + b) / 2);
      else if (filterType === 4) value = rawByte + paeth(a, b, c);
      else throw new Error(`unsupported PNG filter type ${filterType}`);
      pixels[y * stride + x] = value & 0xff;
    }
    pos += stride;
  }
  return { width, height, channels, pixels };
}

/** Pixels that differ by more than a couple of levels per channel (headless rendering is otherwise
 *  bit-exact run to run on the same machine), as a fraction of the image. */
function diffImages(a, b) {
  if (a.width !== b.width || a.height !== b.height) {
    return { sizeMismatch: true, before: [a.width, a.height], after: [b.width, b.height] };
  }
  const total = a.width * a.height;
  const channels = Math.min(a.channels, b.channels);
  let diffPixels = 0;
  for (let i = 0; i < total; i++) {
    const ao = i * a.channels;
    const bo = i * b.channels;
    for (let c = 0; c < channels; c++) {
      if (Math.abs(a.pixels[ao + c] - b.pixels[bo + c]) > 2) {
        diffPixels++;
        break;
      }
    }
  }
  return { sizeMismatch: false, diffPixels, total, ratio: diffPixels / total };
}

// --- drive the four pages, at desktop and 360px, and either write or check baselines ---

const PAGES = [
  { name: 'board', path: '/' },
  { name: 'ticket', path: `/t/${ticketKey}` },
  { name: 'batch', path: `/b/${batchId}` },
  { name: 'needs-you', path: '/needs-you' },
];
const VIEWPORTS = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'mobile', width: 360, height: 800 },
];

fs.mkdirSync(baselineDir, { recursive: true });
const server = await startStaticServer();
const { port } = server.address();
const base = `http://127.0.0.1:${port}`;

const browser = await chromium.launch({ executablePath: chromiumPath, headless: true });
const results = [];
try {
  for (const viewport of VIEWPORTS) {
    const context = await browser.newContext({ viewport: { width: viewport.width, height: viewport.height } });
    const page = await context.newPage();
    // Every relative-time string on these pages reads Date.now(); freeze it well clear of the
    // seed data's real timestamps so "3 minutes ago" never depends on how long seeding took.
    await page.clock.setFixedTime(new Date(Date.now() + 5 * 365 * 24 * 60 * 60 * 1000));
    for (const pageSpec of PAGES) {
      await page.goto(base + pageSpec.path, { waitUntil: 'networkidle' });
      await page.evaluate(() => document.fonts.ready).catch(() => {});
      try {
        await page.waitForSelector('.skeleton-line', { state: 'detached', timeout: 5000 });
      } catch {
        // Nothing was loading, or it did not clear in time — screenshot anyway rather than hang.
      }
      const name = `${pageSpec.name}-${viewport.name}.png`;
      const baselinePath = path.join(baselineDir, name);
      if (update) {
        await page.screenshot({ path: baselinePath, fullPage: true });
        results.push({ name, ok: true, reason: 'baseline written' });
        continue;
      }
      const currentPath = path.join(os.tmpdir(), `render-check-${process.pid}-${name}`);
      await page.screenshot({ path: currentPath, fullPage: true });
      if (!fs.existsSync(baselinePath)) {
        results.push({ name, ok: false, reason: 'no baseline yet — run with --update' });
        continue;
      }
      const diff = diffImages(decodePNG(fs.readFileSync(baselinePath)), decodePNG(fs.readFileSync(currentPath)));
      fs.rmSync(currentPath, { force: true });
      if (diff.sizeMismatch) {
        results.push({ name, ok: false, reason: `size changed from ${diff.before.join('x')} to ${diff.after.join('x')}` });
      } else if (diff.ratio > THRESHOLD) {
        const pct = (diff.ratio * 100).toFixed(3);
        results.push({ name, ok: false, reason: `${diff.diffPixels} of ${diff.total} pixels differ (${pct}%, over the ${(THRESHOLD * 100).toFixed(3)}% threshold)` });
      } else {
        results.push({ name, ok: true, reason: `${diff.diffPixels} of ${diff.total} pixels differ` });
      }
    }
    await context.close();
  }
} finally {
  await browser.close();
  server.close();
}

let failed = 0;
for (const r of results) {
  console.log(`${r.ok ? 'PASS' : 'FAIL'}  ${r.name} — ${r.reason}`);
  if (!r.ok) failed++;
}
console.log(
  update
    ? `\nWrote ${results.length} baselines to ${path.relative(process.cwd(), baselineDir)}.`
    : failed === 0
      ? `\nAll ${results.length} pages matched their baseline.`
      : `\n${failed} of ${results.length} page(s) drifted from their baseline.`,
);
process.exit(update ? 0 : failed === 0 ? 0 : 1);
