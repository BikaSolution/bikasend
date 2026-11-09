'use strict';

// Every untrusted string (specs, responses, variable names) reaches the DOM as text through h().
const api = () => window.go.api.App;
const $ = (id) => document.getElementById(id);
const isMac = /Mac/i.test(navigator.platform);
const MOD = isMac ? '⌘' : 'Ctrl+';
const METHODS = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS'];
const BODY_TYPES = [['none', 'None'], ['json', 'JSON'], ['text', 'Text'], ['xml', 'XML'], ['form', 'Form (urlencoded)'], ['multipart', 'Multipart (files)']];
const VAR_RE = /\{\{\{\{[^{}]*\}\}\}\}|\{\{\s*([A-Za-z0-9_.$-]+)\s*\}\}/g;
const KIND_LABELS = { text: 'Text', secret: 'Secret (this session only)', prompt: 'Ask me each session', aws: 'AWS Secrets Manager', ssm: 'AWS Parameter Store', env: 'Environment variable' };
const SCOPE_LABELS = { session: 'Session', environment: 'Environment', folder: 'Folder', global: 'Global', spec: 'Spec' };

function h(tag, props, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (v == null || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k === 'text') el.textContent = v;
    else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (['value', 'checked', 'disabled', 'selected', 'open', 'title', 'placeholder', 'type', 'id', 'htmlFor', 'tabIndex', 'src', 'alt'].includes(k)) el[k] = v;
    else el.setAttribute(k, v === true ? '' : String(v));
  }
  for (const kid of kids.flat(Infinity)) {
    if (kid == null || kid === false) continue;
    el.append(kid instanceof Node ? kid : String(kid));
  }
  return el;
}

const store = {
  get(k) { try { return localStorage.getItem(k); } catch { return null; } },
  set(k, v) { try { localStorage.setItem(k, v); } catch { /* storage may be blocked */ } },
};

const state = {
  ws: null,
  specs: [],
  specIndex: -1,
  filter: '',
  sideTab: 'ops',
  history: [],
  tabs: [],
  active: null,
  varScope: 'active',
  varSearch: '',
  editing: null,
  revealed: {},
  update: null,
};
let tabSeq = 1;

function errText(e) {
  return String((e && e.message) || e || 'Unknown error').replace(/\b(bikasend|awscli): /g, '');
}

let toastTimer;
function toast(msg, isErr) {
  const t = $('toast');
  t.textContent = msg;
  t.className = 'toast' + (isErr ? ' err' : '');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => t.classList.add('hidden'), isErr ? 6000 : 2500);
}

async function call(fn, ...args) {
  try {
    return await api()[fn](...args);
  } catch (e) {
    toast(errText(e), true);
    throw e;
  }
}

// ---------- Variables ----------

function activeVars() {
  const map = new Map();
  for (const v of (state.ws && state.ws.variables) || []) if (v.active) map.set(v.name, v);
  return map;
}

function varStatus(name, tab) {
  if (name === 'baseUrl' && tab && tab.server && !activeVars().has('baseUrl')) return 'ok';
  const v = activeVars().get(name);
  if (!v || !v.resolved) return 'missing';
  return v.secret ? 'secret' : 'ok';
}

function allVarNames(tab) {
  const names = new Map();
  for (const [n, v] of activeVars()) names.set(n, v.scope);
  if (tab && tab.server && !names.has('baseUrl')) names.set('baseUrl', 'spec');
  return [...names.entries()].sort((a, b) => a[0].localeCompare(b[0]));
}

function usedVars(tab) {
  const r = tab.req;
  const texts = [r.url, r.headers, r.bodyType === 'none' ? '' : r.body];
  for (const p of r.pathParams || []) texts.push(p.value);
  for (const q of r.query || []) if (q.enabled) texts.push(q.name, q.value);
  for (const f of r.form || []) if (f.enabled) texts.push(f.name, f.value);
  const used = [];
  for (const t of texts) for (const m of String(t || '').matchAll(VAR_RE)) if (m[1] && !used.includes(m[1])) used.push(m[1]);
  return used;
}

function fillHighlight(hl, text, tab) {
  hl.replaceChildren();
  let last = 0;
  for (const m of text.matchAll(VAR_RE)) {
    hl.append(text.slice(last, m.index));
    hl.append(m[1] ? h('mark', { class: varStatus(m[1], tab) }, m[0]) : m[0]);
    last = m.index + m[0].length;
  }
  hl.append(text.slice(last) + '\n');
}

// varField is an input or textarea with {{variable}} highlighting and autocomplete.
function varField({ value, multiline, placeholder, label, tab, onInput, className }) {
  const hl = h('div', { class: 'hl', 'aria-hidden': 'true' });
  const input = multiline
    ? h('textarea', { spellcheck: 'false', placeholder, 'aria-label': label, value: value || '' })
    : h('input', { type: 'text', spellcheck: 'false', placeholder, 'aria-label': label, value: value || '', class: className });
  const wrap = h('div', { class: 'field' + (multiline ? '' : ' single') }, hl, input);
  const refresh = () => {
    fillHighlight(hl, input.value, tab);
    hl.scrollTop = input.scrollTop;
    hl.scrollLeft = input.scrollLeft;
  };
  wrap._refresh = refresh;
  input.addEventListener('input', () => {
    onInput(input.value);
    if (tab) tab.dirty = true;
    refresh();
    checkComplete(input, tab);
    renderChips();
  });
  input.addEventListener('scroll', () => { hl.scrollTop = input.scrollTop; hl.scrollLeft = input.scrollLeft; hideVarCard(); });
  input.addEventListener('mousemove', (e) => {
    const hit = varAtPoint(hl, e.clientX, e.clientY);
    if (hit) showVarCardSoon(hit.name, hit.rect, tab);
    else if (!onCardPath(e.clientX, e.clientY)) hideVarCardSoon();
  });
  input.addEventListener('mouseleave', (e) => { if (!onCardPath(e.clientX, e.clientY)) hideVarCardSoon(); });
  input.addEventListener('click', (e) => {
    if (!(e.metaKey || e.ctrlKey)) return;
    const hit = varAtPoint(hl, e.clientX, e.clientY);
    if (!hit) return;
    e.preventDefault();
    openVariable(hit.name);
  });
  input.addEventListener('keydown', (e) => completeKey(e, input));
  input.addEventListener('blur', () => setTimeout(hideComplete, 150));
  refresh();
  return wrap;
}

function refreshVarMarks() {
  document.querySelectorAll('.field').forEach((f) => f._refresh && f._refresh());
  renderChips();
}

// ---------- Autocomplete ----------

const complete = { input: null, items: [], index: 0, tab: null };

