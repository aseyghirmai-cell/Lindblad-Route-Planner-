/* Lindblad Route Planner Local First - streaming OLEX index worker */
'use strict';

const RECORD_SIZE = 18;
const BUFFER_CHUNK_BYTES = 256 * 1024;
const FLUSH_BYTES = 2 * 1024 * 1024;
const MAX_BUFFER_BYTES = 128 * 1024 * 1024;
let cancelled = false;

self.onmessage = async (event) => {
  const msg = event.data || {};
  if (msg.type === 'cancel') {
    cancelled = true;
    return;
  }
  if (msg.type !== 'index' || !msg.file || !msg.id) return;
  cancelled = false;
  try {
    const result = await indexOlex(msg.file, msg.id, msg.name || msg.file.name);
    self.postMessage({ type: 'complete', result });
  } catch (error) {
    self.postMessage({ type: 'error', message: error?.message || String(error) });
  }
};

function assertNotCancelled() {
  if (cancelled) throw new Error('Indexing cancelled.');
}

async function getOlexDirectory(id, reset = false) {
  const root = await navigator.storage.getDirectory();
  const olex = await root.getDirectoryHandle('olex', { create: true });
  if (reset) {
    try { await olex.removeEntry(id, { recursive: true }); } catch (_) {}
  }
  return olex.getDirectoryHandle(id, { create: true });
}

async function writeJson(dir, name, value) {
  const handle = await dir.getFileHandle(name, { create: true });
  const writable = await handle.createWritable();
  await writable.write(JSON.stringify(value));
  await writable.close();
}

function tileId(lat, lon) {
  const lt = Math.floor(Math.min(89.999999, lat));
  const ln = Math.floor(Math.min(179.999999, lon));
  const sign = (v) => `${v >= 0 ? '+' : '-'}${String(Math.abs(v)).padStart(3, '0')}`;
  return `${sign(lt)}_${sign(ln)}`;
}

class TileSpooler {
  constructor(dir) {
    this.dir = dir;
    this.buffers = new Map();
    this.totalBuffered = 0;
    this.stats = new Map();
  }

  add(id, record, lat, lon, depth) {
    let state = this.buffers.get(id);
    if (!state) {
      state = { parts: [], current: new Uint8Array(BUFFER_CHUNK_BYTES), offset: 0, bytes: 0, last: performance.now() };
      this.buffers.set(id, state);
    }
    if (state.current.byteLength - state.offset < record.byteLength) {
      state.parts.push(state.current.subarray(0, state.offset));
      state.current = new Uint8Array(BUFFER_CHUNK_BYTES);
      state.offset = 0;
    }
    state.current.set(record, state.offset);
    state.offset += record.byteLength;
    state.bytes += record.byteLength;
    state.last = performance.now();
    this.totalBuffered += record.byteLength;

    let stat = this.stats.get(id);
    if (!stat) {
      stat = { records: 0, bytes: 0, minLat: lat, maxLat: lat, minLon: lon, maxLon: lon, minDepth: depth, maxDepth: depth };
      this.stats.set(id, stat);
    }
    stat.records += 1;
    stat.minLat = Math.min(stat.minLat, lat); stat.maxLat = Math.max(stat.maxLat, lat);
    stat.minLon = Math.min(stat.minLon, lon); stat.maxLon = Math.max(stat.maxLon, lon);
    stat.minDepth = Math.min(stat.minDepth, depth); stat.maxDepth = Math.max(stat.maxDepth, depth);
  }

  async maybeFlush(id) {
    const state = this.buffers.get(id);
    if (state?.bytes >= FLUSH_BYTES) await this.flush(id);
    while (this.totalBuffered > MAX_BUFFER_BYTES) {
      let oldestId = null; let oldest = Infinity;
      for (const [key, value] of this.buffers) {
        if (value.bytes && value.last < oldest) { oldest = value.last; oldestId = key; }
      }
      if (!oldestId) break;
      await this.flush(oldestId);
    }
  }

  async flush(id) {
    const state = this.buffers.get(id);
    if (!state || !state.bytes) return;
    assertNotCancelled();
    const handle = await this.dir.getFileHandle(`${id}.bin`, { create: true });
    const existing = await handle.getFile();
    const writable = await handle.createWritable({ keepExistingData: true });
    await writable.seek(existing.size);
    // One combined write is dramatically faster than one filesystem write per sounding.
    const combined = new Uint8Array(state.bytes);
    let offset = 0;
    for (const part of state.parts) { combined.set(part, offset); offset += part.byteLength; }
    combined.set(state.current.subarray(0, state.offset), offset);
    await writable.write(combined);
    await writable.close();
    const stat = this.stats.get(id);
    stat.bytes = existing.size + state.bytes;
    this.totalBuffered -= state.bytes;
    state.parts = [];
    state.current = new Uint8Array(BUFFER_CHUNK_BYTES);
    state.offset = 0;
    state.bytes = 0;
    state.last = performance.now();
  }

