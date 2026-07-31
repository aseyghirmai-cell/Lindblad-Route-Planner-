'use strict';
const CACHE = 'lindblad-route-planner-ai-corridor-local-v3';
const SHELL = [
  './','./index.html','./styles.css','./upgrade.css','./app.js','./olex-worker.js','./planner-worker.js',
  './site.webmanifest','./assets/icon.svg','./assets/land.geojson','./assets/planner.bin.gz'
];
self.addEventListener('install', event => event.waitUntil(
  caches.open(CACHE).then(c => c.addAll(SHELL)).then(() => self.skipWaiting())
));
self.addEventListener('activate', event => event.waitUntil(
  caches.keys().then(keys => Promise.all(keys.filter(k => k !== CACHE).map(k => caches.delete(k)))).then(() => self.clients.claim())
));
self.addEventListener('fetch', event => {
  if (event.request.method !== 'GET') return;
  event.respondWith(caches.match(event.request).then(hit => hit || fetch(event.request).then(resp => {
    if (resp.ok) { const copy = resp.clone(); caches.open(CACHE).then(c => c.put(event.request, copy)); }
    return resp;
  })));
});