function checkComplete(input, tab) {
  const pos = input.selectionStart;
  const m = input.value.slice(0, pos).match(/\{\{\s*([A-Za-z0-9_.$-]*)$/);
  if (!m) return hideComplete();
  const q = m[1].toLowerCase();
  const items = allVarNames(tab).filter(([n]) => n.toLowerCase().includes(q)).slice(0, 30);
  if (!items.length) return hideComplete();
  Object.assign(complete, { input, items, index: 0, tab });
  const el = $('complete');
  const rect = input.getBoundingClientRect();
  let top = rect.bottom + 2;
  if (input.tagName === 'TEXTAREA') {
    const lineHeight = parseFloat(getComputedStyle(input).lineHeight) || 18;
    const line = input.value.slice(0, pos).split('\n').length;
    top = Math.min(rect.bottom, rect.top + 6 + line * lineHeight - input.scrollTop) + 2;
  }
  el.style.left = Math.min(rect.left, window.innerWidth - 280) + 'px';
  el.style.top = Math.min(top, window.innerHeight - 250) + 'px';
  renderComplete();
}

function renderComplete() {
  const el = $('complete');
  el.replaceChildren(...complete.items.map(([name, scope], i) => h('button', {
    class: i === complete.index ? 'active' : '', role: 'option', type: 'button',
    onmousedown: (e) => { e.preventDefault(); applyComplete(i); },
  }, h('span', { class: 'mono' }, name), h('span', { class: 'scope' }, SCOPE_LABELS[scope] || scope))));
  el.classList.remove('hidden');
}

function hideComplete() {
  $('complete').classList.add('hidden');
  complete.input = null;
}

function completeKey(e, input) {
  if (complete.input !== input || $('complete').classList.contains('hidden')) return;
  if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
    e.preventDefault();
    complete.index = (complete.index + (e.key === 'ArrowDown' ? 1 : -1) + complete.items.length) % complete.items.length;
    renderComplete();
  } else if (e.key === 'Enter' || e.key === 'Tab') {
    e.preventDefault();
    applyComplete(complete.index);
  } else if (e.key === 'Escape') {
    hideComplete();
  }
}

function applyComplete(i) {
  const input = complete.input;
  if (!input) return;
  const name = complete.items[i][0];
  const pos = input.selectionStart;
  const before = input.value.slice(0, pos).replace(/\{\{\s*[A-Za-z0-9_.$-]*$/, '{{' + name + '}}');
  const after = input.value.slice(pos).replace(/^[A-Za-z0-9_.$-]*\s*\}\}/, '');
  input.value = before + after;
  input.selectionStart = input.selectionEnd = before.length;
  hideComplete();
  input.dispatchEvent(new Event('input'));
  input.focus();
}

// ---------- Tabs and requests ----------

function blankRequest() {
  return { method: 'GET', url: '{{baseUrl}}/', pathParams: [], query: [], headers: '', bodyType: 'none', body: '', form: [], auth: '', operation: '' };
}

function currentSpec() {
  return state.specs[state.specIndex] || null;
}

function opById(id) {
  for (const s of state.specs) for (const o of s.operations) if (o.id === id) return { op: o, spec: s };
  return null;
}

function serverFor(spec) {
  if (!spec || !spec.servers.length) return '';
  const saved = store.get('server:' + spec.path);
  return spec.servers.includes(saved) ? saved : spec.servers[0];
}

function requestFromOp(op) {
  return {
    method: op.method,
    url: '{{baseUrl}}' + op.path,
    pathParams: op.params.filter((p) => p.in === 'path').map((p) => ({ name: p.name, value: p.example || '', enabled: true })),
    query: op.params.filter((p) => p.in === 'query').map((p) => ({ name: p.name, value: p.example || '', enabled: p.required })),
    headers: op.headers,
    bodyType: op.bodyType,
    body: op.body,
    form: op.form.map((f) => ({ ...f })),
    auth: '',
    operation: op.id,
  };
}

function activeTab() {
  return state.tabs.find((t) => t.id === state.active) || null;
}

function tabTitle(t) {
  const found = t.req.operation && opById(t.req.operation);
  if (found && found.op.summary) return found.op.summary;
  return t.req.method + ' ' + t.req.url.replace('{{baseUrl}}', '');
}

function newTab(req, server) {
  const tab = { id: 't' + tabSeq++, req: req || blankRequest(), server: server || serverFor(currentSpec()), sub: 'params', response: null, error: '', sending: false, resTab: 'body', bodyView: 'pretty', find: '', dirty: false };
  state.tabs.push(tab);
  state.active = tab.id;
  renderTabs();
  renderRequest();
  renderResponse();
  return tab;
}

function openOperation(op, spec) {
  const tab = activeTab();
  const req = requestFromOp(op);
  if (tab && !tab.dirty && !tab.response && !tab.sending) {
    Object.assign(tab, { req, server: serverFor(spec), sub: op.params.length ? 'params' : (op.bodyType !== 'none' ? 'body' : 'headers'), error: '' });
    renderTabs();
    renderRequest();
    renderResponse();
  } else {
    newTab(req, serverFor(spec)).sub = op.params.length ? 'params' : (op.bodyType !== 'none' ? 'body' : 'headers');
    renderRequest();
  }
  renderSide();
}

function closeTab(id) {
  const i = state.tabs.findIndex((t) => t.id === id);
  if (i < 0) return;
  const t = state.tabs[i];
  if (t.sending) api().Cancel(t.reqId);
  state.tabs.splice(i, 1);
  if (state.active === id) state.active = (state.tabs[i] || state.tabs[i - 1] || {}).id || null;
  renderTabs();
  renderRequest();
  renderResponse();
}

function wireRequest(tab) {
  const r = tab.req;
  tab.reqId = tab.id + '-' + Date.now();
  return {
    id: tab.reqId,
    method: r.method,
    url: r.url,
    pathParams: (r.pathParams || []).map((p) => ({ name: p.name, value: p.value || '', enabled: true })),
    query: (r.query || []).map((q) => ({ name: q.name || '', value: q.value || '', enabled: !!q.enabled })),
    headers: r.headers || '',
    bodyType: r.bodyType || 'none',
    body: r.body || '',
    form: (r.form || []).map((f) => ({ name: f.name || '', value: f.value || '', file: !!f.file, enabled: !!f.enabled })),
    auth: r.auth || '',
    operation: r.operation || '',
    server: tab.server || '',
    allowInsecure: false,
    allowHost: '',
  };
}

function urlHost(u) {
  return (u.match(/^https?:\/\/([^/?#]+)/) || [])[1] || '';
}

async function send(tab) {
  if (!tab) return;
  if (tab.sending) {
    api().Cancel(tab.reqId);
    return;
  }
  const req = wireRequest(tab);
  let preview = await api().Preview(req);
  if (preview.blockedBy === 'unlisted') {
    const host = urlHost(preview.url);
    const ok = await confirmDialog('Send a secret to an unlisted host?', `No environment lists ${host} in its hosts, and the URL can come from the spec file. Send your secret only if you trust ${host}.`, 'Send anyway', true);
    if (!ok) return;
    req.allowHost = host;
    preview = await api().Preview(req);
  }
  if (preview.blockedBy === 'insecure') {
    const host = urlHost(preview.url) || 'this host';
    const ok = await confirmDialog('Send a secret without TLS?', `This request sends a secret to ${host} over plain http. Anyone on the network path can read it.`, 'Send anyway', true);
    if (!ok) return;
    req.allowInsecure = true;
  }
  const env = (state.ws.environments || []).find((e) => e.name === state.ws.environment);
  if (env && env.production) {
    const ok = await confirmDialog('Send to production?', `Environment "${env.name}" looks like production. Send ${req.method} anyway?`, 'Send', true);
    if (!ok) return;
  }
  tab.sending = true;
  tab.error = '';
  renderTabs();
  renderRequestButton();
  renderResponse();
  try {
    tab.response = await api().Send(req);
    tab.resTab = 'body';
    tab.find = '';
  } catch (e) {
    tab.error = errText(e);
    tab.response = null;
  } finally {
    tab.sending = false;
    renderTabs();
    renderRequestButton();
    renderResponse();
    state.history = await api().History();
    if (state.sideTab === 'history') renderSide();
  }
}

// ---------- Rendering: top bar and banners ----------

function basename(p) {
  return String(p || '').split(/[\\/]/).filter(Boolean).pop() || p;
}

function needsAWS() {
  const ws = state.ws;
  if (!ws) return false;
  return ws.variables.some((v) => v.active && (v.kind === 'aws' || v.kind === 'ssm'));
}

function renderTop() {
  const ws = state.ws;
  $('folder').textContent = ws && ws.root ? basename(ws.root) : 'No folder open';
  $('folder').title = (ws && ws.root) || '';
  $('rescan').disabled = !(ws && ws.root);

  const recent = $('recent');
  recent.replaceChildren(h('option', { value: '' }, 'Recent'), ...((ws && ws.recent) || []).map((p) => h('option', { value: p, title: p }, basename(p) + ' — ' + p)));
  recent.value = '';

  const env = $('env');
  const envs = (ws && ws.environments) || [];
  env.replaceChildren(h('option', { value: '' }, envs.length ? 'No environment' : 'No environments'), ...envs.map((e) => h('option', { value: e.name }, e.name + (e.production ? ' (production)' : ''))));
  env.value = (ws && ws.environment) || '';
  env.disabled = !envs.length;
  env.classList.toggle('prod', !!envs.find((e) => e.name === env.value && e.production));

  const pill = $('aws');
  const aws = ws && ws.aws;
  pill.classList.toggle('hidden', !needsAWS());
  pill.className = 'pill' + (needsAWS() ? '' : ' hidden');
  if (aws) {
    const where = aws.profile ? ` (${aws.profile})` : '';
    if (aws.state === 'loaded') {
      pill.textContent = `AWS${where} · ${aws.count} loaded · ${aws.loadedAt}`;
      pill.classList.add(aws.error ? 'warn' : 'ok');
      pill.title = aws.error || 'Click to reload or forget secrets';
    } else if (aws.state === 'signed-out') {
      pill.textContent = `AWS${where} · sign in`;
      pill.classList.add('warn');
      pill.title = aws.error;
    } else if (aws.state === 'error') {
      pill.textContent = `AWS${where} · error`;
      pill.classList.add('err');
      pill.title = aws.error;
    } else {
      pill.textContent = `AWS${where} · load secrets`;
      pill.title = aws.cli ? 'Uses ' + aws.cli : 'The aws CLI was not found. Set its path in Settings.';
    }
  }
  document.documentElement.dataset.theme = (ws && ws.settings.theme) || 'system';
  document.documentElement.style.setProperty('--fs', ((ws && ws.settings.fontSize) || 13) + 'px');
}

function renderBanners() {
  const ws = state.ws;
  const out = [];
  if (state.update && state.update.available) {
    out.push(h('div', { class: 'banner' },
      h('span', { class: 'grow' }, `Bikasend ${state.update.latest} is available (you have ${state.update.current}).`),
      h('button', { class: 'btn primary', onclick: () => call('OpenURL', state.update.url) }, 'Download'),
      h('button', { class: 'link', onclick: () => { state.update = null; renderBanners(); } }, 'Later')));
  }
  if (ws && ws.configFile && !ws.trusted && !ws.configError) {
    out.push(h('div', { class: 'banner warn' },
      h('span', { class: 'grow' }, 'This folder has a .bika.json. It decides which hosts get your secrets, so it stays off until you trust it.'),
      h('button', { class: 'btn primary', onclick: trustDialog }, 'Review and trust')));
  }
  if (ws && ws.configError) {
    out.push(h('div', { class: 'banner err' }, h('span', { class: 'grow' }, '.bika.json: ' + errText(ws.configError))));
  }
  if (ws && ws.prompts.length) {
    out.push(h('div', { class: 'banner' },
      h('span', { class: 'grow' }, `${ws.prompts.length} value${ws.prompts.length > 1 ? 's' : ''} needed for this session: ${ws.prompts.join(', ')}`),
      h('button', { class: 'btn primary', onclick: promptsDialog }, 'Enter values')));
  }
  $('banners').replaceChildren(...out);
}

// ---------- Rendering: sidebar ----------

function renderSide() {
  document.querySelectorAll('.side-tab').forEach((b) => b.classList.toggle('active', b.dataset.tab === state.sideTab));
  const body = $('sideBody');
  body.replaceChildren();
  if (state.sideTab === 'history') return renderHistory(body);
  if (!state.ws || !state.ws.root) {
    body.append(h('p', { class: 'empty' }, 'Open a folder to see its operations.'));
    return;
  }
  if (!state.specs.length) {
    body.append(h('p', { class: 'empty' }, 'No swagger or openapi files found. Is this the repo root? Bikasend looks for *swagger*.json, *openapi*.json and their .yaml versions.'));
    return;
  }
  const specSel = h('select', { 'aria-label': 'Spec file', onchange: (e) => selectSpec(Number(e.target.value)) },
    state.specs.map((s, i) => h('option', { value: String(i), selected: i === state.specIndex }, (s.title || s.path) + (s.error ? ' ⚠' : '') + ' — ' + s.path)));
  body.append(specSel);
  const spec = currentSpec();
  if (!spec) return;
  if (spec.servers.length > 1) {
    body.append(h('select', {
      'aria-label': 'Server', title: 'Fills {{baseUrl}} when no scope defines it',
      onchange: (e) => {
        store.set('server:' + spec.path, e.target.value);
        for (const t of state.tabs) if (t.req.operation && t.req.operation.startsWith(spec.path + ' ')) t.server = e.target.value;
        refreshVarMarks();
      },
    }, spec.servers.map((s) => h('option', { value: s, selected: s === serverFor(spec) }, s))));
  }
  if (spec.error) body.append(h('div', { class: 'notice err' }, spec.error));
  const filter = h('input', {
    type: 'search', placeholder: 'Filter operations', 'aria-label': 'Filter operations', value: state.filter,
    oninput: (e) => { state.filter = e.target.value; renderOps(list); },
  });
  const list = h('div', { class: 'ops' });
  body.append(filter, list);
  renderOps(list);
}

function renderOps(list) {
  const spec = currentSpec();
  const q = state.filter.toLowerCase();
  const groups = new Map();
  for (const op of spec.operations) {
    const hay = (op.method + ' ' + op.path + ' ' + op.summary + ' ' + op.tag).toLowerCase();
    if (q && !q.split(/\s+/).every((w) => hay.includes(w))) continue;
    const tag = op.tag || 'Other';
    if (!groups.has(tag)) groups.set(tag, []);
    groups.get(tag).push(op);
  }
  const active = activeTab();
  const activeOp = active && active.req.operation;
  list.replaceChildren(...[...groups.entries()].sort((a, b) => a[0].localeCompare(b[0])).map(([tag, ops]) => {
    const key = 'tag:' + spec.path + ':' + tag;
    const details = h('details', { class: 'tag', open: q !== '' || store.get(key) !== 'closed' },
      h('summary', {}, h('span', {}, tag, h('span', { class: 'badge' }, String(ops.length)))),
      ops.map((op) => h('button', {
        class: 'op' + (op.id === activeOp ? ' active' : '') + (op.deprecated ? ' deprecated' : ''),
        title: op.description || op.summary || op.path,
        onclick: () => openOperation(op, spec),
      },
      h('span', { class: 'm ' + op.method }, op.method),
      h('span', { class: 'path' }, op.path),
      op.deprecated ? h('span', { class: 'badge warn' }, 'deprecated') : null,
      op.summary ? h('span', { class: 'summary' }, op.summary) : null)));
    details.addEventListener('toggle', () => { if (!q) store.set(key, details.open ? 'open' : 'closed'); });
    return details;
  }));
  if (!groups.size) list.append(h('p', { class: 'empty' }, 'No operation matches.'));
}

function renderHistory(body) {
  body.append(h('div', { class: 'row-actions' },
    h('span', { class: 'muted grow' }, state.history.length ? `${state.history.length} requests` : ''),
    h('button', { class: 'btn', disabled: !state.history.length, onclick: async () => { await call('ClearHistory'); state.history = []; renderSide(); } }, 'Clear')));
  if (!state.history.length) {
    body.append(h('p', { class: 'empty' }, 'Requests you send appear here. ' + (state.ws && state.ws.settings.saveHistory ? 'History is saved to disk with secret-looking values masked.' : 'History stays in memory unless you turn on saving in Settings.')));
    return;
  }
  for (const e of state.history) {
    const cls = e.status >= 500 ? 'err' : e.status >= 400 ? 'warn' : '';
    body.append(h('button', { class: 'history-item', title: 'Open in a new tab', onclick: () => newTab(JSON.parse(JSON.stringify(e.request))) },
      h('span', { class: 'm ' + e.request.method }, e.request.method),
      h('span', { class: 'badge ' + cls }, String(e.status)),
      h('span', { class: 'muted small' }, ' ' + new Date(e.time).toLocaleTimeString() + (e.environment ? ' · ' + e.environment : '') + ' · ' + e.durationMs + ' ms'),
      h('span', { class: 'url' }, e.request.url)));
  }
}

function selectSpec(i) {
  state.specIndex = i;
  const spec = currentSpec();
  if (spec && state.ws) store.set('spec:' + state.ws.root, spec.path);
  state.filter = '';
  renderSide();
}

// ---------- Rendering: request ----------

function renderTabs() {
  const el = $('tabs');
  el.replaceChildren(...state.tabs.map((t) => h('div', {
    class: 'tab' + (t.id === state.active ? ' active' : ''), role: 'tab', tabIndex: 0, title: tabTitle(t),
    onclick: () => { state.active = t.id; renderTabs(); renderRequest(); renderResponse(); renderSide(); },
    onkeydown: (e) => { if (e.key === 'Enter') e.currentTarget.click(); },
  },
  t.sending ? h('span', { class: 'spin' }, '●') : null,
  h('span', { class: 'm ' + t.req.method }, t.req.method),
  h('span', { class: 'title' }, tabTitle(t)),
  h('button', { class: 'close', 'aria-label': 'Close tab', onclick: (e) => { e.stopPropagation(); closeTab(t.id); } }, '✕'))),
  h('button', { class: 'tab', title: `New tab (${MOD}T)`, 'aria-label': 'New tab', onclick: () => newTab() }, '+'));
}

function renderRequestButton() {
  const tab = activeTab();
  const btn = $('sendBtn');
  if (!btn || !tab) return;
  btn.textContent = tab.sending ? 'Cancel' : 'Send';
  btn.className = 'btn ' + (tab.sending ? 'danger' : 'primary');
}

function renderRequest() {
  const el = $('request');
  el.replaceChildren();
  const tab = activeTab();
  if (!tab) {
    el.append(welcome());
    return;
  }
  const r = tab.req;
  const found = r.operation && opById(r.operation);
  const op = found && found.op;

  const method = h('select', { 'aria-label': 'Method', onchange: (e) => { r.method = e.target.value; tab.dirty = true; renderTabs(); } },
    METHODS.map((m) => h('option', { value: m, selected: m === r.method }, m)));
  const url = varField({ value: r.url, label: 'URL', placeholder: 'https://…', tab, className: 'url', onInput: (v) => { r.url = v; renderTabs(); } });
  const sendBtn = h('button', { id: 'sendBtn', class: 'btn primary', title: `Send (${MOD}Enter)`, onclick: () => send(tab) }, 'Send');
  el.append(h('div', { class: 'urlbar' }, method, url, sendBtn));
  renderRequestButton();

  if (op && (op.deprecated || op.summary)) {
    el.append(h('div', { class: 'muted small' }, op.deprecated ? h('span', { class: 'badge warn' }, 'deprecated') : null, ' ', op.summary));
  }

  const subs = [['params', 'Params', r.pathParams.length + r.query.length], ['headers', 'Headers'], ['body', 'Body', r.bodyType !== 'none' ? '●' : ''], ['auth', 'Auth', r.auth ? '●' : ''], ['resolved', 'Resolved']];
  if (op) subs.push(['docs', 'Docs']);
  el.append(h('div', { class: 'subtabs', role: 'tablist' }, subs.map(([key, label, extra]) => h('button', {
    class: 'subtab' + (tab.sub === key ? ' active' : ''), role: 'tab',
    onclick: () => { tab.sub = key; renderRequest(); },
  }, label, extra ? h('span', { class: 'badge' }, String(extra)) : null))));

  const pane = h('div', { class: 'pane' });
  el.append(pane);
  ({ params: paneParams, headers: paneHeaders, body: paneBody, auth: paneAuth, resolved: paneResolved, docs: paneDocs }[tab.sub] || paneParams)(pane, tab, op);
  el.append(h('div', { class: 'chips', id: 'chips' }));
  renderChips();
}

function paneParams(pane, tab, op) {
  const r = tab.req;
  const docs = new Map(((op && op.params) || []).map((p) => [p.in + ':' + p.name, p]));
  if (r.pathParams.length) {
    pane.append(h('table', { class: 'kv' },
      h('thead', {}, h('tr', {}, h('th', {}, 'Path parameter'), h('th', {}, 'Value'))),
      h('tbody', {}, r.pathParams.map((p) => {
        const d = docs.get('path:' + p.name);
        return h('tr', {},
          h('td', { title: (d && d.description) || '' }, h('span', { class: 'mono' }, p.name), h('span', { class: 'req-mark', title: 'Required' }, '*')),
          h('td', {}, varField({ value: p.value, label: 'Value of ' + p.name, tab, onInput: (v) => { p.value = v; } })));
      }))));
  }
  const tbody = h('tbody');
  const drawRows = () => tbody.replaceChildren(...r.query.map((q, i) => {
    const d = docs.get('query:' + q.name);
    return h('tr', {},
      h('td', { class: 'narrow' }, h('input', { type: 'checkbox', checked: q.enabled, 'aria-label': 'Send ' + q.name, onchange: (e) => { q.enabled = e.target.checked; tab.dirty = true; renderChips(); } })),
      h('td', { title: (d && d.description) || '' }, h('input', { type: 'text', value: q.name, 'aria-label': 'Query name', oninput: (e) => { q.name = e.target.value; tab.dirty = true; } }), d && d.required ? h('span', { class: 'req-mark', title: 'Required' }, '*') : null),
      h('td', {}, varField({ value: q.value, label: 'Value of ' + q.name, tab, onInput: (v) => { q.value = v; } })),
      h('td', { class: 'narrow' }, h('button', { class: 'icon-btn', 'aria-label': 'Remove ' + q.name, onclick: () => { r.query.splice(i, 1); drawRows(); renderChips(); } }, '✕')));
  }));
  drawRows();
  pane.append(h('table', { class: 'kv' }, h('thead', {}, h('tr', {}, h('th', {}), h('th', {}, 'Query parameter'), h('th', {}, 'Value'), h('th', {}))), tbody),
    h('div', { class: 'row-actions' }, h('button', { class: 'btn', onclick: () => { r.query.push({ name: '', value: '', enabled: true }); drawRows(); } }, '+ Query parameter')));
  if (!r.pathParams.length && !r.query.length) pane.append(h('p', { class: 'muted' }, 'This request has no parameters. Path parameters like {id} in the URL appear here when you open an operation.'));
}

const COMMON_HEADERS = ['Accept', 'Accept-Encoding', 'Accept-Language', 'Authorization', 'Cache-Control', 'Content-Type', 'Cookie',
  'Idempotency-Key', 'If-Match', 'If-None-Match', 'Origin', 'Prefer', 'Range', 'User-Agent', 'X-API-Key', 'X-Correlation-ID', 'X-Request-ID'];

function parseHeaders(text) {
  return String(text || '').split('\n').filter((l) => l.trim() !== '').map((l) => {
    const i = l.indexOf(':');
    return i < 0 ? { name: l.trim(), value: '', enabled: true } : { name: l.slice(0, i).trim(), value: l.slice(i + 1).trim(), enabled: true };
  });
}

// headerText is what Go receives: enabled rows only, one "Name: value" per line.
// A header name cannot hold ':' or whitespace; Go would split the line at the first ':'.
function headerText(rows) {
  return rows.map((r) => ({ ...r, name: r.name.replace(/[:\s]/g, '') }))
    .filter((r) => r.enabled && r.name).map((r) => `${r.name}: ${r.value}`).join('\n');
}

function paneHeaders(pane, tab) {
  const r = tab.req;
  const toggle = h('button', { class: 'link', onclick: () => { tab.headersBulk = !tab.headersBulk; renderRequest(); } }, tab.headersBulk ? 'Edit as a table' : 'Bulk edit');
  if (tab.headersBulk) {
    pane.append(varField({ value: r.headers, multiline: true, label: 'Headers', placeholder: 'Name: value', tab, onInput: (v) => { r.headers = v; } }),
      h('div', { class: 'row-actions' }, h('span', { class: 'muted small grow' }, 'One "Name: value" per line. Lines with an empty value are not sent. Type {{ for variables.'), toggle));
    return;
  }
  // Rows live on the tab; reparse when the text changed elsewhere (operation, cURL import, history, bulk edit).
  if (!tab.headerRows || tab.headerSrc !== r.headers) {
    tab.headerRows = parseHeaders(r.headers);
    tab.headerSrc = r.headers;
  }
  const rows = tab.headerRows;
  const sync = () => {
    r.headers = headerText(rows);
    tab.headerSrc = r.headers;
    tab.dirty = true;
    renderChips();
  };
  if (!$('header-names')) document.body.append(h('datalist', { id: 'header-names' }, COMMON_HEADERS.map((n) => h('option', { value: n }))));
  const tbody = h('tbody');
  const draw = () => tbody.replaceChildren(...rows.map((row, i) => h('tr', { class: row.enabled ? '' : 'off' },
    h('td', { class: 'narrow' }, h('input', { type: 'checkbox', checked: row.enabled, 'aria-label': 'Send ' + (row.name || 'header'), onchange: (e) => { row.enabled = e.target.checked; sync(); draw(); } })),
    h('td', { class: 'hname-cell' }, h('input', { type: 'text', value: row.name, list: 'header-names', spellcheck: 'false', placeholder: 'Header', 'aria-label': 'Header name', oninput: (e) => { row.name = e.target.value; sync(); } })),
    h('td', {}, varField({ value: row.value, label: 'Value of ' + (row.name || 'header'), placeholder: 'Value', tab, onInput: (v) => { row.value = v; sync(); } })),
    h('td', { class: 'narrow' }, h('button', { class: 'icon-btn', 'aria-label': 'Remove ' + (row.name || 'header'), onclick: () => { rows.splice(i, 1); sync(); draw(); } }, '✕')))));
  draw();
  pane.append(
    h('table', { class: 'kv headers-edit' }, h('thead', {}, h('tr', {}, h('th', {}), h('th', {}, 'Header'), h('th', {}, 'Value'), h('th', {}))), tbody),
    h('div', { class: 'row-actions' },
      h('button', { class: 'btn', onclick: () => { rows.push({ name: '', value: '', enabled: true }); draw(); const last = tbody.querySelector('tr:last-child input[type=text]'); if (last) last.focus(); } }, '+ Header'),
      h('span', { class: 'muted small grow' }, 'Rows with an empty value are not sent. Type {{ for variables.'),
      toggle));
  if (!rows.length) pane.append(h('p', { class: 'muted' }, 'No headers yet. Auth headers from the spec appear here when you open an operation.'));
}

function paneBody(pane, tab) {
  const r = tab.req;
  const type = h('select', { 'aria-label': 'Body type', onchange: (e) => { r.bodyType = e.target.value; tab.dirty = true; renderRequest(); } },
    BODY_TYPES.map(([v, l]) => h('option', { value: v, selected: v === r.bodyType }, l)));
  const actions = h('div', { class: 'row-actions' }, type);
  pane.append(actions);
  if (r.bodyType === 'none') {
    pane.append(h('p', { class: 'muted' }, 'No body is sent.'));
    return;
  }
  if (['json', 'text', 'xml'].includes(r.bodyType)) {
    if (r.bodyType === 'json') {
      actions.append(h('button', { class: 'btn', onclick: () => {
        try {
          r.body = JSON.stringify(JSON.parse(r.body), null, 2);
          renderRequest();
        } catch { toast('The body is not valid JSON (variables outside strings break JSON).', true); }
      } }, 'Format JSON'));
    }
    pane.append(varField({ value: r.body, multiline: true, label: 'Body', tab, onInput: (v) => { r.body = v; } }));
    return;
  }
  const tbody = h('tbody');
  const draw = () => tbody.replaceChildren(...r.form.map((f, i) => h('tr', {},
    h('td', { class: 'narrow' }, h('input', { type: 'checkbox', checked: f.enabled, 'aria-label': 'Send ' + f.name, onchange: (e) => { f.enabled = e.target.checked; renderChips(); } })),
    h('td', {}, h('input', { type: 'text', value: f.name, 'aria-label': 'Field name', oninput: (e) => { f.name = e.target.value; } })),
    h('td', {}, f.file
      ? h('div', { class: 'row-actions' }, h('span', { class: 'mono small' }, f.value ? basename(f.value) : 'No file chosen'),
        h('button', { class: 'btn', onclick: async () => { const p = await call('PickFile'); if (p) { f.value = p; f.enabled = true; draw(); } } }, 'Choose…'))
      : varField({ value: f.value, label: 'Value of ' + f.name, tab, onInput: (v) => { f.value = v; } })),
    h('td', { class: 'narrow' }, h('button', { class: 'icon-btn', 'aria-label': 'Remove field', onclick: () => { r.form.splice(i, 1); draw(); } }, '✕')))));
  draw();
  pane.append(h('table', { class: 'kv' }, h('thead', {}, h('tr', {}, h('th', {}), h('th', {}, 'Field'), h('th', {}, 'Value'), h('th', {}))), tbody),
    h('div', { class: 'row-actions' },
      h('button', { class: 'btn', onclick: () => { r.form.push({ name: '', value: '', file: false, enabled: true }); draw(); } }, '+ Field'),
      r.bodyType === 'multipart' ? h('button', { class: 'btn', onclick: () => { r.form.push({ name: 'file', value: '', file: true, enabled: true }); draw(); } }, '+ File') : null));
}

function paneAuth(pane, tab) {
  const env = (state.ws.environments || []).find((e) => e.name === state.ws.environment);
  const opts = [
    ['', 'None or headers', 'Send what the Headers tab says, for example Authorization: Bearer {{token}}.', true],
    ['oauth2', 'OAuth2 client credentials', env && env.oauth2 ? 'Bikasend fetches a token with the environment\'s oauth2 settings and keeps it in memory until it expires.' : 'Add an "oauth2" block (tokenUrl, clientId, clientSecret) to this environment in .bika.json.', env && env.oauth2],
    ['sigv4', 'AWS Signature V4', env && env.sigv4 ? 'Bikasend signs the request with the credentials of the environment\'s AWS profile.' : 'Add a "sigv4" block (service, region) and an "aws" profile to this environment in .bika.json.', env && env.sigv4],
  ];
  for (const [value, label, help, enabled] of opts) {
    pane.append(h('label', { class: 'row-actions' },
      h('input', { type: 'radio', name: 'auth-' + tab.id, value, checked: tab.req.auth === value, disabled: !enabled, onchange: () => { tab.req.auth = value; tab.dirty = true; renderTabs(); } }),
      h('span', {}, h('strong', {}, label), h('br'), h('span', { class: 'muted small' }, help))));
  }
}

async function paneResolved(pane, tab) {
  pane.append(h('p', { class: 'muted' }, 'Resolving…'));
  const p = await api().Preview(wireRequest(tab));
  pane.replaceChildren();
  if (p.error) pane.append(h('div', { class: 'notice err' }, errText(p.error)));
  if (p.warning) pane.append(h('div', { class: 'notice warn' }, errText(p.warning)));
  if (p.missing && p.missing.length) pane.append(h('div', { class: 'notice warn' }, 'Not filled in: ' + p.missing.join(', ')));
  pane.append(h('pre', {}, [p.method + ' ' + p.url, p.headers, p.body ? '\n' + p.body : ''].filter(Boolean).join('\n')),
    h('p', { class: 'muted small' }, p.secret ? 'Secret values are masked here. They are only filled in when the request is sent.' : 'This request uses no secret values.'),
    h('div', { class: 'row-actions' }, h('button', { class: 'btn', onclick: () => renderRequest() }, 'Refresh')));
}

function paneDocs(pane, tab, op) {
  const d = h('div', { class: 'docs' });
  if (op.description) d.append(h('p', {}, op.description));
  if (op.params.length) {
    d.append(h('h3', {}, 'Parameters'), h('table', { class: 'kv' },
      h('thead', {}, h('tr', {}, h('th', {}, 'Name'), h('th', {}, 'In'), h('th', {}, 'Description'))),
      h('tbody', {}, op.params.map((p) => h('tr', {},
        h('td', { class: 'mono' }, p.name, p.required ? h('span', { class: 'req-mark' }, '*') : null),
        h('td', {}, p.in), h('td', {}, p.description))))));
  }
  if (op.responses.length) {
    d.append(h('h3', {}, 'Responses'));
    for (const r of op.responses) {
      d.append(h('p', {}, h('strong', {}, r.status), ' ', r.description));
      if (r.example) d.append(h('pre', {}, r.example));
    }
  }
  if (!d.childNodes.length) d.append(h('p', { class: 'muted' }, 'The spec has no extra documentation for this operation.'));
  pane.append(d);
}

function renderChips() {
  const el = $('chips');
  const tab = activeTab();
  if (!el || !tab) return;
  const names = usedVars(tab);
  el.replaceChildren(...names.map((n) => {
    const st = varStatus(n, tab);
    const v = activeVars().get(n);
    const chip = h('button', { class: 'chip ' + st, 'aria-label': `{{${n}}}, ${st}`, onclick: () => openVariable(n) }, (st === 'secret' ? '🔒 ' : st === 'missing' ? '⚠ ' : '') + n);
    const show = () => showVarCardSoon(n, chip.getBoundingClientRect(), tab);
    chip.addEventListener('mouseenter', show);
    chip.addEventListener('focus', show);
    chip.addEventListener('mouseleave', hideVarCardSoon);
    chip.addEventListener('blur', hideVarCardSoon);
    return chip;
  }));
}

// ---------- Variable card ----------
// Shows where a {{variable}} comes from. Secrets stay masked until an explicit Reveal click.

const MASK = '••••••••';
const varCard = { el: null, name: '', anchor: null, showTimer: 0, hideTimer: 0, revealTimer: 0 };

// onCardPath is true between the variable and its card, so moving the mouse across keeps it open.
function onCardPath(x, y) {
  if (!varCard.el || !varCard.anchor) return false;
  const a = varCard.anchor;
  const c = varCard.el.getBoundingClientRect();
  return x >= Math.min(a.left, c.left) - 4 && x <= Math.max(a.right, c.right) + 4
    && y >= Math.min(a.top, c.top) - 4 && y <= Math.max(a.bottom, c.bottom) + 4;
}

// varAtPoint finds the highlighted {{variable}} under the pointer; the overlay sits under the input.
function varAtPoint(hl, x, y) {
  for (const m of hl.querySelectorAll('mark')) {
    for (const r of m.getClientRects()) {
      if (x >= r.left && x <= r.right && y >= r.top && y <= r.bottom) {
        const name = (m.textContent.match(/^\{\{\s*([A-Za-z0-9_.$-]+)\s*\}\}$/) || [])[1];
        if (name) return { name, rect: r };
      }
    }
  }
  return null;
}

function showVarCardSoon(name, rect, tab) {
  clearTimeout(varCard.hideTimer);
  if (varCard.el && varCard.name === name) return;
  clearTimeout(varCard.showTimer);
  varCard.showTimer = setTimeout(() => showVarCard(name, rect, tab), varCard.el ? 0 : 250);
}

function hideVarCardSoon() {
  clearTimeout(varCard.showTimer);
  clearTimeout(varCard.hideTimer);
  varCard.hideTimer = setTimeout(hideVarCard, 400);
}

function hideVarCard() {
  clearTimeout(varCard.showTimer);
  clearTimeout(varCard.hideTimer);
  clearTimeout(varCard.revealTimer);
  if (varCard.el) varCard.el.remove();
  varCard.el = null;
  varCard.name = '';
  varCard.anchor = null;
}

function showVarCard(name, rect, tab) {
  hideVarCard();
  const v = activeVars().get(name);
  const st = varStatus(name, tab);
  const fromSpec = name === 'baseUrl' && !v && tab && tab.server;
  const rows = [];
  const actions = [];

  let label = 'Not defined';
  if (fromSpec) label = 'From the spec server';
  else if (v) label = `${v.scope === 'environment' ? 'Env: ' + (state.ws.environment || '') : SCOPE_LABELS[v.scope]} · ${KIND_LABELS[v.kind] || v.kind}`;
  rows.push(h('div', { class: 'vc-meta' }, label));
  if (v && v.ref) rows.push(h('div', { class: 'vc-ref mono' }, v.kind === 'prompt' ? 'Asks: ' + v.ref : v.ref));

  if (fromSpec) {
    rows.push(h('div', { class: 'vc-value mono' }, tab.server));
  } else if (!v || !v.resolved) {
    rows.push(h('div', { class: 'vc-note' }, (v && v.error) || 'Not defined in any scope.'));
  } else if (v.secret) {
    const value = h('div', { class: 'vc-value mono masked' }, MASK);
    rows.push(value);
    actions.push(h('button', { class: 'btn', onclick: async () => {
      value.textContent = await call('Reveal', name);
      value.classList.remove('masked');
      clearTimeout(varCard.revealTimer);
      varCard.revealTimer = setTimeout(() => { value.textContent = MASK; value.classList.add('masked'); }, 10000);
    } }, 'Reveal'));
  } else {
    rows.push(h('div', { class: 'vc-value mono' }, v.value));
  }

  if (v && (v.kind === 'aws' || v.kind === 'ssm')) {
    actions.push(h('button', { class: 'btn', onclick: () => call('OpenSecretSource', name) }, 'Open in AWS'));
  }
  if (!fromSpec) {
    actions.unshift(h('button', { class: 'btn primary', onclick: () => openVariable(name) }, v ? 'Edit' : 'Add'));
  }

  const el = h('div', { class: 'var-card ' + st, role: 'dialog', 'aria-label': `{{${name}}}` },
    h('div', { class: 'vc-head' }, h('span', { class: 'vc-name mono' }, `{{${name}}}`), st === 'secret' ? h('span', { class: 'badge' }, 'secret') : st === 'missing' ? h('span', { class: 'badge err' }, 'missing') : null),
    ...rows,
    actions.length ? h('div', { class: 'vc-actions' }, ...actions) : null);
  el.addEventListener('mouseenter', () => clearTimeout(varCard.hideTimer));
  el.addEventListener('mouseleave', hideVarCardSoon);
  document.body.append(el);
  varCard.el = el;
  varCard.name = name;
  varCard.anchor = rect;

  const w = el.offsetWidth;
  const ht = el.offsetHeight;
  const left = Math.max(8, Math.min(rect.left, window.innerWidth - w - 8));
  const below = rect.bottom + 4;
  el.style.left = left + 'px';
  el.style.top = (below + ht > window.innerHeight - 8 ? Math.max(8, rect.top - ht - 4) : below) + 'px';
}

// ---------- Rendering: response ----------

function renderResponse() {
  const el = $('response');
  el.replaceChildren();
  const tab = activeTab();
  if (!tab) return;
  if (tab.sending) {
    el.append(h('div', { class: 'status' }, h('span', { class: 'muted' }, 'Sending…'), h('button', { class: 'btn danger', onclick: () => send(tab) }, 'Cancel')));
    return;
  }
  if (tab.error) {
    el.append(h('div', { class: 'notice err' }, tab.error));
    return;
  }
  const res = tab.response;
  if (!res) {
    el.append(h('p', { class: 'muted' }, `Press ${MOD}Enter to send. The response appears here.`));
    return;
  }
  const cls = res.statusCode < 300 ? 'ok' : res.statusCode < 400 ? 'warn' : 'err';
  const t = res.timing;
  const size = res.size < 1024 ? res.size + ' B' : (res.size / 1024).toFixed(1) + ' KB';
  el.append(h('div', { class: 'status' },
    h('span', { class: 'code ' + cls }, res.status),
    h('span', { class: 'timing', title: `DNS ${t.dnsMs} ms · connect ${t.connectMs} ms · TLS ${t.tlsMs} ms · first byte ${t.firstByteMs} ms · total ${t.totalMs} ms` },
      `${res.durationMs} ms · ${size}` + (t.firstByteMs ? ` · first byte ${t.firstByteMs} ms` : '')),
    res.truncated ? h('span', { class: 'badge warn' }, 'truncated at 10 MB') : null,
    res.redacted ? h('span', { class: 'badge', title: 'A loaded secret appeared in the response and was hidden.' }, 'secret redacted') : null,
    h('span', { class: 'spacer' }),
    h('button', { class: 'btn', onclick: () => call('CopyText', res.body || '').then(() => toast('Body copied')) , disabled: !res.body }, 'Copy'),
    h('button', { class: 'btn', onclick: copyCurl }, 'Copy as cURL'),
    h('button', { class: 'btn', onclick: () => call('SaveResponse', saveName(tab, res), res.bodyBase64 || res.body, !!res.bodyBase64) }, 'Save…')));

  const tabs = [['body', 'Body'], ['headers', 'Headers']];
  if (tab.req.operation) tabs.push(['check', 'Spec check', res.issues.length]);
  el.append(h('div', { class: 'subtabs' }, tabs.map(([k, l, n]) => h('button', {
    class: 'subtab' + (tab.resTab === k ? ' active' : ''), onclick: () => { tab.resTab = k; renderResponse(); },
  }, l, n ? h('span', { class: 'badge warn' }, String(n)) : (k === 'check' ? h('span', { class: 'badge' }, '✓') : null)))));

  if (tab.resTab === 'headers') {
    const rows = String(res.headers || '').split('\n').filter((l) => l.includes(':')).map((l) => {
      const i = l.indexOf(':');
      return [l.slice(0, i), l.slice(i + 1).trim()];
    });
    el.append(h('div', { class: 'resbody' }, rows.length
      ? h('table', { class: 'headers-view' }, h('tbody', {}, rows.map(([k, v]) => h('tr', {}, h('th', { scope: 'row' }, k), h('td', {}, v)))))
      : h('p', { class: 'muted pad' }, 'No headers.')));
  } else if (tab.resTab === 'check') {
    el.append(res.issues.length
      ? h('div', { class: 'resbody' }, h('ul', { class: 'issues' }, res.issues.map((i) => h('li', {}, i))))
      : h('div', { class: 'notice' }, 'The response matches the spec for status ' + res.statusCode + '.'));
  } else {
    renderBody(el, tab, res);
  }
}

function saveName(tab, res) {
  const ext = /json/.test(res.contentType) ? 'json' : /xml/.test(res.contentType) ? 'xml' : /html/.test(res.contentType) ? 'html'
    : (res.contentType.match(/^image\/([a-z0-9]+)/) || [])[1] || (res.bodyBase64 ? 'bin' : 'txt');
  return 'response.' + ext;
}

function renderBody(el, tab, res) {
  if (res.bodyBase64) {
    const ct = (res.contentType || '').split(';')[0].trim();
    if (/^image\/[a-z0-9.+-]+$/.test(ct)) {
      el.append(h('div', { class: 'resbody' }, h('img', { src: 'data:' + ct + ';base64,' + res.bodyBase64, alt: 'Response image' })));
    } else {
      el.append(h('div', { class: 'notice' }, `Binary content (${ct || 'unknown type'}, ${res.size} bytes). Use Save… to keep it.`));
    }
    return;
  }
  let json;
  if (/json/.test(res.contentType) && res.body.length < 2_000_000) {
    try { json = JSON.parse(res.body); } catch { json = undefined; }
  }
  const find = h('input', { type: 'search', id: 'findBox', placeholder: `Find (${MOD}F)`, 'aria-label': 'Find in response', value: tab.find });
  const count = h('span', { class: 'muted small' });
  const bar = h('div', { class: 'row-actions' }, find, count);
  if (json !== undefined) {
    bar.prepend(h('div', { class: 'scopes' }, ['pretty', 'raw'].map((v) => h('button', {
      class: tab.bodyView === v ? 'active' : '', onclick: () => { tab.bodyView = v; renderResponse(); },
    }, v === 'pretty' ? 'Tree' : 'Raw'))));
  }
  const box = h('div', { class: 'resbody' });
  el.append(bar, box);
  let marks = [];
  let current = 0;
  const draw = () => {
    box.replaceChildren();
    marks = [];
    if (json !== undefined && tab.bodyView === 'pretty' && !tab.find) {
      box.append(jsonTree(json));
      count.textContent = '';
      return;
    }
    const pre = h('pre');
    const text = res.body;
    const q = tab.find;
    if (!q) {
      pre.textContent = text;
    } else {
      const lower = text.toLowerCase();
      const needle = q.toLowerCase();
      let i = 0;
      let at;
      while ((at = lower.indexOf(needle, i)) !== -1 && marks.length < 2000) {
        pre.append(text.slice(i, at));
        const m = h('mark', { class: 'find' }, text.slice(at, at + q.length));
        marks.push(m);
        pre.append(m);
        i = at + q.length;
      }
      pre.append(text.slice(i));
    }
    box.append(pre);
    count.textContent = q ? `${marks.length} match${marks.length === 1 ? '' : 'es'}` : '';
    current = 0;
    if (marks[0]) { marks[0].classList.add('current'); marks[0].scrollIntoView({ block: 'center' }); }
  };
  find.addEventListener('input', () => { tab.find = find.value; draw(); });
  find.addEventListener('keydown', (e) => {
    if (e.key !== 'Enter' || !marks.length) return;
    marks[current].classList.remove('current');
    current = (current + (e.shiftKey ? -1 : 1) + marks.length) % marks.length;
    marks[current].classList.add('current');
    marks[current].scrollIntoView({ block: 'center' });
  });
  draw();
}

function jsonTree(value) {
  let budget = 5000;
  const leaf = (v) => {
    if (v === null) return h('span', { class: 'z' }, 'null');
    if (typeof v === 'string') return h('span', { class: 's' }, JSON.stringify(v));
    if (typeof v === 'number') return h('span', { class: 'n' }, String(v));
    return h('span', { class: 'b' }, String(v));
  };
  const node = (key, v, depth) => {
    budget--;
    const label = key === null ? [] : [h('span', { class: 'k' }, JSON.stringify(key)), ': '];
    if (v === null || typeof v !== 'object') return h('div', { class: 'leaf' }, label, leaf(v));
    const isArr = Array.isArray(v);
    const entries = isArr ? v.map((x, i) => [i, x]) : Object.entries(v);
    const summary = h('summary', {}, label, isArr ? `[${entries.length}]` : `{${entries.length}}`);
    const d = h('details', { open: depth < 2 }, summary);
    for (const [k, x] of entries) {
      if (budget <= 0) { d.append(h('div', { class: 'leaf muted' }, '… switch to Raw to see the rest')); break; }
      d.append(node(isArr ? null : k, x, depth + 1));
    }
    return d;
  };
  return h('div', { class: 'tree' }, node(null, value, 0));
}

async function copyCurl() {
  const tab = activeTab();
  if (!tab) return;
  const cmd = await call('Curl', wireRequest(tab));
  await call('CopyText', cmd);
  toast(/Set first/.test(cmd) ? 'cURL copied. Secrets are $ENV references.' : 'cURL copied');
}

// ---------- Welcome ----------

function welcome() {
  const recent = (state.ws && state.ws.recent) || [];
  return h('div', { class: 'welcome' },
    h('h1', {}, 'Bikasend'),
    h('p', {}, 'Open a repo. Bikasend finds its Swagger and OpenAPI files and turns every operation into a request.'),
    h('button', { class: 'btn primary', onclick: pickFolder }, 'Open folder…'),
    state.ws && state.ws.root ? h('p', {}, h('button', { class: 'link', onclick: () => newTab() }, 'Start a blank request')) : null,
    recent.length ? h('div', { class: 'recent-list' }, h('p', { class: 'muted' }, 'Recent'), recent.map((p) => h('button', { class: 'btn', title: p, onclick: () => openFolder(p) }, basename(p) + ' — ' + p))) : null,
    h('p', { class: 'muted small' }, `${MOD}K commands · ${MOD}E variables · ${MOD}Enter send · hover a {{variable}} to see its source, ${isMac ? '⌘' : 'Ctrl'}-click to edit it`));
}

// ---------- Variables drawer ----------

function openDrawer(focusName, create) {
  const d = $('drawer');
  d.classList.add('open');
  d.setAttribute('aria-hidden', 'false');
  if (focusName) {
    state.varSearch = create ? '' : focusName;
    $('varSearch').value = state.varSearch;
    if (create) state.editing = { name: focusName, scope: defaultScope(), kind: 'text', value: '', key: '', label: '', isNew: true };
  }
  renderDrawer();
  if (!create) $('varSearch').focus();
}

// openVariable opens the drawer on a variable's editor, or on a new one when it is not defined.
function openVariable(name) {
  hideVarCard();
  const v = activeVars().get(name);
  openDrawer(name, !v);
  if (v) editVariable(v);
}

function closeDrawer() {
  const d = $('drawer');
  d.classList.remove('open');
  d.setAttribute('aria-hidden', 'true');
  state.editing = null;
}

function defaultScope() {
  const ws = state.ws;
  if (ws && ws.environment) return 'environment';
  if (ws && ws.trusted) return 'folder';
  return 'session';
}

function allowedScopes() {
  const ws = state.ws || {};
  const out = ['session'];
  // Without a .bika.json, saving to either shared scope creates one.
  const creatable = ws.root && !ws.configFile;
  if (ws.environment || creatable) out.push('environment');
  if (ws.trusted || creatable) out.push('folder');
  out.push('global');
  return out;
}

function renderDrawer() {
  const body = $('drawerBody');
  if (!body) return;
  body.replaceChildren();
  const ws = state.ws;
  if (!ws) return;

  if (ws.root && !ws.configFile) {
    body.append(h('div', { class: 'notice' },
      'Share variables with your team: save one to the environment or folder scope, and Bikasend creates .bika.json in the repo root with a local environment. It holds names and AWS references, never secret values.'));
  } else if (ws.configFile && !ws.trusted && !ws.configError) {
    body.append(h('div', { class: 'notice warn' }, 'Folder and environment variables are off until you trust this folder\'s .bika.json. ', h('button', { class: 'link', onclick: trustDialog }, 'Review and trust')));
  }

  if (needsAWS()) body.append(awsPanel());

  const scopes = [['active', 'In use'], ...allowedScopes().map((s) => [s, s === 'environment' ? 'Env: ' + (ws.environment || 'local') : SCOPE_LABELS[s]])];
  body.append(h('div', { class: 'row-actions' },
    h('div', { class: 'scopes' }, scopes.map(([k, l]) => h('button', { class: state.varScope === k ? 'active' : '', onclick: () => { state.varScope = k; renderDrawer(); } }, l))),
    h('span', { class: 'spacer' }),
    h('button', { class: 'btn primary', onclick: () => { state.editing = { name: '', scope: state.varScope !== 'active' ? state.varScope : defaultScope(), kind: 'text', value: '', key: '', label: '', isNew: true }; renderDrawer(); } }, '+ Add variable')));

  if (state.editing) body.append(varEditor());

  const q = state.varSearch.toLowerCase();
  const rows = ws.variables.filter((v) => (state.varScope === 'active' ? true : v.scope === state.varScope) && (!q || v.name.toLowerCase().includes(q)));
  if (!rows.length) {
    body.append(h('p', { class: 'empty' }, q ? 'No variable matches.' : 'No variables yet. Add one, or type {{name}} in a request and click its chip.'));
    return;
  }
  body.append(h('table', { class: 'vars' },
    h('thead', {}, h('tr', {}, h('th', {}, 'Name'), h('th', {}, 'Value'), h('th', {}, 'Source'), h('th', {}))),
    h('tbody', {}, rows.map(varRow))));
}

function varRow(v) {
  let value;
  if (state.revealed[v.name] !== undefined && v.active) value = h('span', { class: 'mono' }, state.revealed[v.name]);
  else if (v.secret) value = h('span', {}, v.resolved ? '••••••' : '', v.ref ? h('span', { class: 'muted small' }, (v.resolved ? ' ' : '') + v.ref) : null);
  else value = h('span', { class: 'mono' }, v.value);
  const err = v.error ? h('div', { class: 'small', title: v.error }, h('span', { class: 'badge err' }, '!'), ' ' + v.error) : null;
  const actions = h('td', { class: 'actions' });
  if (v.active && v.resolved && v.secret) {
    actions.append(h('button', { class: 'icon-btn', title: 'Reveal for 10 seconds', 'aria-label': 'Reveal ' + v.name, onclick: () => reveal(v.name) }, '👁'),
      h('button', { class: 'icon-btn', title: 'Copy value', 'aria-label': 'Copy ' + v.name, onclick: () => call('CopyVariable', v.name).then(() => toast('Copied ' + v.name)) }, '⧉'));
  }
  if (v.kind === 'prompt') {
    actions.append(h('button', { class: 'icon-btn', title: 'Enter this session\'s value', 'aria-label': 'Set ' + v.name, onclick: () => promptsDialog([v.name]) }, '⌨'));
  }
  if (v.scope !== 'spec') {
    actions.append(h('button', { class: 'icon-btn', title: 'Edit', 'aria-label': 'Edit ' + v.name, onclick: () => editVariable(v) }, '✎'),
      h('button', { class: 'icon-btn', title: 'Delete', 'aria-label': 'Delete ' + v.name, onclick: () => deleteVariable(v) }, '✕'));
  }
  return h('tr', { class: v.active ? '' : 'inactive' },
    h('td', {}, h('div', { class: 'name' }, v.secret ? '🔒 ' : '', v.name), v.shadows ? h('div', { class: 'muted small' }, 'overridden by ' + SCOPE_LABELS[v.shadows]) : null),
    h('td', { class: 'value' }, value, err),
    h('td', {}, h('div', {}, v.scope === 'environment' ? 'Env: ' + state.ws.environment : SCOPE_LABELS[v.scope]), h('div', { class: 'muted small' }, KIND_LABELS[v.kind] || v.kind)),
    actions);
}

async function reveal(name) {
  try {
    state.revealed[name] = await api().Reveal(name);
  } catch (e) { toast(errText(e), true); return; }
  renderDrawer();
  setTimeout(() => { delete state.revealed[name]; renderDrawer(); }, 10000);
}

function editVariable(v) {
  let value = v.value;
  let key = '';
  if (v.kind === 'aws') [value, key] = v.ref.replace(/^aws:/, '').split('#');
  if (v.kind === 'ssm') value = v.ref.replace(/^ssm:/, '');
  if (v.kind === 'env') value = v.ref.replace(/^env:/, '');
  if (v.kind === 'secret') value = '';
  state.editing = { name: v.name, scope: v.scope, kind: v.kind, value: value || '', key: key || '', label: v.kind === 'prompt' ? v.ref : '', isNew: false };
  renderDrawer();
}

async function deleteVariable(v) {
  const where = v.scope === 'folder' || v.scope === 'environment' ? ' It is removed from .bika.json.' : '';
  if (!(await confirmDialog('Delete ' + v.name + '?', `Delete {{${v.name}}} from the ${SCOPE_LABELS[v.scope].toLowerCase()} scope?${where}`, 'Delete', true))) return;
  applyWS(await call('DeleteVariable', v.scope, v.name));
}

function varEditor() {
  const e = state.editing;
  const kinds = e.scope === 'session' ? ['text', 'secret'] : ['text', 'prompt', 'aws', 'ssm', 'env'];
  if (!kinds.includes(e.kind)) e.kind = kinds[0];
  const field = (label, prop, placeholder, hint, type) => [
    h('label', { htmlFor: 've-' + prop }, label),
    h('input', { id: 've-' + prop, type: type || 'text', value: e[prop], placeholder, spellcheck: 'false', oninput: (ev) => { e[prop] = ev.target.value; } }),
    hint ? h('div', { class: 'hint' }, hint) : null,
  ];
  const fields = [];
  if (e.kind === 'text') fields.push(...field('Value', 'value', '', e.scope === 'session' ? '' : 'Saved in plain text in ' + (e.scope === 'global' ? 'your globals.json' : '.bika.json') + '. Never put a secret here.'));
  if (e.kind === 'secret') fields.push(...field('Value', 'value', '', 'Kept in memory only and masked everywhere.', 'password'));
  if (e.kind === 'prompt') fields.push(...field('Question', 'label', 'Local API key', 'Bikasend asks for the value once per session and keeps it in memory.'));
  if (e.kind === 'aws') fields.push(...field('Secret ID', 'value', 'example-service/api-keys', 'Name or ARN in AWS Secrets Manager.'), ...field('JSON key', 'key', 'bikasend', 'Optional: one field of a JSON secret.'));
  if (e.kind === 'ssm') fields.push(...field('Parameter', 'value', '/example/api-key', 'SecureString values are decrypted.'));
  if (e.kind === 'env') fields.push(...field('Variable', 'value', 'EXAMPLE_API_KEY', 'Read from the environment Bikasend was started in.'));

  const save = async () => {
    if (!e.name) { toast('Give the variable a name', true); return; }
    const ws = await call('SetVariable', e.scope, e.name, { kind: e.kind, value: e.value, key: e.key, label: e.label });
    state.editing = null;
    applyWS(ws);
    if (e.kind === 'aws' || e.kind === 'ssm') loadSecrets(false);
    toast('Saved {{' + e.name + '}}');
  };
  return h('form', { class: 'editor', onsubmit: (ev) => { ev.preventDefault(); save(); } },
    h('label', { htmlFor: 've-name' }, 'Name'),
    h('input', { id: 've-name', type: 'text', value: e.name, disabled: !e.isNew, placeholder: 'api_key', spellcheck: 'false', oninput: (ev) => { e.name = ev.target.value.trim(); } }),
    h('label', { htmlFor: 've-scope' }, 'Scope'),
    h('select', { id: 've-scope', disabled: !e.isNew, onchange: (ev) => { e.scope = ev.target.value; renderDrawer(); } },
      allowedScopes().map((s) => h('option', { value: s, selected: s === e.scope }, s === 'environment' ? 'Environment: ' + state.ws.environment : SCOPE_LABELS[s]))),
    h('div', { class: 'hint' }, { session: 'This session only. Nothing is saved.', environment: 'Saved in .bika.json under this environment. Shared with the team.', folder: 'Saved in .bika.json for every environment. Shared with the team.', global: 'Saved in your own globals.json. Applies to every folder.' }[e.scope]),
    h('label', { htmlFor: 've-kind' }, 'Type'),
    h('select', { id: 've-kind', onchange: (ev) => { e.kind = ev.target.value; renderDrawer(); } }, kinds.map((k) => h('option', { value: k, selected: k === e.kind }, KIND_LABELS[k]))),
    ...fields.filter(Boolean),
    h('div', { class: 'full row-actions' },
      h('button', { class: 'btn primary', type: 'submit' }, 'Save'),
      h('button', { class: 'btn', type: 'button', onclick: () => { state.editing = null; renderDrawer(); } }, 'Cancel')));
}

function awsPanel() {
  const a = state.ws.aws;
  const text = {
    none: 'Secrets are not loaded yet.',
    'signed-out': 'Your AWS session has expired or you are not signed in.',
    loaded: `${a.count} value${a.count === 1 ? '' : 's'} loaded at ${a.loadedAt}${a.account ? ' from account ' + a.account : ''}.`,
    error: 'Loading failed.',
  }[a.state] || '';
  return h('div', { class: 'notice' + (a.state === 'error' ? ' err' : a.state === 'signed-out' ? ' warn' : '') },
    h('strong', {}, 'AWS' + (a.profile ? ' · profile ' + a.profile : '') + (a.region ? ' · ' + a.region : '')), h('br'),
    text, a.error ? h('div', { class: 'small' }, errText(a.error)) : null,
    !a.cli ? h('div', { class: 'small' }, 'The aws CLI was not found. Install AWS CLI v2 or set its path in Settings.') : null,
    h('div', { class: 'row-actions' },
      a.state === 'signed-out' ? h('button', { class: 'btn primary', onclick: signIn }, 'Sign in with AWS SSO') : h('button', { class: 'btn', onclick: () => loadSecrets(true) }, a.state === 'loaded' ? 'Reload' : 'Load secrets'),
      a.state === 'loaded' ? h('button', { class: 'btn', onclick: forget }, 'Forget') : null));
}

async function loadSecrets(loud) {
  try {
    applyWS(await api().LoadSecrets());
    if (loud) toast('AWS values loaded');
  } catch (e) {
    applyWS(await api().Workspace());
    if (loud || !/not signed in/.test(errText(e))) toast(errText(e), true);
  }
}

async function signIn() {
  toast('Opening your browser for AWS sign-in…');
  try {
    applyWS(await api().SignIn());
    toast('Signed in. AWS values loaded.');
  } catch (e) {
    applyWS(await api().Workspace());
    toast(errText(e), true);
  }
}

async function forget() {
  applyWS(await api().ForgetSecrets());
  toast('AWS values and tokens cleared from memory');
}

// ---------- Dialogs ----------

function openDialog(build) {
  const d = $('dialog');
  d.replaceChildren(build(() => d.close()));
  if (!d.open) d.showModal();
  const first = d.querySelector('input, textarea, select, button');
  if (first) first.focus();
}

function confirmDialog(title, text, ok, danger) {
  return new Promise((resolve) => {
    const d = $('dialog');
    const done = (v) => { d.close(); resolve(v); };
    d.onclose = () => resolve(false);
    openDialog(() => h('div', { class: 'body' }, h('h2', {}, title), h('p', {}, text),
      h('div', { class: 'buttons' },
        h('button', { class: 'btn', onclick: () => done(false) }, 'Cancel'),
        h('button', { class: 'btn ' + (danger ? 'danger' : 'primary'), onclick: () => done(true) }, ok))));
  });
}

function trustDialog() {
  openDialog((close) => h('div', { class: 'body' },
    h('h2', {}, 'Trust this folder\'s .bika.json?'),
    h('p', {}, '.bika.json is shared through git. It decides which AWS secrets load and which hosts may receive them. Anyone who can push to this repo can change it.'),
    h('p', {}, 'Open ', h('span', { class: 'mono' }, basename(state.ws.root) + '/.bika.json'), ' and check the hosts, tokenUrl and AWS references before you trust it. If the file changes later, Bikasend asks again.'),
    h('div', { class: 'buttons' },
      h('button', { class: 'btn', onclick: close }, 'Not now'),
      h('button', { class: 'btn primary', onclick: async () => { close(); applyWS(await call('TrustFolder')); toast('Folder trusted'); if (needsAWS()) loadSecrets(false); } }, 'Trust'))));
}

function promptsDialog(names) {
  const list = Array.isArray(names) ? names : state.ws.prompts;
  const labels = new Map(state.ws.variables.filter((v) => v.kind === 'prompt').map((v) => [v.name, v.ref || v.name]));
  const inputs = list.map((n) => h('input', { type: 'password', autocomplete: 'off', 'aria-label': n }));
  openDialog((close) => h('form', {
    onsubmit: async (e) => {
      e.preventDefault();
      let ws;
      for (let i = 0; i < list.length; i++) if (inputs[i].value) ws = await call('SetPrompt', list[i], inputs[i].value);
      close();
      if (ws) applyWS(ws);
    },
  }, h('h2', {}, 'Values for this session'),
  h('p', { class: 'muted' }, 'Kept in memory only. They are gone when you quit Bikasend.'),
  list.map((n, i) => h('label', {}, (labels.get(n) || n) + ' ({{' + n + '}})', inputs[i])),
  h('div', { class: 'buttons' }, h('button', { class: 'btn', type: 'button', onclick: close }, 'Cancel'), h('button', { class: 'btn primary', type: 'submit' }, 'Save'))));
}

function settingsDialog() {
  const s = { ...state.ws.settings };
  openDialog((close) => h('form', {
    onsubmit: async (e) => {
      e.preventDefault();
      applyWS(await call('SaveSettings', s));
      close();
      toast('Settings saved');
    },
  }, h('h2', {}, 'Settings'),
  h('label', {}, 'Theme', h('select', { onchange: (e) => { s.theme = e.target.value; } }, ['system', 'light', 'dark'].map((t) => h('option', { value: t, selected: s.theme === t }, t[0].toUpperCase() + t.slice(1))))),
  h('label', {}, 'Font size', h('input', { type: 'number', min: '10', max: '20', value: String(s.fontSize), oninput: (e) => { s.fontSize = Number(e.target.value); } })),
  h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: s.saveHistory, onchange: (e) => { s.saveHistory = e.target.checked; } }), 'Save history to disk (secret-looking values are masked)'),
  h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: s.checkUpdates, onchange: (e) => { s.checkUpdates = e.target.checked; } }), 'Check for updates on start'),
  h('label', {}, 'Proxy (optional)', h('input', { type: 'text', value: s.proxy, placeholder: 'http://proxy.example.com:3128', oninput: (e) => { s.proxy = e.target.value; } })),
  h('label', {}, 'aws CLI path (optional)', h('input', { type: 'text', value: s.awsCli, placeholder: state.ws.aws.cli || 'not found', oninput: (e) => { s.awsCli = e.target.value; } })),
  h('div', { class: 'row-actions' },
    h('button', { class: 'btn', type: 'button', onclick: async () => { await call('ClearCookies'); toast('Cookies cleared'); } }, 'Clear cookies'),
    h('span', { class: 'muted small' }, 'Bikasend ' + state.ws.version)),
  h('div', { class: 'buttons' }, h('button', { class: 'btn', type: 'button', onclick: close }, 'Cancel'), h('button', { class: 'btn primary', type: 'submit' }, 'Save'))));
}