  async close() {
    for (const id of [...this.buffers.keys()]) await this.flush(id);
  }

  manifestTiles() {
    return [...this.stats.entries()].map(([id, s]) => ({ id, file: `${id}.bin`, ...s })).sort((a, b) => a.id.localeCompare(b.id));
  }
}

function encodeRecord(latKey, lonKey, count, minDepth, meanDepth) {
  const out = new Uint8Array(RECORD_SIZE);
  const v = new DataView(out.buffer);
  v.setInt32(0, latKey, true);
  v.setInt32(4, lonKey, true);
  v.setUint16(8, Math.min(65535, count), true);
  v.setFloat32(10, minDepth, true);
  v.setFloat32(14, meanDepth, true);
  return out;
}

class ByteReader {
  constructor(stream) {
    this.reader = stream.getReader();
    this.buffers = [];
    this.length = 0;
    this.done = false;
  }
  async fill(n) {
    while (this.length < n && !this.done) {
      const { value, done } = await this.reader.read();
      this.done = done;
      if (value?.byteLength) { this.buffers.push(value); this.length += value.byteLength; }
    }
  }
  async readExact(n) {
    await this.fill(n);
    if (this.length < n) throw new Error('Unexpected end of compressed OLEX data.');
    const out = new Uint8Array(n);
    let offset = 0;
    while (offset < n) {
      const first = this.buffers[0];
      const take = Math.min(first.byteLength, n - offset);
      out.set(first.subarray(0, take), offset);
      offset += take;
      this.length -= take;
      if (take === first.byteLength) this.buffers.shift(); else this.buffers[0] = first.subarray(take);
    }
    return out;
  }
  async readAllTextWithPrefix(prefix) {
    const decoder = new TextDecoder();
    let pending = decoder.decode(prefix, { stream: true });
    for (const b of this.buffers) pending += decoder.decode(b, { stream: true });
    this.buffers = []; this.length = 0;
    while (!this.done) {
      const { value, done } = await this.reader.read();
      this.done = done;
      if (value) pending += decoder.decode(value, { stream: !done });
    }
    pending += decoder.decode();
    return pending;
  }
}

async function indexOlex(file, id, displayName) {
  if (!('DecompressionStream' in self)) throw new Error('This browser does not support streaming gzip. Use current Chrome or Edge.');
  const dir = await getOlexDirectory(id, true);
  const tilesDir = await dir.getDirectoryHandle('tiles', { create: true });
  const spooler = new TileSpooler(tilesDir);
  let compressedRead = 0;
  let lastProgress = 0;
  const counter = new TransformStream({
    transform(chunk, controller) {
      compressedRead += chunk.byteLength;
      const now = performance.now();
      if (now - lastProgress > 350) {
        lastProgress = now;
        self.postMessage({ type: 'progress', phase: 'Reading compressed OLEX data', bytesRead: compressedRead, totalBytes: file.size });
      }
      controller.enqueue(chunk);
    }
  });
  const decompressed = file.stream().pipeThrough(counter).pipeThrough(new DecompressionStream('gzip'));
  const reader = new ByteReader(decompressed);
  const prefix = await reader.readExact(8);
  const magic = new TextDecoder().decode(prefix);
  let info;
  if (magic === 'OLXGRID1') info = await parseIndexed(reader, spooler, file, displayName);
  else info = await parseRaw(reader, prefix, spooler, file, displayName);
  await spooler.close();
  const manifest = {
    version: 1,
    format: 'lrp-browser-tiles-v1',
    id,
    name: displayName,
    sourceFilename: file.name,
    sourceSizeBytes: file.size,
    createdUTC: new Date().toISOString(),
    latRes: info.latRes,
    lonRes: info.lonRes,
    records: info.records,
    minLat: info.minLat,
    maxLat: info.maxLat,
    minLon: info.minLon,
    maxLon: info.maxLon,
    minDepth: info.minDepth,
    maxDepth: info.maxDepth,
    tiles: spooler.manifestTiles()
  };
  await writeJson(dir, 'manifest.json', manifest);
  self.postMessage({ type: 'progress', phase: 'Local index complete', bytesRead: file.size, totalBytes: file.size, records: info.records, progress: 1 });
  return manifest;
}

