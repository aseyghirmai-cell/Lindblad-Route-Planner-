'use strict';
const CACHE = 'lindblad-route-planner-local-first-v1';
const SHELL = ['./', './index.html', './styles.css', './app.js', './olex-worker.js', './site.webmanifest', './assets/icon.svg', './assets/land.geojson'];
self.addEventListener('install', event => event.waitUntil(caches.open(CACHE).then(c => c.addAll(SHELL)).then(() => self.skipWaiting())));
self.addEventListener('activate', event => event.waitUntil(caches.keys().then(keys => Promise.all(keys.filter(k => k !== CACHE).map(k => caches.delete(k)))).then(() => self.clients.claim())));
self.addEventListener('fetch', event => {
  if (event.request.method !== 'GET') return;
  event.respondWith(caches.match(event.request).then(hit => hit || fetch(event.request).then(resp => {
    const copy = resp.clone(); caches.open(CACHE).then(c => c.put(event.request, copy)); return resp;
  }).catch(() => caches.match('./index.html'))));
});