function importDialog() {
  const ta = h('textarea', { placeholder: "curl -X POST 'https://…' -H 'Content-Type: application/json' -d '{…}'", spellcheck: 'false', 'aria-label': 'curl command' });
  openDialog((close) => h('form', {
    onsubmit: async (e) => {
      e.preventDefault();
      const req = await call('ImportCurl', ta.value);
      close();
      newTab(req, '');
      toast('Imported. Replace hard-coded secrets with {{variables}}.');
    },
  }, h('h2', {}, 'Import a cURL command'), ta,
  h('div', { class: 'buttons' }, h('button', { class: 'btn', type: 'button', onclick: close }, 'Cancel'), h('button', { class: 'btn primary', type: 'submit' }, 'Import'))));
}

function paletteDialog() {
  const ws = state.ws || {};
  const commands = [
    { label: 'Open folder…', run: pickFolder },
    { label: 'Rescan folder', run: () => ws.root && openFolder(ws.root), hidden: !ws.root },
    { label: 'Variables', hint: MOD + 'E', run: () => openDrawer() },
    { label: 'New tab', hint: MOD + 'T', run: () => newTab() },
    { label: 'Close tab', run: () => state.active && closeTab(state.active) },
    { label: 'Import cURL…', run: importDialog },
    { label: 'Copy as cURL', run: copyCurl, hidden: !activeTab() },
    { label: 'Load AWS secrets', run: () => loadSecrets(true), hidden: !needsAWS() },
    { label: 'Forget AWS secrets and tokens', run: forget, hidden: !needsAWS() },
    { label: 'Clear history', run: async () => { await call('ClearHistory'); state.history = []; renderSide(); } },
    { label: 'Clear cookies', run: () => call('ClearCookies').then(() => toast('Cookies cleared')) },
    { label: 'Settings', run: settingsDialog },
    ...(ws.environments || []).map((e) => ({ label: 'Environment: ' + e.name, run: () => selectEnv(e.name) })),
    ...(ws.recent || []).map((p) => ({ label: 'Open recent: ' + basename(p), hint: p, run: () => openFolder(p) })),
  ].filter((c) => !c.hidden);
  for (const s of state.specs) for (const op of s.operations) commands.push({ label: op.method + ' ' + op.path, hint: op.summary || s.title, run: () => { state.specIndex = state.specs.indexOf(s); renderSide(); openOperation(op, s); } });

  const input = h('input', { type: 'text', placeholder: 'Type a command or an operation…', 'aria-label': 'Command' });
  const list = h('div', { class: 'palette-list', role: 'listbox' });
  let items = [];
  let index = 0;
  const draw = () => {
    const words = input.value.toLowerCase().split(/\s+/).filter(Boolean);
    items = commands.filter((c) => words.every((w) => (c.label + ' ' + (c.hint || '')).toLowerCase().includes(w))).slice(0, 60);
    index = Math.min(index, Math.max(items.length - 1, 0));
    list.replaceChildren(...items.map((c, i) => h('button', { class: 'palette-item' + (i === index ? ' active' : ''), onclick: () => run(i) }, c.label, c.hint ? h('span', { class: 'hint' }, c.hint) : null)));
    const active = list.querySelector('.active');
    if (active) active.scrollIntoView({ block: 'nearest' });
  };
  const run = (i) => { $('dialog').close(); if (items[i]) items[i].run(); };
  input.addEventListener('input', () => { index = 0; draw(); });
  input.addEventListener('keydown', (e) => {
    if (e.key === 'ArrowDown') { e.preventDefault(); index = Math.min(index + 1, items.length - 1); draw(); }
    if (e.key === 'ArrowUp') { e.preventDefault(); index = Math.max(index - 1, 0); draw(); }
    if (e.key === 'Enter') { e.preventDefault(); run(index); }
  });
  openDialog(() => h('div', { class: 'body palette' }, input, list));
  draw();
}