async function parseIndexed(reader, spooler, file, displayName) {
  const u32 = (b) => new DataView(b.buffer, b.byteOffset, b.byteLength).getUint32(0, true);
  const metaLength = u32(await reader.readExact(4));
  if (metaLength > 16 * 1024 * 1024) throw new Error('Invalid OLEX index metadata.');
  let metadata = {};
  try { metadata = JSON.parse(new TextDecoder().decode(await reader.readExact(metaLength))); } catch (_) {}
  const dims = new DataView((await reader.readExact(48)).buffer);
  const latRes = dims.getFloat64(0, true); const lonRes = dims.getFloat64(8, true);
  let minLat = dims.getFloat64(16, true); let maxLat = dims.getFloat64(24, true);
  let minLon = dims.getFloat64(32, true); let maxLon = dims.getFloat64(40, true);
  const records = u32(await reader.readExact(4));
  if (records > 100_000_000) throw new Error('The OLEX index declares an unreasonable number of records.');
  let minDepth = Infinity; let maxDepth = -Infinity;
  for (let i = 0; i < records; i++) {
    assertNotCancelled();
    const raw = await reader.readExact(RECORD_SIZE);
    const v = new DataView(raw.buffer, raw.byteOffset, raw.byteLength);
    const latKey = v.getInt32(0, true); const lonKey = v.getInt32(4, true);
    const depth = v.getFloat32(10, true); const mean = v.getFloat32(14, true);
    const lat = latKey * latRes; const lon = lonKey * lonRes;
    const id = tileId(lat, lon);
    spooler.add(id, raw.slice(), lat, lon, Math.min(depth, mean));
    await spooler.maybeFlush(id);
    minDepth = Math.min(minDepth, depth, mean); maxDepth = Math.max(maxDepth, depth, mean);
    if (i % 25000 === 0) {
      self.postMessage({ type: 'progress', phase: `Indexing ${metadata.name || displayName}`, bytesRead: i, totalBytes: records, records: i, progress: records ? i / records : 0 });
      await new Promise(r => setTimeout(r, 0));
    }
  }
  return { latRes, lonRes, records, minLat, maxLat, minLon, maxLon, minDepth, maxDepth };
}

async function parseRaw(reader, prefix, spooler, file, displayName) {
  // Raw OLEX sounding exports are gzip text with: latitude longitude depth.
  const decoder = new TextDecoder();
  let pending = decoder.decode(prefix, { stream: true });
  // readExact(8) may have pulled a larger decompressed chunk; preserve the remainder.
  for (const buffered of reader.buffers) pending += decoder.decode(buffered, { stream: true });
  reader.buffers = [];
  reader.length = 0;
  let records = 0;
  const latRes = .001; const lonRes = .002;
  let minLat = Infinity, maxLat = -Infinity, minLon = Infinity, maxLon = -Infinity, minDepth = Infinity, maxDepth = -Infinity;
  const processLine = async (line) => {
    line = line.trim();
    if (!line || line.startsWith('#')) return;
    const f = line.split(/\s+/);
    if (f.length < 3) return;
    const lat = Number(f[0]); const lon = Number(f[1]); const depth = Number(f[2]);
    if (!Number.isFinite(lat) || !Number.isFinite(lon) || !Number.isFinite(depth) || lat < -90 || lat > 90 || lon < -180 || lon > 180 || depth < 0) return;
    const latKey = Math.round(lat / latRes); const lonKey = Math.round(lon / lonRes);
    const id = tileId(lat, lon);
    spooler.add(id, encodeRecord(latKey, lonKey, 1, depth, depth), lat, lon, depth);
    await spooler.maybeFlush(id);
    records++;
    minLat = Math.min(minLat, lat); maxLat = Math.max(maxLat, lat);
    minLon = Math.min(minLon, lon); maxLon = Math.max(maxLon, lon);
    minDepth = Math.min(minDepth, depth); maxDepth = Math.max(maxDepth, depth);
  };

  while (!reader.done) {
    assertNotCancelled();
    const { value, done } = await reader.reader.read();
    reader.done = done;
    if (value) pending += decoder.decode(value, { stream: !done });
    const lines = pending.split(/\r?\n/);
    pending = lines.pop() || '';
    for (const line of lines) await processLine(line);
    if (records % 25000 < lines.length) {
      self.postMessage({ type: 'progress', phase: `Indexing ${displayName}`, bytesRead: records, totalBytes: 0, records });
      await new Promise(r => setTimeout(r, 0));
    }
  }
  pending += decoder.decode();
  if (pending.trim()) await processLine(pending);
  if (!records) throw new Error('No valid OLEX soundings were found. Supported files are .olxidx.gz or gzip text exports containing latitude longitude depth rows.');
  return { latRes, lonRes, records, minLat, maxLat, minLon, maxLon, minDepth, maxDepth };
}
