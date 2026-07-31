(() => {
  'use strict';

  const $ = id => document.getElementById(id);
  const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
  const CHUNK_BYTES = 8 * 1024 * 1024;

  function escapeHTML(value) {
    return String(value ?? '').replace(/[&<>'"]/g, c => ({
      '&': '&amp;', '<': '&lt;', '>': '&gt;', "'": '&#39;', '"': '&quot;'
    })[c]);
  }

  function formatStorage(bytes) {
    const value = Number(bytes || 0);
    if (!Number.isFinite(value) || value <= 0) return '0 MB';
    const mb = value / 1_000_000;
    if (mb >= 1024) return `${(mb / 1024).toFixed(1)} GB`;
    return `${Math.max(1, Math.round(mb)).toLocaleString()} MB`;
  }

  async function api(url, options = {}) {
    const response = await fetch(url, options);
    const type = response.headers.get('content-type') || '';
    const data = type.includes('application/json') ? await response.json() : await response.text();
    if (!response.ok) {
      const error = new Error(data?.error || `Request failed (${response.status})`);
      error.data = data;
      error.status = response.status;
      throw error;
    }
    return data;
  }

  function setMessage(element, text, kind = '') {
    if (!element) return;
    element.textContent = text;
    element.classList.remove('hidden', 'error');
    if (kind === 'error') element.classList.add('error');
  }

  function setRTZProgress(percent, title, meta) {
    const box = $('rtzUploadProgress');
    if (!box) return;
    box.classList.remove('hidden');
    const pct = Math.max(0, Math.min(100, Number(percent || 0)));
    $('rtzUploadProgressPct').textContent = `${pct.toFixed(1)}%`;
    $('rtzUploadProgressBar').style.width = `${pct}%`;
    $('rtzUploadProgressText').textContent = title || 'Uploading RTZ file…';
    $('rtzUploadProgressMeta').textContent = meta || 'Persistent disk-backed upload in progress.';
  }

  function setOlexProgress(percent, title, meta) {
    const box = $('olexImportProgress');
    if (!box) return;
    box.classList.remove('hidden');
    const pct = Math.max(0, Math.min(100, Number(percent || 0)));
    $('olexImportProgressPct').textContent = `${pct.toFixed(1)}%`;
    $('olexImportProgressBar').style.width = `${pct}%`;
    $('olexImportProgressText').textContent = title || 'Uploading OLEX database…';
    $('olexImportProgressMeta').textContent = meta || 'Persistent disk-backed upload in progress.';
  }

  async function startUpload(file, kind, displayName) {
    return api('/api/upload/start', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({
        kind,
        name: displayName,
        originalName: file.name,
        sizeBytes: file.size,
        lastModified: Number(file.lastModified || 0)
      })
    });
  }

  async function sendUploadChunk(id, file, offset) {
    const end = Math.min(file.size, offset + CHUNK_BYTES);
    const response = await fetch(`/api/upload/chunk?id=${encodeURIComponent(id)}&offset=${offset}`, {
      method: 'POST',
      headers: {'Content-Type': 'application/octet-stream'},
      body: file.slice(offset, end)
    });
    const data = await response.json().catch(() => ({}));
    if (response.status === 409 && Number.isFinite(Number(data.offset))) {
      return Number(data.offset);
    }
    if (!response.ok) throw new Error(data.error || `Upload failed (${response.status})`);
    return Number(data.offset);
  }

  async function resumableUpload(file, kind, displayName, onProgress) {
    const session = await startUpload(file, kind, displayName);
    let offset = Number(session.offset || 0);
    onProgress(offset, file.size, Boolean(session.resumed));
    while (offset < file.size) {
      const next = await sendUploadChunk(session.id, file, offset);
      if (!Number.isFinite(next) || next <= offset) throw new Error('The stored upload offset did not advance.');
      offset = next;
      onProgress(offset, file.size, Boolean(session.resumed));
    }
    return api('/api/upload/finish', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({id: session.id})
    });
  }

  function renderRTZAreas(areas) {
    const list = $('rtzAreaList');
    if (!list) return;
    list.innerHTML = '';
    const active = areas.filter(area => !area.disabled);
    const totalBytes = areas.reduce((sum, area) => sum + Number(area.sizeBytes || 0), 0);
    const totalRoutes = active.reduce((sum, area) => sum + Number(area.routeCount || 0), 0);

    if (!areas.length) {
      list.innerHTML = '<div class="manager-empty">No uploaded RTZ files are stored yet.<br><small>Add one or many RTZ files; they will remain available after restart.</small></div>';
      if ($('rtzStatusTitle')) $('rtzStatusTitle').textContent = 'No uploaded RTZ routes';
      if ($('rtzStatusDetail')) $('rtzStatusDetail').textContent = 'The embedded historical route library remains active.';
      return;
    }

    for (const area of areas) {
      const row = document.createElement('div');
      row.className = 'area-row';
      const status = area.disabled ? 'Disabled' : 'Active in corridor engine';
      const imported = area.importedUTC ? new Date(area.importedUTC).toLocaleString() : 'stored locally';
      row.innerHTML = `<div class="area-row-main"><strong>${escapeHTML(area.name)}</strong><span>${Number(area.routeCount || 0).toLocaleString()} route${Number(area.routeCount || 0) === 1 ? '' : 's'} · ${Number(area.waypointCount || 0).toLocaleString()} waypoints</span><small>${formatStorage(area.sizeBytes)} · ${escapeHTML(status)} · imported ${escapeHTML(imported)}</small></div><div class="area-row-actions"><button class="btn btn-secondary" type="button" data-rtz-rename="${escapeHTML(area.id)}" data-rtz-name="${escapeHTML(area.name)}">Rename</button><button class="btn btn-secondary" type="button" data-rtz-toggle="${escapeHTML(area.id)}" data-enable="${area.disabled ? '1' : '0'}">${area.disabled ? 'Enable' : 'Disable'}</button><button class="btn btn-danger" type="button" data-rtz-remove="${escapeHTML(area.id)}" data-rtz-name="${escapeHTML(area.name)}">Remove</button></div>`;
      list.appendChild(row);
    }

    if ($('rtzStatusTitle')) $('rtzStatusTitle').textContent = `${active.length} active uploaded RTZ file${active.length === 1 ? '' : 's'}`;
    if ($('rtzStatusDetail')) $('rtzStatusDetail').textContent = `${totalRoutes.toLocaleString()} uploaded historical route${totalRoutes === 1 ? '' : 's'} merged into the corridor engine · ${formatStorage(totalBytes)} stored persistently.`;
  }

  function renderOlexAreas(areas) {
    const list = $('olexAreaList');
    if (!list) return;
    list.innerHTML = '';
    if (!areas.length) {
      list.innerHTML = '<div class="manager-empty">No OLEX depth databases are stored on this computer.</div>';
      return;
    }
    let total = 0;
    let enabled = 0;
    for (const area of areas) {
      total += Number(area.sizeBytes || 0);
      if (!area.disabled) enabled++;
      const row = document.createElement('div');
      row.className = 'area-row';
      const records = Number(area.records || 0) > 0 ? `${Number(area.records).toLocaleString()} soundings` : 'Ready for analysis';
      const indexed = Number(area.indexedSizeBytes || 0) > 0 ? ` · ${formatStorage(area.indexedSizeBytes)} indexed` : '';
      const status = area.disabled ? 'Disabled' : area.builtin ? 'Built in · Active' : 'Active';
      row.innerHTML = `<div class="area-row-main"><strong>${escapeHTML(area.name)}</strong><span>${records}</span><small class="area-size">${formatStorage(area.sizeBytes)} source${indexed} · ${status}</small></div><div class="area-row-actions"><button class="btn btn-secondary" type="button" data-olex-rename="${escapeHTML(area.name)}">Rename</button><button class="btn btn-secondary" type="button" data-olex-toggle="${escapeHTML(area.name)}" data-enable="${area.disabled ? '1' : '0'}">${area.disabled ? 'Enable' : 'Disable'}</button><button class="btn btn-danger" type="button" data-olex-remove="${escapeHTML(area.name)}">Remove</button></div>`;
      list.appendChild(row);
    }
    if ($('olexStatusTitle')) $('olexStatusTitle').textContent = `${enabled} active OLEX database${enabled === 1 ? '' : 's'}`;
    if ($('olexStatusDetail')) $('olexStatusDetail').textContent = `${formatStorage(total)} stored persistently. Disabled databases remain available but are excluded from route analysis.`;
  }

  async function refreshPersistentStatus() {
    const status = await api('/api/status');
    const rtzAreas = status.rtzAreas || [];
    const olexAreas = status.olexAreas || [];
    if ($('rtzFileCount')) $('rtzFileCount').textContent = Number(status.counts?.rtzFiles || rtzAreas.length).toLocaleString();
    if ($('routeCount')) $('routeCount').textContent = Number(status.meta?.cleaned_routes || status.meta?.route_count || status.counts?.routeNodes || 0).toLocaleString();
    if ($('destinationCount')) $('destinationCount').textContent = Number(status.counts?.destinations || 0).toLocaleString();
    if ($('olexStorageSize')) $('olexStorageSize').textContent = formatStorage(status.olexStorageBytes);
    if ($('rtzLibraryPath')) $('rtzLibraryPath').textContent = status.rtzLibraryPath || 'persistent local application-data folder';
    if ($('olexLibraryPath')) $('olexLibraryPath').textContent = status.olexLibraryPath || 'persistent local application-data folder';
    renderRTZAreas(rtzAreas);
    renderOlexAreas(olexAreas);
    return status;
  }

  async function watchOlexJob(jobId, label) {
    const message = $('importStatus');
    for (;;) {
      const job = await api(`/api/olex/import/status?id=${encodeURIComponent(jobId)}`);
      const pct = Math.max(0, Math.min(100, Number(job.progress || 0) * 100));
      const bytes = Number(job.bytesRead || 0);
      const total = Number(job.totalBytes || 0);
      const detail = `${total ? `${formatStorage(bytes)} of ${formatStorage(total)}` : 'Working locally'}${job.validRows ? ` · ${Number(job.validRows).toLocaleString()} valid rows` : ''}${job.tilesTotal ? ` · tiles ${Number(job.tilesDone || 0).toLocaleString()}/${Number(job.tilesTotal).toLocaleString()}` : ''}${job.detail ? ` · ${job.detail}` : ''}`;
      setOlexProgress(pct, job.phase || `Indexing ${label}`, detail);
      setMessage(message, `${job.phase || 'Indexing'} — ${pct.toFixed(1)}%`);
      if (job.status === 'failed') throw new Error(job.error || 'OLEX import failed');
      if (job.status === 'complete') {
        setOlexProgress(100, 'OLEX database ready', `${label} is indexed and stored persistently. It will reload automatically after restart.`);
        setMessage(message, job.message || `${label} is stored and ready.`);
        await refreshPersistentStatus();
        return;
      }
      await sleep(1200);
    }
  }

  async function uploadOlexFiles(files) {
    const selected = [...files];
    if (!selected.length) return;
    const message = $('importStatus');
    for (let index = 0; index < selected.length; index++) {
      const file = selected[index];
      const name = file.name.replace(/\.olxidx\.gz$|\.gz$/i, '') || 'OLEX Database';
      try {
        const result = await resumableUpload(file, 'olex', name, (offset, total, resumed) => {
          const pct = total ? offset / total * 100 : 0;
          setOlexProgress(pct, `Uploading ${file.name} (${index + 1}/${selected.length})`, `${formatStorage(offset)} of ${formatStorage(total)} stored${resumed ? ' · resumed from the previous saved offset' : ''}. Indexing starts after upload reaches 100%.`);
          setMessage(message, `Uploading ${file.name} — ${pct.toFixed(1)}%`);
        });
        await watchOlexJob(result.id, name);
      } catch (error) {
        setMessage(message, `${file.name}: ${error.message}. Reselect the same file to resume a saved partial upload.`, 'error');
        setOlexProgress(0, 'OLEX upload paused', `${error.message}. The partial upload remains on disk unless you replace or cancel it.`);
        break;
      }
    }
  }

  async function uploadRTZFiles(files) {
    const selected = [...files];
    if (!selected.length) return;
    const message = $('rtzImportStatus');
    let imported = 0;
    for (let index = 0; index < selected.length; index++) {
      const file = selected[index];
      const name = file.name.replace(/\.(rtz|xml)$/i, '') || 'Imported RTZ';
      try {
        const result = await resumableUpload(file, 'rtz', name, (offset, total, resumed) => {
          const pct = total ? offset / total * 100 : 0;
          setRTZProgress(pct, `Uploading ${file.name} (${index + 1}/${selected.length})`, `${formatStorage(offset)} of ${formatStorage(total)} stored${resumed ? ' · resumed from the previous saved offset' : ''}. The route engine rebuilds after this file is validated.`);
          setMessage(message, `Uploading ${file.name} — ${pct.toFixed(1)}%`);
        });
        imported++;
        setRTZProgress(100, `${result.area?.name || name} ready`, 'Stored persistently and merged into the historical route engine.');
        setMessage(message, `${result.area?.name || file.name} imported. Rebuilding route counts…`);
        await refreshPersistentStatus();
      } catch (error) {
        setMessage(message, `${file.name}: ${error.message}`, 'error');
      }
    }
    if (imported > 0) setMessage(message, `${imported} RTZ file${imported === 1 ? '' : 's'} stored persistently and active after restart.`);
  }

  async function importRTZPath() {
    const path = prompt('Paste the full Windows path to an RTZ file or a folder containing RTZ files. Example:\nD:\\Historical RTZ Routes');
    if (!path || !path.trim()) return;
    const message = $('rtzImportStatus');
    setMessage(message, 'Reading RTZ files directly from the local path and rebuilding the corridor engine…');
    setRTZProgress(10, 'Importing RTZ folder by path', 'Files are copied into the persistent RTZ library.');
    try {
      const result = await api('/api/rtz/import-path', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({path: path.trim().replace(/^"|"$/g, '')})
      });
      const count = (result.imported || []).length;
      const failures = result.failures || [];
      setRTZProgress(100, `${count} RTZ file${count === 1 ? '' : 's'} imported`, failures.length ? `${failures.length} file${failures.length === 1 ? '' : 's'} could not be imported. Check the message below.` : 'All files are stored persistently and active.');
      setMessage(message, failures.length ? `${count} imported. ${failures.join(' | ')}` : `${count} RTZ file${count === 1 ? '' : 's'} stored persistently.` , failures.length ? 'error' : '');
      await refreshPersistentStatus();
    } catch (error) {
      setMessage(message, error.message, 'error');
      setRTZProgress(0, 'RTZ path import failed', error.message);
    }
  }

  async function renameRTZ(id, currentName) {
    const next = prompt('Rename RTZ library entry:', currentName || '');
    if (!next || !next.trim() || next.trim() === currentName) return;
    await api('/api/managed/rtz/rename', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({id, newName: next.trim()})
    });
    await refreshPersistentStatus();
  }

  async function toggleRTZ(id, enabled) {
    setMessage($('rtzImportStatus'), `${enabled ? 'Enabling' : 'Disabling'} RTZ file and rebuilding the historical corridor graph…`);
    await api('/api/managed/rtz/toggle', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({id, enabled})
    });
    await refreshPersistentStatus();
    setMessage($('rtzImportStatus'), `RTZ file ${enabled ? 'enabled' : 'disabled'} persistently.`);
  }

  async function removeRTZ(id, name) {
    if (!confirm(`Permanently remove ${name || 'this RTZ file'} from the persistent route library?`)) return;
    setMessage($('rtzImportStatus'), 'Removing RTZ file and rebuilding the historical corridor graph…');
    await api(`/api/managed/rtz/remove?id=${encodeURIComponent(id)}`, {method: 'DELETE'});
    await refreshPersistentStatus();
    setMessage($('rtzImportStatus'), `${name || 'RTZ file'} removed.`);
  }

  function wirePersistentLibraries() {
    const rtzFile = $('rtzFile');
    if (rtzFile) {
      rtzFile.addEventListener('change', event => {
        const files = [...event.target.files];
        event.target.value = '';
        uploadRTZFiles(files).catch(error => setMessage($('rtzImportStatus'), error.message, 'error'));
      });
    }

    const olexFile = $('olexFile');
    if (olexFile) {
      olexFile.multiple = true;
      olexFile.addEventListener('change', event => {
        // Capture-phase interception prevents the legacy single-request upload handler
        // from duplicating very large files. This path is chunked and restart-resumable.
        event.stopImmediatePropagation();
        const files = [...event.target.files];
        event.target.value = '';
        uploadOlexFiles(files).catch(error => setMessage($('importStatus'), error.message, 'error'));
      }, true);
    }

    const openRTZ = async () => {
      try {
        await refreshPersistentStatus();
        $('rtzDialog')?.showModal();
      } catch (error) {
        alert(error.message);
      }
    };

    $('manageRTZ')?.addEventListener('click', openRTZ);
    $('openRTZHeader')?.addEventListener('click', openRTZ);
    $('dialogBrowseRTZ')?.addEventListener('click', () => $('rtzFile')?.click());
    $('dialogPathRTZ')?.addEventListener('click', () => importRTZPath());

    $('rtzAreaList')?.addEventListener('click', event => {
      const target = event.target;
      const rename = target.dataset.rtzRename;
      const toggle = target.dataset.rtzToggle;
      const remove = target.dataset.rtzRemove;
      const name = target.dataset.rtzName || '';
      const enable = target.dataset.enable === '1';
      const task = rename ? renameRTZ(rename, name) : toggle ? toggleRTZ(toggle, enable) : remove ? removeRTZ(remove, name) : null;
      if (task) task.catch(error => setMessage($('rtzImportStatus'), error.message, 'error'));
    });
  }

  wirePersistentLibraries();
  refreshPersistentStatus().catch(() => {});
})();