// ---------- Folder, environment, workspace ----------

function applyWS(ws) {
  if (!ws) return;
  state.ws = ws;
  renderTop();
  renderBanners();
  renderDrawer();
  refreshVarMarks();
}

async function pickFolder() {
  const dir = await call('PickFolder');
  if (dir) await openFolder(dir);
}

async function openFolder(root) {
  try {
    state.specs = await api().Open(root);
  } catch (e) {
    toast(errText(e), true);
    return;
  }
  state.ws = await api().Workspace();
  const last = store.get('spec:' + root);
  const i = state.specs.findIndex((s) => s.path === last);
  state.specIndex = i >= 0 ? i : (state.specs.length ? 0 : -1);
  applyWS(state.ws);
  renderSide();
  if (!state.tabs.length) {
    renderRequest();
    renderResponse();
  } else {
    renderTabs();
  }
  if (needsAWS() && state.ws.aws.state === 'none') loadSecrets(false);
}

async function selectEnv(name) {
  applyWS(await call('SelectEnvironment', name));
  if (needsAWS()) loadSecrets(false);
  if (activeTab() && activeTab().sub === 'auth') renderRequest();
}

// ---------- Resizing ----------

function resizer(handle, apply, key, vertical) {
  const saved = Number(store.get(key));
  if (saved) apply(saved);
  handle.addEventListener('pointerdown', (e) => {
    handle.setPointerCapture(e.pointerId);
    const move = (ev) => apply(vertical ? ev.clientY - $('main').getBoundingClientRect().top - 30 : ev.clientX);
    const up = () => {
      handle.removeEventListener('pointermove', move);
      handle.removeEventListener('pointerup', up);
      store.set(key, vertical ? parseInt(getComputedStyle($('main')).getPropertyValue('--req-h'), 10) : parseInt(getComputedStyle($('layout')).getPropertyValue('--side-w'), 10));
    };
    handle.addEventListener('pointermove', move);
    handle.addEventListener('pointerup', up);
  });
  handle.addEventListener('keydown', (e) => {
    const cur = vertical ? $('request').getBoundingClientRect().height : $('side').getBoundingClientRect().width;
    const step = { ArrowLeft: -20, ArrowUp: -20, ArrowRight: 20, ArrowDown: 20 }[e.key];
    if (step) { e.preventDefault(); apply(cur + step); }
  });
}

