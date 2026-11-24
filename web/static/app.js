'use strict';

(() => {
  const ACCOUNT_STORAGE_KEY = 'flow-console.account-id';
  const LIVE_REFRESH_MS = 5000;
  const DEFAULT_ACCOUNT = 'default';
  const DEFAULT_QUEUE_CAP = 8192;

  const ALIASES = {
    id: ['id', 'ID'],
    displayName: ['display_name', 'displayName', 'DisplayName'],
    email: ['email', 'Email'],
    version: ['version', 'Version'],
    createdAt: ['created_at', 'createdAt', 'CreatedAt'],
    updatedAt: ['updated_at', 'updatedAt', 'UpdatedAt'],
    name: ['name', 'Name'],
    teamID: ['team_id', 'teamID', 'TeamID'],
    userID: ['user_id', 'userID', 'UserID'],
    role: ['role', 'Role'],
    status: ['status', 'Status'],
    accountID: ['account_id', 'accountID', 'AccountID'],
    balance: ['balance', 'Balance'],
    rateLimitRPM: ['rate_limit_rpm', 'rateLimitRPM', 'RateLimitRPM', 'rpm'],
    amount: ['amount', 'Amount'],
    reason: ['reason', 'Reason'],
    requestID: ['request_id', 'requestID', 'RequestID'],
    occurredAt: ['occurred_at', 'occurredAt', 'OccurredAt'],
    method: ['method', 'Method'],
    path: ['path', 'Path'],
    statusCode: ['status_code', 'statusCode', 'StatusCode'],
    latencyUS: ['latency_us', 'latencyUS', 'LatencyUS'],
  };

  const state = {
    accountId: DEFAULT_ACCOUNT,
    health: null,
    stats: null,
    config: null,
    configLoaded: false,
    routesShape: 'object',
    routeDrafts: [],
    account: null,
    users: [],
    teams: [],
    memberships: [],
    selectedTeamId: '',
    billing: [],
    logs: [],
    liveInFlight: false,
  };

  // -----------------------------------------------------------------------
  // Small utilities
  // -----------------------------------------------------------------------

  const byId = (id) => document.getElementById(id);
  const norm = (value) => String(value ?? '').trim();

  function sanitizeHeaderValue(value) {
    return String(value ?? '').replace(/[\r\n]/g, '').trim();
  }

  function normKey(value) {
    return String(value ?? '').toLowerCase().replace(/[^a-z0-9]/g, '');
  }

  function readField(object, names, fallback) {
    if (!object || typeof object !== 'object' || Array.isArray(object)) return fallback;
    for (const name of names) {
      if (Object.prototype.hasOwnProperty.call(object, name)) {
        const value = object[name];
        if (value !== undefined && value !== null) return value;
      }
    }
    return fallback;
  }

  function field(object, alias, fallback) {
    return readField(object, ALIASES[alias] || [alias], fallback);
  }

  function unwrapObject(raw, keys) {
    if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return {};
    for (const key of keys) {
      const candidate = raw[key];
      if (candidate && typeof candidate === 'object' && !Array.isArray(candidate)) {
        return candidate;
      }
    }
    return raw;
  }

  function rowsFrom(raw, keys) {
    if (Array.isArray(raw)) return raw.filter((row) => row && typeof row === 'object');
    if (!raw || typeof raw !== 'object') return [];
    for (const key of keys) {
      if (Array.isArray(raw[key])) return raw[key].filter((row) => row && typeof row === 'object');
    }
    for (const wrapper of ['data', 'result']) {
      const inner = raw[wrapper];
      if (Array.isArray(inner)) return inner.filter((row) => row && typeof row === 'object');
      if (inner && typeof inner === 'object') {
        for (const key of keys) {
          if (Array.isArray(inner[key])) {
            return inner[key].filter((row) => row && typeof row === 'object');
          }
        }
      }
    }
    return [];
  }

  function flattenLeaves(value, prefix, out) {
    if (Array.isArray(value)) {
      value.forEach((item, index) => flattenLeaves(item, `${prefix}[${index}]`, out));
      return;
    }
    if (value && typeof value === 'object') {
      for (const [key, item] of Object.entries(value)) {
        flattenLeaves(item, prefix ? `${prefix}.${key}` : key, out);
      }
      return;
    }
    if (prefix) out.set(prefix, value);
  }

  function pickNumber(source, names, roots = []) {
    if (!source || typeof source !== 'object') return undefined;
    const leaves = new Map();
    flattenLeaves(source, '', leaves);
    const wanted = new Map(names.map((name, index) => [normKey(name), names.length - index]));
    let best = null;
    for (const [path, value] of leaves) {
      if (value === null || value === undefined || typeof value === 'object') continue;
      const parts = path.split('.');
      const rank = wanted.get(normKey(parts[parts.length - 1]));
      if (rank === undefined) continue;
      let score = rank * 2 - parts.length;
      roots.forEach((root, index) => {
        if (normKey(path).startsWith(normKey(root))) {
          score += (roots.length - index) * 10;
        }
      });
      if (!best || score > best.score) best = { score, value };
    }
    return best ? best.value : undefined;
  }

  function asNumber(value, fallback = 0) {
    const parsed = typeof value === 'number' ? value : Number(value);
    return Number.isFinite(parsed) ? parsed : fallback;
  }

  function displayNumber(value, fallback = '—') {
    if (value === undefined || value === null || value === '') return fallback;
    const parsed = Number(value);
    if (!Number.isFinite(parsed)) return String(value);
    return new Intl.NumberFormat().format(parsed);
  }

  function formatTime(value, fallback = '—') {
    if (!value) return fallback;
    const normalized = String(value).replace(/(\.\d{3})\d+/, '$1');
    const date = new Date(normalized);
    if (Number.isNaN(date.getTime())) return String(value);
    return date.toLocaleString(undefined, {
      year: 'numeric',
      month: 'short',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    });
  }

  function formatClock(date) {
    return date.toLocaleTimeString(undefined, {
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    });
  }

  function formatLatency(microseconds) {
    const value = Number(microseconds);
    if (!Number.isFinite(value)) return '—';
    if (value < 1000) return `${displayNumber(value)} µs`;
    const ms = value / 1000;
    return `${ms.toFixed(ms < 10 ? 2 : 1)} ms`;
  }

  function formatBool(value) {
    if (value === undefined || value === null) return '—';
    return value ? 'Yes' : 'No';
  }

  function parseIntegerValue(value, label, min, max) {
    const raw = String(value ?? '').trim();
    if (!raw) throw new Error(`${label} is required.`);
    const parsed = Number(raw);
    if (!Number.isInteger(parsed)) {
      throw new Error(`${label} must be a whole number.`);
    }
    if (parsed < min) {
      throw new Error(`${label} must be at least ${min}.`);
    }
    if (max !== undefined && parsed > max) {
      throw new Error(`${label} must be at most ${max}.`);
    }
    return parsed;
  }

  function parseLimit(value, fallback = 50) {
    const parsed = Number(String(value ?? '').trim());
    if (!Number.isInteger(parsed) || parsed < 1 || parsed > 1000) return fallback;
    return parsed;
  }

  function statusTone(status) {
    switch (String(status || '').toUpperCase()) {
      case 'ACTIVE':
      case 'APPROVED':
        return 'success';
      case 'PENDING':
        return 'warning';
      case 'LEAVED':
        return 'neutral';
      default:
        return 'info';
    }
  }

  function httpTone(code) {
    const value = Number(code);
    if (!Number.isFinite(value)) return 'neutral';
    if (value >= 500) return 'error';
    if (value >= 400) return 'warn';
    if (value >= 300) return 'info';
    return 'ok';
  }

  // -----------------------------------------------------------------------
  // API client
  // -----------------------------------------------------------------------

  class ApiError extends Error {
    constructor(message, status, payload) {
      super(message);
      this.name = 'ApiError';
      this.status = status;
      this.payload = payload;
    }
  }

  function messageFromPayload(payload) {
    if (payload === null || payload === undefined) return '';
    if (typeof payload === 'string') return payload.trim();
    if (typeof payload !== 'object') return String(payload);
    for (const key of ['error', 'message', 'detail', 'reason']) {
      const value = payload[key];
      if (typeof value === 'string' && value.trim()) return value.trim();
      if (value && typeof value === 'object') {
        const nested = messageFromPayload(value);
        if (nested) return nested;
      }
    }
    return '';
  }

  async function api(path, options = {}) {
    const method = options.method || 'GET';
    const headers = {
      Accept: 'application/json',
      'X-Account-ID': sanitizeHeaderValue(state.accountId) || DEFAULT_ACCOUNT,
    };
    if (options.body !== undefined) headers['Content-Type'] = 'application/json';

    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), 12000);
    let response;
    try {
      response = await fetch(path, {
        method,
        headers,
        body: options.body === undefined ? undefined : JSON.stringify(options.body),
        cache: 'no-store',
        signal: controller.signal,
      });
    } catch (error) {
      clearTimeout(timeout);
      if (error && error.name === 'AbortError') {
        throw new ApiError('Request timed out after 12 seconds.', 0, null);
      }
      throw new ApiError('Network request failed. Is the API running?', 0, null);
    }

    let text;
    try {
      text = await response.text();
    } finally {
      clearTimeout(timeout);
    }
    let payload = null;
    if (text) {
      try {
        payload = JSON.parse(text);
      } catch {
        payload = text;
      }
    }

    if (!response.ok) {
      const detail = messageFromPayload(payload) || response.statusText || 'Request failed';
      throw new ApiError(`${detail} (HTTP ${response.status})`, response.status, payload);
    }
    return payload;
  }

  // -----------------------------------------------------------------------
  // DOM + feedback helpers
  // -----------------------------------------------------------------------

  function el(tag, options = {}, children = []) {
    const node = document.createElement(tag);
    if (options.className) node.className = options.className;
    if (options.text !== undefined && options.text !== null) node.textContent = String(options.text);

    for (const [name, value] of Object.entries(options.attrs || {})) {
      if (value === undefined || value === null || value === false) continue;
      node.setAttribute(name, value === true ? '' : String(value));
    }
    for (const [name, value] of Object.entries(options.props || {})) {
      node[name] = value;
    }
    for (const [event, handler] of Object.entries(options.on || {})) {
      node.addEventListener(event, handler);
    }
    for (const child of [].concat(children)) {
      if (child === null || child === undefined || child === false) continue;
      node.append(child instanceof Node ? child : document.createTextNode(String(child)));
    }
    return node;
  }

  function button(label, className, onClick, options = {}) {
    const node = el('button', {
      className: `btn ${className}`.trim(),
      text: label,
      attrs: { type: options.type || 'button', ...(options.attrs || {}) },
      props: options.props || {},
    });
    if (onClick) node.addEventListener('click', onClick);
    return node;
  }

  function cell(content, className = '') {
    const node = el('td', { className });
    if (content instanceof Node) {
      node.append(content);
    } else {
      node.textContent = content === undefined || content === null || content === '' ? '—' : String(content);
    }
    return node;
  }

  function emptyRow(colspan, message) {
    return el('tr', {}, [el('td', { className: 'empty', attrs: { colspan: String(colspan) }, text: message })]);
  }

  function codeNode(value) {
    const text = String(value ?? '');
    if (!text) return document.createTextNode('—');
    return el('code', { className: 'mono truncate', text, attrs: { title: text } });
  }

  function badge(label, tone = 'neutral') {
    return el('span', { className: `badge badge--${tone}`, text: label });
  }

  function setText(id, value) {
    const node = byId(id);
    if (!node) return;
    node.textContent = value === undefined || value === null || value === '' ? '—' : String(value);
  }

  function setBadge(id, label, tone = 'neutral') {
    const node = byId(id);
    if (!node) return;
    node.textContent = label;
    node.className = `badge badge--${tone}`;
  }

  function setError(id, error) {
    const node = byId(id);
    if (!node) return;
    if (!error) {
      node.hidden = true;
      node.textContent = '';
      return;
    }
    node.hidden = false;
    node.textContent = error instanceof Error ? error.message : String(error);
  }

  function setConnection(tone, label) {
    const pill = byId('connection-pill');
    if (pill) pill.className = `pill pill--${tone}`;
    setText('connection-text', label);
  }

  function toast(message, tone = 'info') {
    const region = byId('toasts');
    if (!region) return;
    const text = String(message ?? '').trim();
    if (!text) return;
    const node = el('div', { className: `toast toast--${tone}`, attrs: { role: tone === 'error' ? 'alert' : 'status' } }, [
      el('span', { className: 'toast__dot', attrs: { 'aria-hidden': 'true' } }),
      el('span', { text }),
    ]);
    region.append(node);
    setTimeout(() => {
      node.classList.add('toast--out');
      setTimeout(() => node.remove(), 240);
    }, 4200);
  }

  async function withBusy(buttonNode, task) {
    if (!buttonNode) return task();
    const label = buttonNode.textContent;
    buttonNode.disabled = true;
    buttonNode.textContent = 'Working…';
    try {
      return await task();
    } finally {
      buttonNode.disabled = false;
      buttonNode.textContent = label;
    }
  }

  function errorText(error) {
    return error instanceof Error ? error.message : String(error);
  }

  // -----------------------------------------------------------------------
  // Live health + runtime stats
  // -----------------------------------------------------------------------

  function renderHealth() {
    const node = byId('health-status');
    if (!state.health && !state.stats) {
      setText('health-status', '—');
      if (node) node.className = 'badge badge--neutral';
      setText('health-journal', '—');
      setText('health-wal', '—');
      setText('health-query', '—');
      setText('health-db', '—');
      setText('db-read-conns', '—');
      return;
    }

    const healthRaw = state.health || state.stats;
    const health = unwrapObject(healthRaw, ['data', 'health']);
    const db = unwrapObject(readField(health, ['db', 'DB', 'database', 'Database'], {}), []);
    const statusRaw = readField(health, ['status', 'Status'], '');
    const dbOK = Boolean(readField(db, ['ok', 'OK'], false));
    const hasDB = Object.keys(db).length > 0;
    const status = norm(statusRaw);
    let tone = 'neutral';
    let label = status ? status.toUpperCase() : 'UNKNOWN';

    if (status) {
      const healthyStatus = /ok|healthy|up|ready/i.test(status);
      if (healthyStatus && (!hasDB || dbOK)) {
        tone = 'success';
      } else if (healthyStatus) {
        tone = 'danger';
      } else {
        tone = 'danger';
      }
    } else if (dbOK) {
      label = 'OK';
      tone = 'success';
    }

    setBadge('health-status', label, tone);

    const journal = readField(db, ['journal_mode', 'JournalMode', 'journalMode'], '');
    const wal = readField(db, ['wal_autocheckpoint', 'WALAutocheckpoint', 'walAutocheckpoint'], undefined);
    const queryOnly = readField(db, ['read_query_only', 'ReadQueryOnly', 'readQueryOnly'], undefined);

    setText('health-journal', journal || '—');
    if (wal === undefined || wal === null) {
      setText('health-wal', '—');
    } else if (Number(wal) === 0) {
      setText('health-wal', '0 (Litestream)');
    } else {
      setText('health-wal', String(wal));
    }
    setText('health-query', formatBool(queryOnly));
    setText('health-db', hasDB ? (dbOK ? 'OK' : 'DEGRADED') : '—');

    const readConns = pickNumber(state.stats, ['read_connections', 'read_conns', 'readConnections'], ['db', 'database']);
    setText('db-read-conns', displayNumber(readConns));
  }

  function renderStats() {
    const stats = state.stats;
    const metric = (id, names, roots) => setText(id, displayNumber(pickNumber(stats, names, roots)));

    metric('hp-requests', ['requests', 'request_count', 'total_requests'], ['hotpath', 'hot_path']);
    metric('hp-allowed', ['allowed', 'allowed_requests'], ['hotpath', 'hot_path']);
    metric('hp-rate-limited', ['rate_limited', 'ratelimited', 'rate_limited_requests'], ['hotpath', 'hot_path']);
    metric('hp-insufficient', ['insufficient_balance', 'insufficientbalance', 'insufficient'], ['hotpath', 'hot_path']);
    metric('hp-route-disabled', ['route_disabled', 'routedisabled'], ['hotpath', 'hot_path']);
    metric('hp-maintenance', ['maintenance_rejected', 'maintenancerejected'], ['hotpath', 'hot_path']);

    setBadge('hotpath-mode', stats ? 'ADMISSION' : 'NO DATA', stats ? 'success' : 'neutral');

    const capRaw = pickNumber(stats, ['queue_cap', 'queue_capacity', 'queuecapacity', 'capacity', 'cap'], ['batcher']);
    const cap = Math.max(1, asNumber(capRaw, DEFAULT_QUEUE_CAP) || DEFAULT_QUEUE_CAP);
    const lenRaw = pickNumber(stats, ['queue_len', 'queuedepth', 'queue_length', 'queuelength', 'depth'], ['batcher']);
    const len = Math.max(0, asNumber(lenRaw, 0));
    const droppedRaw = pickNumber(stats, ['dropped', 'drop_count', 'dropped_events'], ['batcher']);

    setText('batcher-cap', displayNumber(cap));
    setText('batcher-len', displayNumber(lenRaw));
    metric('batcher-enqueued', ['enqueued', 'enqueued_events', 'enqueued_total'], ['batcher']);
    metric('batcher-flushed', ['flushed', 'flushed_events', 'written', 'written_events'], ['batcher']);
    metric('batcher-flush-commits', ['flush_commits', 'flushcommits', 'commits', 'flush_count'], ['batcher']);
    metric('batcher-dropped', ['dropped', 'drop_count', 'dropped_events'], ['batcher']);

    const percent = Math.min(100, Math.max(0, (len / cap) * 100));
    const bar = byId('batcher-gauge');
    const track = byId('batcher-gauge-track');
    if (bar) {
      bar.style.width = `${percent.toFixed(1)}%`;
      bar.classList.toggle('is-high', percent >= 75 && percent < 95);
      bar.classList.toggle('is-critical', percent >= 95);
    }
    if (track) {
      track.setAttribute('aria-valuemax', String(cap));
      track.setAttribute('aria-valuenow', String(Math.round(len)));
    }
    setText(
      'batcher-gauge-label',
      stats ? `${displayNumber(len)} of ${displayNumber(cap)} slots · ${percent.toFixed(1)}%` : 'Awaiting metrics',
    );

    const dropped = asNumber(droppedRaw, 0);
    if (!stats) {
      setBadge('batcher-state', '—', 'neutral');
    } else if (dropped > 0) {
      setBadge('batcher-state', 'DROPS', 'danger');
    } else {
      setBadge('batcher-state', 'HEALTHY', 'success');
    }

    const raw = byId('stats-raw');
    if (raw) raw.textContent = stats ? JSON.stringify(stats, null, 2) : 'No stats loaded yet.';
  }

  async function refreshLive({ silent = false } = {}) {
    if (state.liveInFlight) return;
    state.liveInFlight = true;
    try {
      const [healthResult, statsResult] = await Promise.allSettled([
        api('/healthz'),
        api('/api/v1/runtime/stats'),
      ]);

      let successes = 0;
      if (healthResult.status === 'fulfilled') {
        state.health = healthResult.value;
        successes += 1;
      }
      if (statsResult.status === 'fulfilled') {
        state.stats = statsResult.value;
        successes += 1;
      }

      renderHealth();
      renderStats();

      if (successes > 0) {
        const now = new Date();
        setConnection('online', 'Live');
        const updated = byId('last-updated');
        if (updated) {
          updated.textContent = `Updated ${formatClock(now)}`;
          updated.setAttribute('title', now.toLocaleString());
        }
        setError('overview-error', null);
      } else {
        setConnection('offline', 'API unreachable');
        const reason =
          (healthResult.status === 'rejected' && healthResult.reason) ||
          (statsResult.status === 'rejected' && statsResult.reason);
        setError('overview-error', reason || new Error('Runtime health is unavailable.'));
        if (!silent) toast(errorText(reason || 'Runtime health is unavailable.'), 'error');
      }
    } finally {
      state.liveInFlight = false;
    }
  }

  // -----------------------------------------------------------------------
  // Runtime config
  // -----------------------------------------------------------------------

  function routeDraftsFrom(routes) {
    if (Array.isArray(routes)) {
      state.routesShape = 'array';
      return routes
        .map((entry) => ({
          name: norm(readField(entry, ['route', 'path', 'name', 'key', 'Route', 'Path', 'Name'], '')),
          enabled: Boolean(readField(entry, ['enabled', 'Enabled'], true)),
          cost: asNumber(readField(entry, ['cost', 'Cost'], 0), 0),
          rateLimitRpm: asNumber(readField(entry, ['rate_limit_rpm', 'rateLimitRPM', 'RateLimitRPM', 'rpm'], 0), 0),
        }))
        .filter((entry) => entry.name);
    }

    state.routesShape = 'object';
    const source = routes && typeof routes === 'object' ? routes : {};
    return Object.entries(source).map(([name, policy]) => ({
      name,
      enabled: Boolean(readField(policy, ['enabled', 'Enabled'], true)),
      cost: asNumber(readField(policy, ['cost', 'Cost'], 0), 0),
      rateLimitRpm: asNumber(readField(policy, ['rate_limit_rpm', 'rateLimitRPM', 'RateLimitRPM', 'rpm'], 0), 0),
    }));
  }

  function renderRouteRows() {
    const tbody = byId('route-rows');
    if (!tbody) return;
    if (!state.routeDrafts.length) {
      tbody.replaceChildren(emptyRow(5, 'No route overrides. Requests use the defaults above.'));
      return;
    }

    const rows = state.routeDrafts.map((draft, index) => {
      const name = String(draft.name);
      const enabledInput = el('input', {
        className: 'checkbox',
        attrs: { type: 'checkbox', 'aria-label': `Enable route ${name}` },
        props: { checked: Boolean(draft.enabled) },
        on: {
          change: (event) => {
            draft.enabled = event.target.checked;
          },
        },
      });
      const costInput = el('input', {
        className: 'input input--table',
        attrs: { type: 'number', min: '0', step: '1', inputmode: 'numeric', 'aria-label': `Cost for route ${name}` },
        props: { value: String(draft.cost) },
        on: {
          input: (event) => {
            draft.cost = event.target.value;
          },
        },
      });
      const rpmInput = el('input', {
        className: 'input input--table',
        attrs: { type: 'number', min: '0', step: '1', inputmode: 'numeric', 'aria-label': `Rate limit for route ${name}` },
        props: { value: String(draft.rateLimitRpm) },
        on: {
          input: (event) => {
            draft.rateLimitRpm = event.target.value;
          },
        },
      });
      const remove = button('Remove', 'btn--ghost btn--xs', () => {
        state.routeDrafts.splice(index, 1);
        renderRouteRows();
      });

      return el('tr', {}, [
        cell(el('code', { className: 'mono', text: name })),
        cell(enabledInput),
        cell(costInput),
        cell(rpmInput),
        cell(remove, 'cell--action'),
      ]);
    });

    tbody.replaceChildren(...rows);
  }

  function updateMaintenanceUi() {
    const toggle = byId('cfg-maintenance');
    const enabled = Boolean(toggle && toggle.checked);
    setText('maintenance-state', enabled ? 'Enabled' : 'Disabled');
    const banner = byId('maintenance-banner');
    if (banner) banner.hidden = !enabled;
  }

  function renderConfig() {
    const config = state.config && typeof state.config === 'object' ? state.config : {};
    const rpm = readField(config, ['default_rate_limit_rpm', 'defaultRateLimitRPM', 'DefaultRateLimitRPM'], 120);
    const cost = readField(config, ['default_request_cost', 'defaultRequestCost', 'DefaultRequestCost'], 1);
    const maintenance = readField(config, ['maintenance_mode', 'maintenanceMode', 'MaintenanceMode'], false);
    const routes = readField(config, ['routes', 'Routes'], {});

    const rpmInput = byId('cfg-default-rpm');
    const costInput = byId('cfg-default-cost');
    const maintenanceInput = byId('cfg-maintenance');
    if (rpmInput) rpmInput.value = String(rpm);
    if (costInput) costInput.value = String(cost);
    if (maintenanceInput) maintenanceInput.checked = Boolean(maintenance);

    state.routeDrafts = routeDraftsFrom(routes);
    renderRouteRows();
    updateMaintenanceUi();

    const save = byId('runtime-save');
    if (save) save.disabled = !state.configLoaded;
  }

  async function loadConfig({ silent = false } = {}) {
    try {
      const raw = await api('/api/v1/runtime/config');
      const unwrapped = unwrapObject(raw, ['config', 'data']);
      state.config = unwrapObject(unwrapped, ['config']);
      state.configLoaded = true;
      renderConfig();
      setError('runtime-error', null);
    } catch (error) {
      state.configLoaded = false;
      const save = byId('runtime-save');
      if (save) save.disabled = true;
      setError('runtime-error', error);
      if (!silent) toast(errorText(error), 'error');
    }
  }

  function buildRoutesPayload() {
    const seen = new Set();
    const entries = state.routeDrafts.map((draft) => {
      const name = norm(draft.name);
      if (!name) throw new Error('Route names must not be empty.');
      if (seen.has(name)) throw new Error(`Duplicate route override: ${name}`);
      seen.add(name);
      return {
        name,
        enabled: Boolean(draft.enabled),
        cost: parseIntegerValue(draft.cost, `Cost for ${name}`, 0),
        rateLimitRpm: parseIntegerValue(draft.rateLimitRpm, `Rate limit for ${name}`, 0),
      };
    });

    if (state.routesShape === 'array') {
      return entries.map((entry) => ({
        route: entry.name,
        enabled: entry.enabled,
        cost: entry.cost,
        rate_limit_rpm: entry.rateLimitRpm,
      }));
    }

    const routes = {};
    for (const entry of entries) {
      routes[entry.name] = {
        enabled: entry.enabled,
        cost: entry.cost,
        rate_limit_rpm: entry.rateLimitRpm,
      };
    }
    return routes;
  }

  async function saveConfig(event) {
    event.preventDefault();
    const saveButton = byId('runtime-save');
    try {
      const rpm = parseIntegerValue(byId('cfg-default-rpm')?.value, 'Default rate limit', 1);
      const cost = parseIntegerValue(byId('cfg-default-cost')?.value, 'Default request cost', 0);
      const maintenance = Boolean(byId('cfg-maintenance')?.checked);
      const routes = buildRoutesPayload();

      await withBusy(saveButton, () =>
        api('/api/v1/runtime/config', {
          method: 'PUT',
          body: {
            default_rate_limit_rpm: rpm,
            default_request_cost: cost,
            maintenance_mode: maintenance,
            routes,
          },
        }),
      );

      setError('runtime-error', null);
      toast('Runtime config updated.', 'success');
      await Promise.allSettled([loadConfig({ silent: true }), refreshLive({ silent: true })]);
    } catch (error) {
      setError('runtime-error', error);
      toast(errorText(error), 'error');
    }
  }

  function addRoute(event) {
    event.preventDefault();
    const input = byId('route-add-name');
    const name = norm(input?.value);
    if (!name) {
      setError('runtime-error', new Error('Route name is required.'));
      return;
    }
    if (state.routeDrafts.some((draft) => draft.name === name)) {
      setError('runtime-error', new Error(`Route override already exists: ${name}`));
      return;
    }

    const defaultCost = Number(byId('cfg-default-cost')?.value);
    const defaultRPM = Number(byId('cfg-default-rpm')?.value);
    state.routeDrafts.push({
      name,
      enabled: true,
      cost: Number.isInteger(defaultCost) ? defaultCost : 1,
      rateLimitRpm: Number.isInteger(defaultRPM) ? defaultRPM : 120,
    });
    setError('runtime-error', null);
    if (input) input.value = '';
    renderRouteRows();
  }

  // -----------------------------------------------------------------------
  // Account
  // -----------------------------------------------------------------------

  function renderAccount() {
    const account = state.account
      ? unwrapObject(unwrapObject(state.account, ['account', 'quota', 'data']), ['quota', 'account'])
      : null;
    const id = account ? readField(account, ['account_id', 'accountID', 'AccountID'], state.accountId) : state.accountId;
    setText('account-id-display', id);

    const balance = account ? readField(account, ['balance', 'Balance'], undefined) : undefined;
    setText('account-balance', displayNumber(balance));
    const balanceNode = byId('account-balance');
    if (balanceNode) {
      balanceNode.classList.toggle('is-negative', Number(balance) < 0);
    }

    setText('account-rpm', displayNumber(account ? readField(account, ['rate_limit_rpm', 'rateLimitRPM', 'RateLimitRPM'], undefined) : undefined));

    const updated = account ? readField(account, ['updated_at', 'updatedAt', 'UpdatedAt'], '') : '';
    const updatedNode = byId('account-updated');
    if (updatedNode) {
      updatedNode.textContent = account
        ? `Quota updated ${formatTime(updated, 'at an unknown time')}`
        : 'No account snapshot yet.';
    }
  }

  async function loadAccount({ silent = false } = {}) {
    try {
      const raw = await api(`/api/v1/accounts/${encodeURIComponent(state.accountId)}`);
      state.account = raw;
      renderAccount();
      setError('account-error', null);
    } catch (error) {
      state.account = null;
      renderAccount();
      setError('account-error', error);
      if (!silent) toast(errorText(error), 'error');
    }
  }

  async function submitLedger(action, event) {
    event.preventDefault();
    const isCredit = action === 'credit';
    const amountInput = byId(isCredit ? 'credit-amount' : 'charge-amount');
    const reasonInput = byId(isCredit ? 'credit-reason' : 'charge-reason');
    const submit = event.submitter || event.currentTarget?.querySelector('button[type="submit"]');

    try {
      const amount = parseIntegerValue(amountInput?.value, 'Amount', 1);
      const reason = norm(reasonInput?.value);
      if (!reason) throw new Error('Reason is required.');

      await withBusy(submit, () =>
        api(`/api/v1/accounts/${encodeURIComponent(state.accountId)}/${action}`, {
          method: 'POST',
          body: { amount, reason },
        }),
      );

      if (amountInput) amountInput.value = '';
      if (reasonInput) reasonInput.value = '';
      setError('account-error', null);
      toast(`${isCredit ? 'Credited' : 'Charged'} ${displayNumber(amount)} credits.`, 'success');
      await Promise.allSettled([loadAccount({ silent: true }), refreshLive({ silent: true }), loadBilling({ silent: true })]);
    } catch (error) {
      setError('account-error', error);
      toast(errorText(error), 'error');
    }
  }

  // -----------------------------------------------------------------------
  // Users
  // -----------------------------------------------------------------------

  function userID(user) {
    return String(readField(user, ['id', 'ID', 'user_id', 'UserID'], ''));
  }

  function renderUserOptions() {
    const list = byId('user-options');
    if (!list) return;
    list.replaceChildren(
      ...state.users.map((user) => {
        const id = userID(user);
        if (!id) return null;
        const name = String(field(user, 'displayName', ''));
        return el('option', { props: { value: id }, text: name ? `${name} · ${id}` : id });
      }).filter(Boolean),
    );
  }

  function renderUsers() {
    const tbody = byId('users-tbody');
    if (!tbody) return;
    if (!state.users.length) {
      tbody.replaceChildren(emptyRow(6, 'No users yet. Create one above.'));
      setText('users-count', '0');
      renderUserOptions();
      return;
    }

    const rows = state.users.map((user) => {
      const id = userID(user);
      const displayName = String(field(user, 'displayName', ''));
      const email = String(field(user, 'email', ''));
      const version = field(user, 'version', '—');
      const createdAt = field(user, 'createdAt', '');

      const emailInput = el('input', {
        className: 'input input--table',
        attrs: { type: 'email', maxlength: '254', 'aria-label': `Email for ${displayName || id}`, spellcheck: 'false' },
        props: { value: email },
      });
      const saveButton = button('Save', 'btn--ghost btn--sm', null, {
        attrs: { 'aria-label': `Save email for ${displayName || id}` },
      });
      saveButton.addEventListener('click', () =>
        withBusy(saveButton, async () => {
          try {
            const nextEmail = norm(emailInput.value);
            if (!nextEmail) throw new Error('Email is required.');
            await api(`/api/v1/users/${encodeURIComponent(id)}/email`, {
              method: 'PUT',
              body: { email: nextEmail },
            });
            toast('Email updated.', 'success');
            await loadUsers({ silent: true });
          } catch (error) {
            toast(errorText(error), 'error');
          }
        }),
      );

      return el('tr', {}, [
        cell(codeNode(id)),
        cell(displayName || '—'),
        cell(emailInput),
        cell(String(version)),
        cell(formatTime(createdAt)),
        cell(saveButton, 'cell--action'),
      ]);
    });

    tbody.replaceChildren(...rows);
    setText('users-count', String(state.users.length));
    renderUserOptions();
  }

  async function loadUsers({ silent = false } = {}) {
    try {
      const raw = await api('/api/v1/users');
      state.users = rowsFrom(raw, ['users', 'items', 'data']);
      renderUsers();
      setError('users-error', null);
    } catch (error) {
      setError('users-error', error);
      if (!silent) toast(errorText(error), 'error');
    }
  }

  async function createUser(event) {
    event.preventDefault();
    const form = event.currentTarget;
    const submit = event.submitter || form?.querySelector('button[type="submit"]');
    const displayName = norm(byId('user-display-name')?.value);
    const email = norm(byId('user-email')?.value);

    try {
      if (!displayName) throw new Error('Display name is required.');
      if (!email) throw new Error('Email is required.');
      await withBusy(submit, () =>
        api('/api/v1/users', {
          method: 'POST',
          body: { display_name: displayName, email },
        }),
      );
      if (form) form.reset();
      toast('User created.', 'success');
      await Promise.allSettled([loadUsers({ silent: true }), refreshLive({ silent: true })]);
    } catch (error) {
      setError('users-error', error);
      toast(errorText(error), 'error');
    }
  }

  // -----------------------------------------------------------------------
  // Teams
  // -----------------------------------------------------------------------

  function teamID(team) {
    return String(readField(team, ['id', 'ID', 'team_id', 'TeamID'], ''));
  }

  function renderTeamOptions() {
    const list = byId('team-options');
    if (list) {
      list.replaceChildren(
        ...state.teams.map((team) => {
          const id = teamID(team);
          if (!id) return null;
          const name = String(field(team, 'name', ''));
          return el('option', { props: { value: id }, text: name ? `${name} · ${id}` : id });
        }).filter(Boolean),
      );
    }

    const teamInput = byId('membership-team-id');
    if (teamInput && !norm(teamInput.value) && state.teams.length) {
      const first = teamID(state.teams[0]);
      if (first) teamInput.value = first;
    }
  }

  function renderTeams() {
    const tbody = byId('teams-tbody');
    if (!tbody) return;
    if (!state.teams.length) {
      tbody.replaceChildren(emptyRow(5, 'No teams yet. Create one above.'));
      setText('teams-count', '0');
      renderTeamOptions();
      return;
    }

    const rows = state.teams.map((team) => {
      const id = teamID(team);
      const name = String(field(team, 'name', ''));
      const version = field(team, 'version', '—');
      const createdAt = field(team, 'createdAt', '');

      const nameInput = el('input', {
        className: 'input input--table',
        attrs: { type: 'text', maxlength: '100', 'aria-label': `Name for team ${name || id}`, spellcheck: 'false' },
        props: { value: name },
      });
      const saveButton = button('Save', 'btn--ghost btn--sm', null, {
        attrs: { 'aria-label': `Save name for team ${name || id}` },
      });
      saveButton.addEventListener('click', () =>
        withBusy(saveButton, async () => {
          try {
            const nextName = norm(nameInput.value);
            if (!nextName) throw new Error('Team name is required.');
            const expected = Number(version);
            if (!Number.isInteger(expected) || expected < 0) {
              throw new Error('Team version is unavailable; reload teams and retry.');
            }
            await api(`/api/v1/teams/${encodeURIComponent(id)}`, {
              method: 'PUT',
              body: { name: nextName, expected_version: expected },
            });
            toast('Team name updated.', 'success');
            await loadTeams({ silent: true });
          } catch (error) {
            toast(errorText(error), 'error');
          }
        }),
      );

      return el('tr', {}, [
        cell(codeNode(id)),
        cell(nameInput),
        cell(String(version)),
        cell(formatTime(createdAt)),
        cell(saveButton, 'cell--action'),
      ]);
    });

    tbody.replaceChildren(...rows);
    setText('teams-count', String(state.teams.length));
    renderTeamOptions();
  }

  async function loadTeams({ silent = false } = {}) {
    try {
      const raw = await api('/api/v1/teams');
      state.teams = rowsFrom(raw, ['teams', 'items', 'data']);
      renderTeams();
      setError('teams-error', null);
    } catch (error) {
      setError('teams-error', error);
      if (!silent) toast(errorText(error), 'error');
    }
  }

  async function createTeam(event) {
    event.preventDefault();
    const form = event.currentTarget;
    const submit = event.submitter || form?.querySelector('button[type="submit"]');
    const name = norm(byId('team-name')?.value);

    try {
      if (!name) throw new Error('Team name is required.');
      await withBusy(submit, () =>
        api('/api/v1/teams', {
          method: 'POST',
          body: { name },
        }),
      );
      if (form) form.reset();
      toast('Team created.', 'success');
      await loadTeams({ silent: true });
    } catch (error) {
      setError('teams-error', error);
      toast(errorText(error), 'error');
    }
  }

  // -----------------------------------------------------------------------
  // Memberships
  // -----------------------------------------------------------------------

  function renderMemberships() {
    const tbody = byId('memberships-tbody');
    if (!tbody) return;
    const teamId = norm(byId('membership-team-id')?.value);
    if (!teamId) {
      tbody.replaceChildren(emptyRow(7, 'Enter a team ID to load memberships.'));
      setText('memberships-count', '0');
      return;
    }
    if (!state.memberships.length) {
      tbody.replaceChildren(emptyRow(7, 'No memberships for this team yet.'));
      setText('memberships-count', '0');
      return;
    }

    const rows = state.memberships.map((membership) => {
      const id = String(readField(membership, ['id', 'ID', 'membership_id', 'MembershipID'], ''));
      const userIDValue = String(field(membership, 'userID', ''));
      const role = String(field(membership, 'role', 'MEMBER')).toUpperCase();
      const status = String(field(membership, 'status', '')).toUpperCase();
      const version = field(membership, 'version', '—');
      const updatedAt = field(membership, 'updatedAt', '');

      const roleSelect = el(
        'select',
        { className: 'select', attrs: { 'aria-label': `Role for membership ${id}` } },
        ['OWNER', 'ADMIN', 'MEMBER'].map((option) =>
          el('option', { text: option, props: { value: option, selected: option === role } }),
        ),
      );

      const actions = [];
      const roleButton = button('Save role', 'btn--ghost btn--xs', null);
      roleButton.addEventListener('click', () =>
        withBusy(roleButton, async () => {
          try {
            await api(`/api/v1/memberships/${encodeURIComponent(id)}/role`, {
              method: 'PUT',
              body: { role: roleSelect.value },
            });
            toast('Membership role updated.', 'success');
            await Promise.allSettled([loadMemberships({ silent: true }), refreshLive({ silent: true })]);
          } catch (error) {
            toast(errorText(error), 'error');
          }
        }),
      );
      actions.push(roleButton);

      if (status === 'PENDING') {
        const approveButton = button('Approve', 'btn--primary btn--xs', null);
        approveButton.addEventListener('click', () =>
          withBusy(approveButton, async () => {
            try {
              await api(`/api/v1/memberships/${encodeURIComponent(id)}/approve`, {
                method: 'POST',
                body: {},
              });
              toast('Membership approved.', 'success');
              await Promise.allSettled([loadMemberships({ silent: true }), refreshLive({ silent: true })]);
            } catch (error) {
              toast(errorText(error), 'error');
            }
          }),
        );
        actions.push(approveButton);
      }

      if (status !== 'LEAVED') {
        const leaveButton = button('Leave', 'btn--danger btn--xs', null);
        leaveButton.addEventListener('click', () =>
          withBusy(leaveButton, async () => {
            try {
              await api(`/api/v1/memberships/${encodeURIComponent(id)}/leave`, {
                method: 'POST',
                body: {},
              });
              toast('Membership ended.', 'success');
              await Promise.allSettled([loadMemberships({ silent: true }), refreshLive({ silent: true })]);
            } catch (error) {
              toast(errorText(error), 'error');
            }
          }),
        );
        actions.push(leaveButton);
      }

      return el('tr', {}, [
        cell(codeNode(id)),
        cell(codeNode(userIDValue)),
        cell(roleSelect),
        cell(badge(status || 'UNKNOWN', statusTone(status))),
        cell(String(version)),
        cell(formatTime(updatedAt)),
        cell(el('div', { className: 'action-row' }, actions), 'cell--actions'),
      ]);
    });

    tbody.replaceChildren(...rows);
    setText('memberships-count', String(state.memberships.length));
  }

  async function loadMemberships({ silent = false } = {}) {
    const teamId = norm(byId('membership-team-id')?.value);
    if (!teamId) {
      state.memberships = [];
      state.selectedTeamId = '';
      renderMemberships();
      setError('memberships-error', null);
      return;
    }

    try {
      const raw = await api(`/api/v1/teams/${encodeURIComponent(teamId)}/memberships`);
      state.memberships = rowsFrom(raw, ['memberships', 'items', 'data']);
      state.selectedTeamId = teamId;
      renderMemberships();
      setError('memberships-error', null);
    } catch (error) {
      state.memberships = [];
      renderMemberships();
      setError('memberships-error', error);
      if (!silent) toast(errorText(error), 'error');
    }
  }

  async function submitMembership(action, event) {
    event.preventDefault();
    const form = event.currentTarget;
    const submit = event.submitter || form?.querySelector('button[type="submit"]');
    const teamId = norm(byId('membership-team-id')?.value);
    const userInput = byId(action === 'join' ? 'join-user-id' : 'request-user-id');
    const userIDValue = norm(userInput?.value);

    try {
      if (!teamId) throw new Error('Set a team context before joining.');
      if (!userIDValue) throw new Error('User ID is required.');

      await withBusy(submit, () =>
        api(`/api/v1/teams/${encodeURIComponent(teamId)}/memberships/${action}`, {
          method: 'POST',
          body: { user_id: userIDValue },
        }),
      );

      if (userInput) userInput.value = '';
      setError('memberships-error', null);
      toast(action === 'join' ? 'Joined team.' : 'Join request created.', 'success');
      await Promise.allSettled([loadMemberships({ silent: true }), refreshLive({ silent: true })]);
    } catch (error) {
      setError('memberships-error', error);
      toast(errorText(error), 'error');
    }
  }

  // -----------------------------------------------------------------------
  // Billing + request logs
  // -----------------------------------------------------------------------

  function renderBilling() {
    const tbody = byId('billing-tbody');
    if (!tbody) return;
    if (!state.billing.length) {
      tbody.replaceChildren(emptyRow(6, 'No billing events for this filter.'));
      setText('billing-count', '0');
      return;
    }

    const rows = state.billing.map((event) => {
      const amount = asNumber(field(event, 'amount', 0), 0);
      const amountNode = el('span', {
        className: `amount ${amount > 0 ? 'amount--positive' : amount < 0 ? 'amount--negative' : ''}`.trim(),
        text: `${amount > 0 ? '+' : ''}${displayNumber(amount)}`,
      });
      return el('tr', {}, [
        cell(formatTime(field(event, 'occurredAt', ''))),
        cell(codeNode(readField(event, ['id', 'ID', 'event_id', 'EventID'], ''))),
        cell(String(field(event, 'accountID', ''))),
        cell(amountNode),
        cell(String(field(event, 'reason', ''))),
        cell(codeNode(field(event, 'requestID', ''))),
      ]);
    });

    tbody.replaceChildren(...rows);
    setText('billing-count', String(state.billing.length));
  }

  async function loadBilling({ silent = false } = {}) {
    try {
      const account = norm(byId('billing-account')?.value);
      const limit = parseLimit(byId('billing-limit')?.value, 50);
      const params = new URLSearchParams();
      params.set('limit', String(limit));
      if (account) params.set('account_id', account);
      const raw = await api(`/api/v1/billing/events?${params.toString()}`);
      state.billing = rowsFrom(raw, ['events', 'billing_events', 'items', 'data']);
      renderBilling();
      setError('billing-error', null);
    } catch (error) {
      setError('billing-error', error);
      if (!silent) toast(errorText(error), 'error');
    }
  }

  function renderLogs() {
    const tbody = byId('logs-tbody');
    if (!tbody) return;
    if (!state.logs.length) {
      tbody.replaceChildren(emptyRow(7, 'No request logs for this filter.'));
      setText('logs-count', '0');
      return;
    }

    const rows = state.logs.map((log) => {
      const code = field(log, 'statusCode', '');
      const tone = httpTone(code);
      return el('tr', {}, [
        cell(formatTime(field(log, 'occurredAt', ''))),
        cell(el('code', { className: 'mono', text: String(field(log, 'method', '')) })),
        cell(codeNode(field(log, 'path', ''))),
        cell(el('span', { className: `status-code status-code--${tone}`, text: String(code || '—') })),
        cell(formatLatency(field(log, 'latencyUS', undefined))),
        cell(String(field(log, 'accountID', ''))),
        cell(codeNode(field(log, 'requestID', ''))),
      ]);
    });

    tbody.replaceChildren(...rows);
    setText('logs-count', String(state.logs.length));
  }

  async function loadLogs({ silent = false } = {}) {
    try {
      const limit = parseLimit(byId('logs-limit')?.value, 50);
      const raw = await api(`/api/v1/logs?limit=${encodeURIComponent(String(limit))}`);
      state.logs = rowsFrom(raw, ['logs', 'request_logs', 'items', 'data']);
      renderLogs();
      setError('logs-error', null);
    } catch (error) {
      setError('logs-error', error);
      if (!silent) toast(errorText(error), 'error');
    }
  }

  // -----------------------------------------------------------------------
  // Orchestration
  // -----------------------------------------------------------------------

  async function refreshAll({ silent = false } = {}) {
    await Promise.allSettled([
      refreshLive({ silent }),
      loadConfig({ silent }),
      loadAccount({ silent }),
      loadUsers({ silent }),
      loadTeams({ silent }),
      loadBilling({ silent }),
      loadLogs({ silent }),
    ]);

    const teamInput = byId('membership-team-id');
    if (teamInput && !norm(teamInput.value) && state.teams.length) {
      const first = teamID(state.teams[0]);
      if (first) teamInput.value = first;
    }
    await loadMemberships({ silent });
  }

  function applyAccount(event) {
    event.preventDefault();
    const input = byId('account-id');
    const next = sanitizeHeaderValue(input?.value) || DEFAULT_ACCOUNT;
    state.accountId = next;
    if (input) input.value = next;
    try {
      localStorage.setItem(ACCOUNT_STORAGE_KEY, next);
    } catch {
      // Storage can be unavailable in hardened browsing modes; the header still works.
    }
    const billingAccount = byId('billing-account');
    if (billingAccount) billingAccount.value = next;
    setText('account-id-display', next);
    toast(`Account context: ${next}`, 'info');
    refreshAll();
  }

  function bindEvents() {
    byId('account-form')?.addEventListener('submit', applyAccount);

    byId('refresh-all')?.addEventListener('click', (event) => {
      withBusy(event.currentTarget, async () => {
        await refreshAll();
        toast('Console refreshed.', 'success');
      });
    });

    byId('runtime-form')?.addEventListener('submit', saveConfig);
    byId('route-add-form')?.addEventListener('submit', addRoute);
    byId('cfg-maintenance')?.addEventListener('change', updateMaintenanceUi);
    byId('runtime-refresh')?.addEventListener('click', (event) => {
      withBusy(event.currentTarget, async () => {
        await loadConfig();
        if (state.configLoaded) toast('Runtime config reloaded.', 'success');
      });
    });

    byId('credit-form')?.addEventListener('submit', (event) => submitLedger('credit', event));
    byId('charge-form')?.addEventListener('submit', (event) => submitLedger('charge', event));
    byId('account-refresh')?.addEventListener('click', (event) => {
      withBusy(event.currentTarget, () => loadAccount());
    });

    byId('user-create-form')?.addEventListener('submit', createUser);
    byId('users-refresh')?.addEventListener('click', (event) => {
      withBusy(event.currentTarget, () => loadUsers());
    });

    byId('team-create-form')?.addEventListener('submit', createTeam);
    byId('teams-refresh')?.addEventListener('click', (event) => {
      withBusy(event.currentTarget, () => loadTeams());
    });

    byId('membership-team-form')?.addEventListener('submit', (event) => {
      event.preventDefault();
      const submit = event.submitter || event.currentTarget.querySelector('button[type="submit"]');
      withBusy(submit, () => loadMemberships());
    });
    byId('memberships-refresh')?.addEventListener('click', (event) => {
      withBusy(event.currentTarget, () => loadMemberships());
    });
    byId('join-form')?.addEventListener('submit', (event) => submitMembership('join', event));
    byId('request-form')?.addEventListener('submit', (event) => submitMembership('request', event));

    byId('billing-form')?.addEventListener('submit', (event) => {
      event.preventDefault();
      const submit = event.submitter || event.currentTarget.querySelector('button[type="submit"]');
      withBusy(submit, () => loadBilling());
    });
    byId('logs-form')?.addEventListener('submit', (event) => {
      event.preventDefault();
      const submit = event.submitter || event.currentTarget.querySelector('button[type="submit"]');
      withBusy(submit, () => loadLogs());
    });
  }

  function setupScrollSpy() {
    const links = Array.from(document.querySelectorAll('.main-nav a'));
    const entries = links
      .map((link) => ({ link, section: document.querySelector(link.getAttribute('href')) }))
      .filter((entry) => entry.section);
    if (!entries.length || !('IntersectionObserver' in window)) return;

    const observer = new IntersectionObserver(
      (observed) => {
        for (const entry of observed) {
          if (!entry.isIntersecting) continue;
          const match = entries.find((item) => item.section === entry.target);
          if (!match) continue;
          links.forEach((link) => link.removeAttribute('aria-current'));
          match.link.setAttribute('aria-current', 'true');
        }
      },
      { rootMargin: '-30% 0px -60% 0px', threshold: 0 },
    );
    entries.forEach((entry) => observer.observe(entry.section));
  }

  function startPolling() {
    setInterval(() => {
      if (!document.hidden) refreshLive({ silent: true });
    }, LIVE_REFRESH_MS);

    document.addEventListener('visibilitychange', () => {
      if (!document.hidden) refreshLive({ silent: true });
    });
  }

  async function boot() {
    let saved = DEFAULT_ACCOUNT;
    try {
      saved = sanitizeHeaderValue(localStorage.getItem(ACCOUNT_STORAGE_KEY)) || DEFAULT_ACCOUNT;
    } catch {
      saved = DEFAULT_ACCOUNT;
    }
    state.accountId = saved;

    const accountInput = byId('account-id');
    if (accountInput) accountInput.value = saved;
    const billingAccount = byId('billing-account');
    if (billingAccount) billingAccount.value = saved;
    setText('account-id-display', saved);

    bindEvents();
    setupScrollSpy();
    await refreshAll({ silent: true });
    startPolling();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', boot);
  } else {
    boot();
  }
})();
