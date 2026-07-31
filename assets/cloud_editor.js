(() => {
  'use strict';

  const $ = (id) => document.getElementById(id);
  const fetcher = (resource, options = {}) =>
    (window.lrpFetch || window.fetch.bind(window))(resource, options);

  let current = null;
  let context = null;
  let view = null;
  let selected = -1;
  let dragIndex = null;
  let panning = null;
  let dragSnapshotTaken = false;
  let undoStack = [];
  let redoStack = [];
  let dirty = false;
  let saveTimer = null;

  async function api(url, options = {}) {
    const response = await fetcher(url, options);
    const contentType = response.headers.get('content-type') || '';
    const data = contentType.includes('json') ? await response.json() : await response.text();
    if (!response.ok) {
      throw new Error(data?.error || `Request failed (${response.status})`);
    }
    return data;
  }

  function esc(value) {
    return String(value ?? '').replace(/[&<>"']/g, (char) => ({
      '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
    }[char]));
  }

  function num(value, fallback = 0) {
    const parsed = Number(value);
    return Number.isFinite(parsed) ? parsed : fallback;
  }

  function canEdit() {
    return window.lrpMe?.role !== 'viewer';
  }

  function applyRole(account = window.lrpMe) {
    if (!account || account.role !== 'viewer') return;
    ['editCloudRoute', 'generateRoute', 'dialogBrowseOlex', 'dialogBrowseRTZ', 'saveCloudRoute', 'saveEnterprisePreview', 'addCloudWaypoint'].forEach((id) => {
      const element = $(id);
      if (element) {
        element.disabled = true;
        element.title = 'This account is read-only';
      }
    });
    document.body.classList.add('enterprise-viewer-mode');
  }

  function fmt(value, digits = 1) {
    return Number.isFinite(Number(value)) ? Number(value).toFixed(digits) : '—';
  }

  function routeId() {
    const href = $('exportRTZ')?.getAttribute('href') || '';
    try {
      return new URL(href, location.href).searchParams.get('id');
    } catch (_) {
      return null;
    }
  }

  function normalizeWaypoint(w, index) {
    return {
      name: String(w?.name || `WP${String(index + 1).padStart(3, '0')}`),
      lat: num(w?.lat),
      lon: num(w?.lon),
      radiusNM: num(w?.radiusNM, 0.5),
      portsideXTDNM: num(w?.portsideXTDNM, 0.1),
      starboardXTDNM: num(w?.starboardXTDNM, 0.1),
      wheelOverNM: num(w?.wheelOverNM),
      speedKn: num(w?.speedKn),
      geometryType: String(w?.geometryType || 'Loxodrome'),
      remarks: String(w?.remarks || ''),
      leg: w?.leg || null
    };
  }

  function normalizePlan(plan) {
    plan.waypoints = (plan.waypoints || []).map(normalizeWaypoint);
    return plan;
  }

  async function loadRoute(id = routeId()) {
    if (!id) throw new Error('Generate or open a route first.');
    return normalizePlan(await api(`/api/route/get?id=${encodeURIComponent(id)}`));
  }

  function openPlan(plan) {
    current = normalizePlan(plan);
    window.lrpOpenPlan?.(current);
    const jsonLink = $('exportJSON');
    if (jsonLink) {
      jsonLink.href = `/api/download/json?id=${encodeURIComponent(current.id)}`;
      jsonLink.classList.remove('disabled');
      jsonLink.setAttribute('aria-disabled', 'false');
    }
    const edit = $('editCloudRoute');
    if (edit) edit.disabled = !canEdit();
    updatePreviewMetrics();
  }

  function snapshot() {
    return JSON.stringify({
      routeName: current?.routeName || '',
      waypoints: current?.waypoints || []
    });
  }

  function restore(serialized) {
    if (!current || !serialized) return;
    const state = JSON.parse(serialized);
    current.routeName = state.routeName;
    current.waypoints = state.waypoints.map(normalizeWaypoint);
    selected = Math.min(Math.max(selected, 0), current.waypoints.length - 1);
    dirty = true;
    renderEditorRows();
    renderInspector();
    draw();
    updatePreviewMetrics();
  }

  function pushUndo() {
    if (!current) return;
    const state = snapshot();
    if (undoStack[undoStack.length - 1] !== state) {
      undoStack.push(state);
      if (undoStack.length > 80) undoStack.shift();
    }
    redoStack = [];
    updateHistoryButtons();
  }

  function undo() {
    if (!undoStack.length) return;
    redoStack.push(snapshot());
    restore(undoStack.pop());
    updateHistoryButtons();
  }

  function redo() {
    if (!redoStack.length) return;
    undoStack.push(snapshot());
    restore(redoStack.pop());
    updateHistoryButtons();
  }

  function updateHistoryButtons() {
    if ($('enterpriseUndo')) $('enterpriseUndo').disabled = undoStack.length === 0;
    if ($('enterpriseRedo')) $('enterpriseRedo').disabled = redoStack.length === 0;
  }

  function markDirty(message = 'Unsaved waypoint changes') {
    dirty = true;
    const status = $('enterpriseSaveStatus');
    if (status) {
      status.textContent = message;
      status.className = 'enterprise-status warning';
    }
    updatePreviewMetrics();
  }

  function serializeWaypoints() {
    return current.waypoints.map((w) => ({
      name: w.name,
      lat: num(w.lat),
      lon: num(w.lon),
      radiusNM: num(w.radiusNM),
      portsideXTDNM: num(w.portsideXTDNM),
      starboardXTDNM: num(w.starboardXTDNM),
      wheelOverNM: num(w.wheelOverNM),
      speedKn: num(w.speedKn),
      geometryType: w.geometryType || 'Loxodrome',
      remarks: w.remarks || ''
    }));
  }

  async function saveRoute(options = {}) {
    if (!current) return null;
    clearTimeout(saveTimer);
    const status = $('enterpriseSaveStatus');
    if (status) {
      status.textContent = 'Saving and recalculating OLEX support…';
      status.className = 'enterprise-status working';
    }
    const updated = normalizePlan(await api('/api/route/update', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        id: current.id,
        routeName: current.routeName,
        waypoints: serializeWaypoints()
      })
    }));
    current = updated;
    dirty = false;
    openPlan(updated);
    if ($('previewDialog')?.open) {
      context = await api(`/api/preview/context?id=${encodeURIComponent(current.id)}`);
    }
    renderEditorRows();
    renderInspector();
    draw();
    if (status) {
      status.textContent = `Saved · revision ${updated.revision || 1}`;
      status.className = 'enterprise-status saved';
    }
    if (options.closeEditor) $('cloudEditorDialog')?.close();
    return updated;
  }

  function scheduleAutosave() {
    clearTimeout(saveTimer);
    if (!$('enterpriseAutosave')?.checked) return;
    saveTimer = setTimeout(() => saveRoute().catch(showError), 700);
  }

  function showError(error) {
    const status = $('enterpriseSaveStatus') || $('previewLoading');
    if (status) {
      status.textContent = error.message;
      status.className = 'enterprise-status error';
    } else {
      alert(error.message);
    }
  }

  function setupMarkup() {
    document.title = 'Lindblad Route Planner Cloud';
    const title = document.querySelector('.brand-block h1');
    const subtitle = document.querySelector('.brand-block p');
    if (title) title.textContent = 'Lindblad Route Planner Cloud';
    if (subtitle) subtitle.textContent = 'Secure cloud workspaces · Historical RTZ corridors · OLEX route editing';
    const eyebrow = document.querySelector('.brand-block .eyebrow');
    if (eyebrow) eyebrow.textContent = 'LINDBLAD EXPEDITIONS · ENTERPRISE ROUTE INTELLIGENCE';

    const close = $('closeApp');
    if (close && window.lrpFetch) close.textContent = 'Sign out';

    const header = document.querySelector('.topbar');
    if (header && !$('openRouteLibrary')) {
      const button = document.createElement('button');
      button.id = 'openRouteLibrary';
      button.className = 'btn btn-header enterprise-library-button';
      button.type = 'button';
      button.textContent = 'Saved Routes';
      header.insertBefore(button, close || null);
    }

    const exports = document.querySelector('.export-actions');
    if (exports && !$('exportJSON')) {
      const link = document.createElement('a');
      link.id = 'exportJSON';
      link.className = 'btn btn-export-alt disabled';
      link.href = '#';
      link.setAttribute('aria-disabled', 'true');
      link.textContent = 'Export Route JSON';
      exports.appendChild(link);
    }

    const editor = $('cloudEditorDialog');
    if (editor) {
      editor.classList.add('enterprise-editor-dialog');
      editor.innerHTML = `
        <div class="dialog-header enterprise-dialog-header">
          <div><span class="section-kicker">ENTERPRISE WAYPOINT EDITOR</span><h2>Edit the complete route</h2>
          <p>All changes are persisted in the organization workspace and recalculated against the active OLEX and RTZ libraries.</p></div>
          <button class="icon-button" id="closeCloudEditor" aria-label="Close">×</button>
        </div>
        <div class="enterprise-editor-toolbar">
          <label class="enterprise-route-name">Route name<input id="cloudRouteName" maxlength="160"></label>
          <button id="addCloudWaypoint" class="btn btn-secondary" type="button">Insert before arrival</button>
          <button id="openEnterprisePreview" class="btn btn-secondary" type="button">Open visual editor</button>
          <button id="saveCloudRoute" class="btn btn-primary" type="button">Save and recalculate</button>
          <span id="cloudEditorStatus" class="enterprise-status"></span>
        </div>
        <div class="table-wrap enterprise-table-wrap">
          <table class="cloud-edit-table enterprise-edit-table">
            <thead><tr><th>#</th><th>Name</th><th>Latitude</th><th>Longitude</th><th>Turn radius NM</th><th>XTD port NM</th><th>XTD starboard NM</th><th>Leg speed kn</th><th>Wheel-over NM</th><th>Geometry</th><th>Remarks</th><th></th></tr></thead>
            <tbody id="cloudWaypointRows"></tbody>
          </table>
        </div>`;
    }

    const preview = $('previewDialog');
    if (preview) {
      preview.classList.add('enterprise-preview-dialog');
      preview.innerHTML = `
        <div class="dialog-header enterprise-dialog-header">
          <div><span class="section-kicker">VISUAL ROUTE REVIEW</span><h2 id="previewTitle">Route editor</h2>
          <p>Zoom, pan and drag waypoints directly over OLEX-derived depth traces and historical RTZ corridors.</p></div>
          <button class="icon-button" id="closeEnterprisePreview" aria-label="Close">×</button>
        </div>
        <div class="enterprise-preview-toolbar">
          <button id="zoomInRoute" class="btn btn-secondary" type="button">Zoom in</button>
          <button id="zoomOutRoute" class="btn btn-secondary" type="button">Zoom out</button>
          <button id="fitRoute" class="btn btn-secondary" type="button">Fit route</button>
          <button id="enterpriseUndo" class="btn btn-secondary" type="button" disabled>Undo</button>
          <button id="enterpriseRedo" class="btn btn-secondary" type="button" disabled>Redo</button>
          <label class="enterprise-toggle"><input id="showOlexContext" type="checkbox" checked> OLEX depth traces</label>
          <label class="enterprise-toggle"><input id="showHistoricalContext" type="checkbox" checked> Historical RTZ tracks</label>
          <label class="enterprise-toggle"><input id="showXTDContext" type="checkbox" checked> XTD corridor</label>
          <label class="enterprise-toggle"><input id="showWaypointLabels" type="checkbox" checked> Labels</label>
          <label class="enterprise-toggle"><input id="enterpriseAutosave" type="checkbox" checked> Autosave after drag</label>
          <button id="saveEnterprisePreview" class="btn btn-primary" type="button">Save route</button>
          <span id="enterpriseSaveStatus" class="enterprise-status"></span>
        </div>
        <div class="enterprise-preview-layout">
          <div class="enterprise-map-column">
            <div class="preview-wrap enterprise-map-wrap"><svg id="routeSVG" role="img" aria-label="Editable route over OLEX and historical route context"></svg></div>
            <div class="enterprise-map-help">Mouse wheel: zoom · drag background: pan · drag a waypoint: reposition · double-click a route leg: insert waypoint</div>
          </div>
          <aside class="enterprise-inspector">
            <div id="enterpriseMetrics" class="enterprise-metrics"></div>
            <div id="enterpriseWaypointInspector" class="enterprise-waypoint-inspector"><p>Select a waypoint to edit its properties.</p></div>
          </aside>
        </div>
        <div class="bar-legend preview-legend enterprise-legend">
          <span><i class="line-key supported"></i>OLEX supported</span><span><i class="line-key review"></i>Officer review</span><span><i class="line-key unsupported"></i>Critical unsupported</span>
          <span><i class="depth-key shallow"></i>&lt;50 m</span><span><i class="depth-key medium"></i>50–100 m</span><span><i class="depth-key deep"></i>&gt;100 m</span>
        </div>
        <p class="dialog-note"><strong>Planning aid:</strong> OLEX-derived traces and historical tracks are contextual evidence only. Final route approval still requires approved ENC/ECDIS, UKC/XTD, SMS and bridge-team checks.</p>`;
    }

    if (!$('routeLibraryDialog')) {
      const dialog = document.createElement('dialog');
      dialog.id = 'routeLibraryDialog';
      dialog.className = 'wide-dialog enterprise-library-dialog';
      dialog.innerHTML = `
        <div class="dialog-header enterprise-dialog-header"><div><span class="section-kicker">ORGANIZATION ROUTE LIBRARY</span><h2>Saved route plans</h2><p>Open, duplicate or remove routes stored in this workspace.</p></div><button class="icon-button" id="closeRouteLibrary" aria-label="Close">×</button></div>
        <div class="enterprise-library-toolbar"><button id="refreshRouteLibrary" class="btn btn-secondary" type="button">Refresh</button><span id="routeLibraryStatus" class="enterprise-status"></span></div>
        <div id="routeLibraryList" class="enterprise-route-list"></div>`;
      document.body.appendChild(dialog);
    }
  }

  function renderEditorRows() {
    if (!current || !$('cloudWaypointRows')) return;
    $('cloudRouteName').value = current.routeName || '';
    const body = $('cloudWaypointRows');
    body.innerHTML = '';
    current.waypoints.forEach((w, index) => {
      const row = document.createElement('tr');
      row.dataset.index = index;
      row.innerHTML = `
        <td><strong>${index + 1}</strong></td>
        <td><input data-key="name" value="${esc(w.name)}" maxlength="128"></td>
        <td><input data-key="lat" type="number" step="0.000001" value="${fmt(w.lat, 6)}"></td>
        <td><input data-key="lon" type="number" step="0.000001" value="${fmt(w.lon, 6)}"></td>
        <td><input data-key="radiusNM" type="number" min="0" max="20" step="0.01" value="${fmt(w.radiusNM, 2)}"></td>
        <td><input data-key="portsideXTDNM" type="number" min="0" max="20" step="0.01" value="${fmt(w.portsideXTDNM, 2)}"></td>
        <td><input data-key="starboardXTDNM" type="number" min="0" max="20" step="0.01" value="${fmt(w.starboardXTDNM, 2)}"></td>
        <td><input data-key="speedKn" type="number" min="0" max="60" step="0.1" value="${fmt(w.speedKn, 1)}" title="0 uses route average speed"></td>
        <td><input data-key="wheelOverNM" type="number" min="0" max="20" step="0.01" value="${fmt(w.wheelOverNM, 2)}"></td>
        <td><select data-key="geometryType"><option ${w.geometryType === 'Loxodrome' ? 'selected' : ''}>Loxodrome</option><option ${w.geometryType === 'Orthodrome' ? 'selected' : ''}>Orthodrome</option></select></td>
        <td><textarea data-key="remarks" rows="2" maxlength="2000">${esc(w.remarks)}</textarea></td>
        <td class="enterprise-row-actions"><button class="btn btn-ghost" data-insert="${index}" type="button">Insert after</button><button class="btn btn-ghost" data-remove="${index}" type="button" ${current.waypoints.length <= 2 ? 'disabled' : ''}>Remove</button></td>`;
      row.querySelectorAll('input,select,textarea').forEach((input) => {
        input.addEventListener('focus', () => { selected = index; });
        input.addEventListener('change', () => {
          pushUndo();
          const key = input.dataset.key;
          current.waypoints[index][key] = ['name', 'geometryType', 'remarks'].includes(key) ? input.value : num(input.value);
          markDirty();
        });
      });
      body.appendChild(row);
    });
  }

  function midpoint(a, b) {
    return {
      name: `WP${String(current.waypoints.length + 1).padStart(3, '0')}`,
      lat: (num(a.lat) + num(b.lat)) / 2,
      lon: (num(a.lon) + num(b.lon)) / 2,
      radiusNM: (num(a.radiusNM, 0.5) + num(b.radiusNM, 0.5)) / 2,
      portsideXTDNM: num(a.portsideXTDNM, 0.1),
      starboardXTDNM: num(a.starboardXTDNM, 0.1),
      wheelOverNM: num(a.wheelOverNM),
      speedKn: num(a.speedKn),
      geometryType: a.geometryType || 'Loxodrome',
      remarks: ''
    };
  }

  function insertAfter(index, point = null) {
    if (!current || current.waypoints.length >= 2000) return;
    pushUndo();
    const a = current.waypoints[Math.max(0, index)];
    const b = current.waypoints[Math.min(current.waypoints.length - 1, index + 1)] || a;
    const wp = midpoint(a, b);
    if (point) {
      wp.lat = point.lat;
      wp.lon = point.lon;
    }
    current.waypoints.splice(index + 1, 0, wp);
    selected = index + 1;
    markDirty('Waypoint inserted');
    renderEditorRows();
    renderInspector();
    draw();
  }

  function removeWaypoint(index) {
    if (!current || current.waypoints.length <= 2) return;
    pushUndo();
    current.waypoints.splice(index, 1);
    selected = Math.min(index, current.waypoints.length - 1);
    markDirty('Waypoint removed');
    renderEditorRows();
    renderInspector();
    draw();
  }

  async function openEditor() {
    try {
      current = await loadRoute();
      selected = 0;
      undoStack = [];
      redoStack = [];
      dirty = false;
      renderEditorRows();
      $('cloudEditorStatus').textContent = `Revision ${current.revision || 1} · ${current.waypoints.length} waypoints`;
      $('cloudEditorDialog').showModal();
    } catch (error) {
      alert(error.message);
    }
  }

  function routeBounds(points) {
    let minLat = 90, maxLat = -90, minLon = 180, maxLon = -180;
    points.forEach((point) => {
      minLat = Math.min(minLat, num(point.lat));
      maxLat = Math.max(maxLat, num(point.lat));
      minLon = Math.min(minLon, num(point.lon));
      maxLon = Math.max(maxLon, num(point.lon));
    });
    const dy = Math.max(0.01, maxLat - minLat);
    const dx = Math.max(0.01, maxLon - minLon);
    return { minLat: minLat - dy * 0.2, maxLat: maxLat + dy * 0.2, minLon: minLon - dx * 0.2, maxLon: maxLon + dx * 0.2 };
  }

  function fit() {
    if (!current?.waypoints?.length) return;
    const bounds = routeBounds(current.waypoints);
    view = { x: bounds.minLon, y: bounds.minLat, w: bounds.maxLon - bounds.minLon, h: bounds.maxLat - bounds.minLat };
    draw();
  }

  const mapWidth = 1200;
  const mapHeight = 720;
  function sx(lon) { return (lon - view.x) / view.w * mapWidth; }
  function sy(lat) { return (view.y + view.h - lat) / view.h * mapHeight; }
  function lonAt(x) { return view.x + x / mapWidth * view.w; }
  function latAt(y) { return view.y + view.h - y / mapHeight * view.h; }

  function svgElement(name, attributes = {}) {
    const element = document.createElementNS('http://www.w3.org/2000/svg', name);
    Object.entries(attributes).forEach(([key, value]) => element.setAttribute(key, value));
    return element;
  }

  function drawGrid(svg) {
    const count = 10;
    for (let i = 1; i < count; i += 1) {
      svg.appendChild(svgElement('line', { x1: i * mapWidth / count, y1: 0, x2: i * mapWidth / count, y2: mapHeight, class: 'enterprise-grid-line' }));
      svg.appendChild(svgElement('line', { x1: 0, y1: i * mapHeight / count, x2: mapWidth, y2: i * mapHeight / count, class: 'enterprise-grid-line' }));
    }
  }

  function drawHistorical(svg) {
    if (!$('showHistoricalContext')?.checked || !context?.historicalSegments) return;
    const group = svgElement('g', { class: 'historical-context-layer' });
    context.historicalSegments.forEach((segment) => {
      if (Math.max(segment.lon1, segment.lon2) < view.x || Math.min(segment.lon1, segment.lon2) > view.x + view.w || Math.max(segment.lat1, segment.lat2) < view.y || Math.min(segment.lat1, segment.lat2) > view.y + view.h) return;
      const opacity = Math.min(0.5, 0.08 + num(segment.consensus) * 0.025);
      group.appendChild(svgElement('line', {
        x1: sx(segment.lon1), y1: sy(segment.lat1), x2: sx(segment.lon2), y2: sy(segment.lat2),
        class: 'historical-track-line', opacity
      }));
    });
    svg.appendChild(group);
  }

  function drawOlex(svg) {
    if (!$('showOlexContext')?.checked || !context?.olexCells) return;
    const visible = context.olexCells.filter((cell) => cell.lon >= view.x && cell.lon <= view.x + view.w && cell.lat >= view.y && cell.lat <= view.y + view.h);
    const rows = new Map();
    visible.forEach((cell) => {
      const key = Math.round(cell.lat * 1500);
      if (!rows.has(key)) rows.set(key, []);
      rows.get(key).push(cell);
    });
    const group = svgElement('g', { class: 'olex-context-layer' });
    let lines = 0;
    for (const row of rows.values()) {
      row.sort((a, b) => a.lon - b.lon);
      for (let i = 1; i < row.length && lines < 14000; i += 1) {
        const a = row[i - 1], b = row[i];
        if (Math.abs(b.lon - a.lon) > Math.max(0.015, view.w / 70)) continue;
        const depth = (num(a.meanDepth) + num(b.meanDepth)) / 2;
        const depthClass = depth < 50 ? 'olex-shallow' : depth < 100 ? 'olex-medium' : 'olex-deep';
        group.appendChild(svgElement('line', { x1: sx(a.lon), y1: sy(a.lat), x2: sx(b.lon), y2: sy(b.lat), class: `olex-trace ${depthClass}` }));
        lines += 1;
      }
    }
    if (view.w < 0.25 && visible.length < 5000) {
      visible.forEach((cell) => {
        const depth = num(cell.meanDepth);
        const depthClass = depth < 50 ? 'olex-shallow-fill' : depth < 100 ? 'olex-medium-fill' : 'olex-deep-fill';
        group.appendChild(svgElement('circle', { cx: sx(cell.lon), cy: sy(cell.lat), r: 1.4, class: depthClass, opacity: 0.55 }));
      });
    }
    svg.appendChild(group);
  }

  function routeStatusClass(status) {
    return status === 'SUPPORTED' ? 'enterprise-route-supported' : status === 'UNSUPPORTED' ? 'enterprise-route-unsupported' : 'enterprise-route-review';
  }

  function drawRoute(svg) {
    if (!current) return;
    const routeGroup = svgElement('g', { class: 'enterprise-route-layer' });
    for (let i = 0; i < current.waypoints.length - 1; i += 1) {
      const a = current.waypoints[i];
      const b = current.waypoints[i + 1];
      if ($('showXTDContext')?.checked) {
        const xtd = Math.max(num(a.portsideXTDNM, 0.1), num(a.starboardXTDNM, 0.1));
        const pixelsPerNM = mapWidth / Math.max(0.01, view.w * 60 * Math.max(0.1, Math.cos((a.lat + b.lat) * Math.PI / 360)));
        const width = Math.max(5, Math.min(80, xtd * 2 * pixelsPerNM));
        routeGroup.appendChild(svgElement('line', { x1: sx(a.lon), y1: sy(a.lat), x2: sx(b.lon), y2: sy(b.lat), class: 'enterprise-xtd-band', 'stroke-width': width }));
      }
      const segments = a.leg?.supportSegments?.length ? a.leg.supportSegments : [{ startFraction: 0, endFraction: 1, status: a.leg?.status || 'OFFICER CHECK' }];
      segments.forEach((segment) => {
        const start = num(segment.startFraction);
        const end = num(segment.endFraction, 1);
        const lon1 = a.lon + (b.lon - a.lon) * start;
        const lat1 = a.lat + (b.lat - a.lat) * start;
        const lon2 = a.lon + (b.lon - a.lon) * end;
        const lat2 = a.lat + (b.lat - a.lat) * end;
        routeGroup.appendChild(svgElement('line', { x1: sx(lon1), y1: sy(lat1), x2: sx(lon2), y2: sy(lat2), class: `enterprise-route-leg ${routeStatusClass(segment.status)}`, 'data-leg': i }));
      });
    }
    svg.appendChild(routeGroup);

    const waypointGroup = svgElement('g', { class: 'enterprise-waypoint-layer' });
    current.waypoints.forEach((point, index) => {
      const circle = svgElement('circle', {
        cx: sx(point.lon), cy: sy(point.lat), r: index === selected ? 10 : (index === 0 || index === current.waypoints.length - 1 ? 8 : 6),
        class: `enterprise-waypoint ${index === selected ? 'selected' : ''}`, 'data-index': index, tabindex: 0
      });
      waypointGroup.appendChild(circle);
      if ($('showWaypointLabels')?.checked) {
        const label = svgElement('text', { x: sx(point.lon) + 10, y: sy(point.lat) - 10, class: 'enterprise-waypoint-label' });
        label.textContent = point.name;
        waypointGroup.appendChild(label);
      }
    });
    svg.appendChild(waypointGroup);
  }

  function draw() {
    if (!current || !view || !$('routeSVG')) return;
    const svg = $('routeSVG');
    svg.setAttribute('viewBox', `0 0 ${mapWidth} ${mapHeight}`);
    svg.innerHTML = '';
    svg.appendChild(svgElement('rect', { x: 0, y: 0, width: mapWidth, height: mapHeight, class: 'enterprise-map-background' }));
    drawGrid(svg);
    drawHistorical(svg);
    drawOlex(svg);
    drawRoute(svg);
  }

  function zoom(factor, centerX = mapWidth / 2, centerY = mapHeight / 2) {
    if (!view) return;
    const lon = lonAt(centerX), lat = latAt(centerY);
    view.w = Math.max(0.0005, Math.min(360, view.w * factor));
    view.h = Math.max(0.0005, Math.min(180, view.h * factor));
    view.x = lon - centerX / mapWidth * view.w;
    view.y = lat - (1 - centerY / mapHeight) * view.h;
    draw();
  }

  function updatePreviewMetrics() {
    if (!$('enterpriseMetrics')) return;
    if (!current) {
      $('enterpriseMetrics').innerHTML = '<p>No route loaded.</p>';
      return;
    }
    const stale = dirty ? '<span class="enterprise-metric-stale">pending recalculation</span>' : '<span class="enterprise-metric-live">recalculated</span>';
    $('enterpriseMetrics').innerHTML = `
      <div class="enterprise-metric-heading"><strong>Route assessment</strong>${stale}</div>
      <div class="enterprise-metric-grid">
        <span>Distance<strong>${fmt(current.distanceNM, 1)} NM</strong></span>
        <span>Waypoints<strong>${current.waypoints.length}</strong></span>
        <span>Corridor centred<strong>${fmt(current.corridorCenteredPct, 1)}%</strong></span>
        <span>OLEX supported<strong>${fmt(current.supportedPct, 1)}%</strong></span>
        <span>Officer review<strong>${fmt(current.reviewPct, 1)}%</strong></span>
        <span>Critical unsupported<strong>${fmt(current.unsupportedPct, 1)}%</strong></span>
        <span>Required SOG<strong>${fmt(current.requiredSpeedKn, 1)} kn</strong></span>
        <span>Revision<strong>${current.revision || 1}</strong></span>
      </div>`;
  }

  function renderInspector() {
    const box = $('enterpriseWaypointInspector');
    if (!box || !current || selected < 0 || !current.waypoints[selected]) {
      if (box) box.innerHTML = '<p>Select a waypoint to edit its properties.</p>';
      return;
    }
    const w = current.waypoints[selected];
    box.innerHTML = `
      <div class="enterprise-inspector-heading"><strong>Waypoint ${selected + 1}</strong><span>${esc(w.name)}</span></div>
      <label>Name<input data-inspector="name" value="${esc(w.name)}"></label>
      <div class="enterprise-inspector-two"><label>Latitude<input data-inspector="lat" type="number" step="0.000001" value="${fmt(w.lat, 6)}"></label><label>Longitude<input data-inspector="lon" type="number" step="0.000001" value="${fmt(w.lon, 6)}"></label></div>
      <div class="enterprise-inspector-two"><label>Turn radius NM<input data-inspector="radiusNM" type="number" min="0" max="20" step="0.01" value="${fmt(w.radiusNM, 2)}"></label><label>Wheel-over NM<input data-inspector="wheelOverNM" type="number" min="0" max="20" step="0.01" value="${fmt(w.wheelOverNM, 2)}"></label></div>
      <div class="enterprise-inspector-two"><label>XTD port NM<input data-inspector="portsideXTDNM" type="number" min="0" max="20" step="0.01" value="${fmt(w.portsideXTDNM, 2)}"></label><label>XTD starboard NM<input data-inspector="starboardXTDNM" type="number" min="0" max="20" step="0.01" value="${fmt(w.starboardXTDNM, 2)}"></label></div>
      <div class="enterprise-inspector-two"><label>Speed to next WP kn<input data-inspector="speedKn" type="number" min="0" max="60" step="0.1" value="${fmt(w.speedKn, 1)}"><small>0 = route average</small></label><label>Geometry<select data-inspector="geometryType"><option ${w.geometryType === 'Loxodrome' ? 'selected' : ''}>Loxodrome</option><option ${w.geometryType === 'Orthodrome' ? 'selected' : ''}>Orthodrome</option></select></label></div>
      <label>Remarks<textarea data-inspector="remarks" rows="4" maxlength="2000">${esc(w.remarks)}</textarea></label>
      <div class="enterprise-inspector-actions"><button id="insertAfterSelected" class="btn btn-secondary" type="button">Insert after</button><button id="removeSelected" class="btn btn-secondary" type="button" ${current.waypoints.length <= 2 ? 'disabled' : ''}>Remove</button><button id="saveSelected" class="btn btn-primary" type="button">Save route</button></div>`;
    box.querySelectorAll('[data-inspector]').forEach((input) => {
      input.addEventListener('change', () => {
        pushUndo();
        const key = input.dataset.inspector;
        w[key] = ['name', 'geometryType', 'remarks'].includes(key) ? input.value : num(input.value);
        markDirty();
        draw();
        renderEditorRows();
      });
    });
    $('insertAfterSelected')?.addEventListener('click', () => insertAfter(selected));
    $('removeSelected')?.addEventListener('click', () => removeWaypoint(selected));
    $('saveSelected')?.addEventListener('click', () => saveRoute().catch(showError));
  }

  async function openPreview(event) {
    event?.preventDefault();
    event?.stopImmediatePropagation();
    try {
      current = await loadRoute();
      selected = Math.min(Math.max(selected, 0), current.waypoints.length - 1);
      undoStack = [];
      redoStack = [];
      dirty = false;
      $('previewTitle').textContent = current.routeName;
      $('previewDialog').showModal();
      $('enterpriseSaveStatus').textContent = 'Loading OLEX and historical corridor context…';
      $('enterpriseSaveStatus').className = 'enterprise-status working';
      context = await api(`/api/preview/context?id=${encodeURIComponent(current.id)}`);
      $('enterpriseSaveStatus').textContent = `Ready · ${context.olexCells?.length || 0} OLEX cells · ${context.historicalSegments?.length || 0} historical segments`;
      $('enterpriseSaveStatus').className = 'enterprise-status saved';
      fit();
      renderInspector();
      updatePreviewMetrics();
      updateHistoryButtons();
    } catch (error) {
      alert(error.message);
    }
  }

  function pointerCoordinates(event, svg) {
    const rect = svg.getBoundingClientRect();
    return {
      x: (event.clientX - rect.left) / rect.width * mapWidth,
      y: (event.clientY - rect.top) / rect.height * mapHeight
    };
  }

  function nearestLeg(x, y) {
    if (!current || current.waypoints.length < 2) return -1;
    let best = -1, bestDistance = Infinity;
    for (let i = 0; i < current.waypoints.length - 1; i += 1) {
      const a = current.waypoints[i], b = current.waypoints[i + 1];
      const ax = sx(a.lon), ay = sy(a.lat), bx = sx(b.lon), by = sy(b.lat);
      const dx = bx - ax, dy = by - ay;
      const lengthSquared = dx * dx + dy * dy || 1;
      const t = Math.max(0, Math.min(1, ((x - ax) * dx + (y - ay) * dy) / lengthSquared));
      const px = ax + t * dx, py = ay + t * dy;
      const distance = Math.hypot(x - px, y - py);
      if (distance < bestDistance) {
        bestDistance = distance;
        best = i;
      }
    }
    return bestDistance <= 35 ? best : -1;
  }

  async function loadRouteLibrary() {
    const status = $('routeLibraryStatus');
    status.textContent = 'Loading saved routes…';
    status.className = 'enterprise-status working';
    try {
      const routes = (await api('/api/routes')).map(normalizePlan);
      const list = $('routeLibraryList');
      list.innerHTML = routes.length ? '' : '<div class="enterprise-empty-library">No saved route plans yet. Generate your first route from the planner.</div>';
      routes.forEach((route) => {
        const card = document.createElement('article');
        card.className = 'enterprise-route-card';
        const updated = route.updatedUTC ? new Date(route.updatedUTC).toLocaleString() : 'Stored route';
        card.innerHTML = `
          <div><h3>${esc(route.routeName)}</h3><p>${fmt(route.distanceNM, 1)} NM · ${route.waypoints.length} WPs · ${fmt(route.supportedPct, 1)}% OLEX support</p><small>Revision ${route.revision || 1} · ${esc(updated)}</small></div>
          <div class="enterprise-route-card-actions"><button class="btn btn-primary" data-open="${esc(route.id)}" type="button">Open</button><button class="btn btn-secondary" data-clone="${esc(route.id)}" type="button">Duplicate</button><button class="btn btn-ghost" data-delete="${esc(route.id)}" type="button">Delete</button></div>`;
        list.appendChild(card);
      });
      status.textContent = `${routes.length} saved route${routes.length === 1 ? '' : 's'}`;
      status.className = 'enterprise-status saved';
    } catch (error) {
      status.textContent = error.message;
      status.className = 'enterprise-status error';
    }
  }

  async function handleLibraryClick(event) {
    const openId = event.target.dataset.open;
    const cloneId = event.target.dataset.clone;
    const deleteId = event.target.dataset.delete;
    try {
      if (openId) {
        const plan = normalizePlan(await api(`/api/route/get?id=${encodeURIComponent(openId)}`));
        openPlan(plan);
        $('routeLibraryDialog').close();
        document.querySelector('#routeSummary')?.scrollIntoView({ behavior: 'smooth', block: 'start' });
      } else if (cloneId) {
        const plan = normalizePlan(await api('/api/route/clone', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id: cloneId }) }));
        openPlan(plan);
        await loadRouteLibrary();
      } else if (deleteId && confirm('Delete this saved route? This cannot be undone.')) {
        await api(`/api/route/delete?id=${encodeURIComponent(deleteId)}`, { method: 'DELETE' });
        if (String(current?.id) === String(deleteId)) current = null;
        await loadRouteLibrary();
      }
    } catch (error) {
      alert(error.message);
    }
  }

  function wire() {
    setupMarkup();
    applyRole();
    window.addEventListener('lrp-account-ready', (event) => applyRole(event.detail));

    $('editCloudRoute')?.addEventListener('click', openEditor);
    $('closeCloudEditor')?.addEventListener('click', () => $('cloudEditorDialog').close());
    $('saveCloudRoute')?.addEventListener('click', () => {
      if (current) current.routeName = $('cloudRouteName').value.trim() || current.routeName;
      saveRoute().catch(showError);
    });
    $('addCloudWaypoint')?.addEventListener('click', () => insertAfter(Math.max(0, current.waypoints.length - 2)));
    $('openEnterprisePreview')?.addEventListener('click', async (event) => {
      try {
        if (current) {
          current.routeName = $('cloudRouteName').value.trim() || current.routeName;
          if (dirty) await saveRoute();
        }
        $('cloudEditorDialog').close();
        await openPreview(event);
      } catch (error) {
        showError(error);
      }
    });
    $('cloudRouteName')?.addEventListener('change', (event) => {
      if (!current) return;
      pushUndo();
      current.routeName = event.target.value.trim() || current.routeName;
      markDirty('Route name changed');
    });
    $('cloudWaypointRows')?.addEventListener('click', (event) => {
      if (event.target.dataset.insert !== undefined) insertAfter(Number(event.target.dataset.insert));
      if (event.target.dataset.remove !== undefined) removeWaypoint(Number(event.target.dataset.remove));
    });

    $('routePreview')?.addEventListener('click', openPreview, true);
    $('closeEnterprisePreview')?.addEventListener('click', () => $('previewDialog').close());
    $('zoomInRoute')?.addEventListener('click', () => zoom(0.7));
    $('zoomOutRoute')?.addEventListener('click', () => zoom(1.4));
    $('fitRoute')?.addEventListener('click', fit);
    $('enterpriseUndo')?.addEventListener('click', undo);
    $('enterpriseRedo')?.addEventListener('click', redo);
    $('saveEnterprisePreview')?.addEventListener('click', () => saveRoute().catch(showError));
    ['showOlexContext', 'showHistoricalContext', 'showXTDContext', 'showWaypointLabels'].forEach((id) => $(id)?.addEventListener('change', draw));

    $('openRouteLibrary')?.addEventListener('click', async () => {
      $('routeLibraryDialog').showModal();
      await loadRouteLibrary();
    });
    $('closeRouteLibrary')?.addEventListener('click', () => $('routeLibraryDialog').close());
    $('refreshRouteLibrary')?.addEventListener('click', loadRouteLibrary);
    $('routeLibraryList')?.addEventListener('click', handleLibraryClick);

    const svg = $('routeSVG');
    svg?.addEventListener('wheel', (event) => {
      if (!current) return;
      event.preventDefault();
      const point = pointerCoordinates(event, svg);
      zoom(event.deltaY < 0 ? 0.8 : 1.25, point.x, point.y);
    }, { passive: false });

    svg?.addEventListener('pointerdown', (event) => {
      if (!current) return;
      const point = pointerCoordinates(event, svg);
      if (event.target.dataset.index !== undefined) {
        selected = Number(event.target.dataset.index);
        dragIndex = selected;
        dragSnapshotTaken = false;
        renderInspector();
        draw();
        event.target.setPointerCapture?.(event.pointerId);
      } else {
        panning = { x: point.x, y: point.y, viewX: view.x, viewY: view.y };
        svg.setPointerCapture?.(event.pointerId);
      }
    });

    svg?.addEventListener('pointermove', (event) => {
      if (!current || !view) return;
      const point = pointerCoordinates(event, svg);
      if (dragIndex !== null) {
        if (!dragSnapshotTaken) {
          pushUndo();
          dragSnapshotTaken = true;
        }
        current.waypoints[dragIndex].lon = lonAt(point.x);
        current.waypoints[dragIndex].lat = latAt(point.y);
        markDirty('Waypoint moved · release to recalculate');
        draw();
        renderInspector();
      } else if (panning) {
        view.x = panning.viewX - (point.x - panning.x) / mapWidth * view.w;
        view.y = panning.viewY + (point.y - panning.y) / mapHeight * view.h;
        draw();
      }
    });

    const endPointer = () => {
      if (dragIndex !== null) {
        dragIndex = null;
        renderEditorRows();
        scheduleAutosave();
      }
      panning = null;
      dragSnapshotTaken = false;
    };
    svg?.addEventListener('pointerup', endPointer);
    svg?.addEventListener('pointercancel', endPointer);

    svg?.addEventListener('dblclick', (event) => {
      if (!current || !view) return;
      event.preventDefault();
      const point = pointerCoordinates(event, svg);
      const leg = nearestLeg(point.x, point.y);
      if (leg >= 0) {
        insertAfter(leg, { lat: latAt(point.y), lon: lonAt(point.x) });
        scheduleAutosave();
      }
    });

    window.addEventListener('keydown', (event) => {
      if (!$('previewDialog')?.open) return;
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'z') {
        event.preventDefault();
        event.shiftKey ? redo() : undo();
      }
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 's') {
        event.preventDefault();
        saveRoute().catch(showError);
      }
      if (event.key === 'Delete' && selected >= 0 && document.activeElement?.tagName !== 'INPUT' && document.activeElement?.tagName !== 'TEXTAREA') {
        removeWaypoint(selected);
      }
    });

    const observer = new MutationObserver(() => {
      const id = routeId();
      if ($('editCloudRoute')) $('editCloudRoute').disabled = !id || !canEdit();
      if (id && $('exportJSON')) {
        $('exportJSON').href = `/api/download/json?id=${encodeURIComponent(id)}`;
        $('exportJSON').classList.remove('disabled');
        $('exportJSON').setAttribute('aria-disabled', 'false');
      }
    });
    observer.observe(document.body, { subtree: true, attributes: true, attributeFilter: ['href'] });
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', wire);
  else wire();
})();