// ---------- Start ----------

function wire() {
  $('open').addEventListener('click', pickFolder);
  $('rescan').addEventListener('click', () => state.ws.root && openFolder(state.ws.root));
  $('recent').addEventListener('change', (e) => e.target.value && openFolder(e.target.value));
  $('env').addEventListener('change', (e) => selectEnv(e.target.value));
  $('aws').addEventListener('click', () => {
    const a = state.ws.aws;
    if (a.state === 'signed-out') signIn();
    else openDrawer();
  });
  $('varsBtn').addEventListener('click', () => ($('drawer').classList.contains('open') ? closeDrawer() : openDrawer()));
  $('drawerClose').addEventListener('click', closeDrawer);
  $('varSearch').addEventListener('input', (e) => { state.varSearch = e.target.value; renderDrawer(); });
  $('paletteBtn').addEventListener('click', paletteDialog);
  $('settingsBtn').addEventListener('click', settingsDialog);
  document.querySelectorAll('.side-tab').forEach((b) => b.addEventListener('click', async () => {
    state.sideTab = b.dataset.tab;
    if (state.sideTab === 'history') state.history = await api().History();
    renderSide();
  }));
  resizer($('sideResizer'), (w) => $('layout').style.setProperty('--side-w', Math.max(220, Math.min(w, window.innerWidth * 0.6)) + 'px'), 'sideWidth', false);
  resizer($('splitResizer'), (px) => $('main').style.setProperty('--req-h', Math.max(140, Math.min(px, window.innerHeight - 220)) + 'px'), 'reqHeight', true);

  document.addEventListener('keydown', (e) => {
    const mod = e.metaKey || e.ctrlKey;
    if (mod && e.key === 'Enter') { e.preventDefault(); send(activeTab()); }
    else if (mod && e.key.toLowerCase() === 'k') { e.preventDefault(); paletteDialog(); }
    else if (mod && e.key.toLowerCase() === 'e') { e.preventDefault(); $('drawer').classList.contains('open') ? closeDrawer() : openDrawer(); }
    else if (mod && e.key.toLowerCase() === 't') { e.preventDefault(); newTab(); }
    else if (mod && e.key.toLowerCase() === 'f' && $('findBox')) { e.preventDefault(); $('findBox').focus(); }
    else if (e.key === 'Escape' && varCard.el) hideVarCard();
    else if (e.key === 'Escape' && $('drawer').classList.contains('open') && !$('dialog').open) closeDrawer();
  });
  window.addEventListener('scroll', (e) => { if (!varCard.el || !varCard.el.contains(e.target)) hideVarCard(); }, true);
  window.addEventListener('blur', hideVarCard);
  if (window.runtime && window.runtime.EventsOn) {
    window.runtime.EventsOn('workspace:changed', () => {
      if (state.ws && state.ws.root) {
        toast('Files changed on disk. Reloaded.');
        openFolder(state.ws.root);
      }
    });
  }
}

async function start() {
  wire();
  state.ws = await api().Workspace();
  applyWS(state.ws);
  renderSide();
  renderRequest();
  if (state.ws.recent.length) await openFolder(state.ws.recent[0]);
  try {
    const up = await api().CheckUpdate();
    if (up.available) { state.update = up; renderBanners(); }
  } catch { /* the update check never blocks the app */ }
}

window.addEventListener('load', start);
