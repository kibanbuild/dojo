// Dojo app-shell worker. Bump VERSION when this file's own logic changes.
const VERSION = 'v1';
const PREFIX = 'dojo-shell-';
const CACHE = PREFIX + VERSION;
const SHELL = '/app/index.html';
const OFFLINE = '/app/offline.html';
const ASSETS = '/app/assets/';

async function install() {
  const cache = await caches.open(CACHE);
  const res = await fetch(SHELL, { cache: 'reload' });
  if (!res.ok) throw new Error('shell unavailable');
  const html = await res.clone().text();
  const urls = new Set(html.match(/\/app\/assets\/[^"'\s)]+/g) ?? []);
  await cache.addAll([...urls, OFFLINE]);
  await cache.put(SHELL, res);
}

async function activate() {
  const keys = await caches.keys();
  await Promise.all(keys.filter((k) => k.startsWith(PREFIX) && k !== CACHE).map((k) => caches.delete(k)));
}

async function navigate(request) {
  const cache = await caches.open(CACHE);
  try {
    const res = await fetch(request);
    if (res.ok && (res.headers.get('content-type') ?? '').includes('text/html')) {
      await cache.put(SHELL, res.clone());
    }
    return res;
  } catch {
    return (await cache.match(SHELL)) ?? (await cache.match(OFFLINE)) ?? Response.error();
  }
}

async function asset(request) {
  const cache = await caches.open(CACHE);
  const hit = await cache.match(request);
  if (hit) return hit;
  const res = await fetch(request);
  if (res.ok) await cache.put(request, res.clone());
  return res;
}

self.addEventListener('install', (event) => event.waitUntil(install()));
self.addEventListener('activate', (event) => event.waitUntil(activate()));

self.addEventListener('fetch', (event) => {
  const { request } = event;
  if (request.method !== 'GET') return;
  const url = new URL(request.url);
  if (url.origin !== self.location.origin) return;
  if (url.pathname === '/v1' || url.pathname.startsWith('/v1/')) return;
  if (request.mode === 'navigate' && url.pathname.startsWith('/app/') && url.pathname !== OFFLINE) {
    event.respondWith(navigate(request));
  } else if (url.pathname.startsWith(ASSETS)) {
    event.respondWith(asset(request));
  }
});
