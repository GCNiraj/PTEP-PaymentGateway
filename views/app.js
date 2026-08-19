/* app.js — Core logic: tab routing, API calls, data rendering */

'use strict';

// ── CSRF helpers ─────────────────────────────────────────────
/**
 * Read the CSRF double-submit cookie set by the server on login.
 * The cookie is non-HttpOnly so JS can read it; SameSite=Strict
 * means a cross-origin attacker cannot read or predict its value.
 *
 * Some deployments use a suffixed cookie name (e.g. "csrf_token_<suffix>").
 * This helper scans all cookies and returns the first value whose name
 * starts with "csrf_token".
 */
function getCsrfToken() {
  const cookies = document.cookie ? document.cookie.split(';') : [];
  for (const raw of cookies) {
    const [name, ...rest] = raw.split('=');
    if (!name) continue;
    const trimmed = name.trim();
    if (trimmed === 'csrf_token' || trimmed.startsWith('csrf_token_')) {
      const value = rest.join('=');
      return value ? decodeURIComponent(value) : '';
    }
  }
  return '';
}

// ── Auth helpers ──────────────────────────────────────────────
/**
 * Wrapper around fetch() that:
 *  - always sends credentials (session cookie)
 *  - redirects to /login on 401
 *  - attaches the X-CSRF-Token header for state-mutating methods
 */
async function adminFetch(url, options = {}) {
  const method = (options.method || 'GET').toUpperCase();
  const safeMethods = new Set(['GET', 'HEAD', 'OPTIONS']);
  const headers = { ...(options.headers || {}) };
  if (!safeMethods.has(method)) {
    const token = getCsrfToken();
    if (token) headers['X-CSRF-Token'] = token;
  }
  const res = await fetch(url, { ...options, headers, credentials: 'same-origin' });
  if (res.status === 401) { window.location.href = '/login'; throw new Error('unauthorized'); }
  return res;
}

async function logout() {
  try {
    await fetch('/api/admin/logout', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'X-CSRF-Token': getCsrfToken() },
    });
  } catch (err) {
    console.error('Logout request failed', err);
  }
  // Always redirect to login page, regardless of API response
  window.location.href = '/login';
}

// ── Toast ─────────────────────────────────────────────────────
function showToast(message, type = 'success') {
  const toast = document.getElementById('app-toast');
  const msgEl = document.getElementById('toast-message');
  if (!toast || !msgEl) return;
  msgEl.textContent = message;
  toast.className = `toast align-items-center text-white border-0 bg-${type === 'success' ? 'success' : type === 'danger' ? 'danger' : 'primary'}`;
  bootstrap.Toast.getOrCreateInstance(toast, { delay: 3000 }).show();
}

// ── Escape HTML ───────────────────────────────────────────────
function esc(value) {
  return String(value ?? '').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;').replace(/'/g,'&#39;');
}

// ── Badges ────────────────────────────────────────────────────
function statusBadge(status) {
  const s = String(status).toLowerCase();
  const map = { success:'success', successful:'success', completed:'success', failed:'danger', pending:'warning text-dark' };
  const cls = map[s] || 'secondary';
  return `<span class="badge bg-${cls} text-capitalize">${esc(status)}</span>`;
}
function httpBadge(code) {
  const n = parseInt(code, 10);
  if (n >= 500) return `<span class="badge bg-danger">${code}</span>`;
  if (n >= 400) return `<span class="badge bg-warning text-dark">${code}</span>`;
  if (n >= 200) return `<span class="badge bg-success">${code}</span>`;
  return `<span class="badge bg-secondary">${code}</span>`;
}
function methodBadge(method) {
  const map = { GET:'primary', POST:'success', PUT:'warning text-dark', DELETE:'danger', PATCH:'info' };
  const cls = map[String(method).toUpperCase()] || 'secondary';
  return `<span class="badge bg-${cls}">${esc(method)}</span>`;
}
function emptyRow(cols, message = 'No data found.') {
  return `<tr><td colspan="${cols}" class="empty-state"><i class="bi bi-inbox"></i>${esc(message)}</td></tr>`;
}

// ── Tab switching ─────────────────────────────────────────────
function initTabSwitching() {
  document.querySelectorAll('.sidebar-link[data-tab]').forEach(link => {
    link.addEventListener('click', e => { e.preventDefault(); switchTab(link.dataset.tab); });
  });
}

let _loadedTabs = new Set();

function switchTab(tab) {
  document.querySelectorAll('.tab-pane').forEach(p => p.classList.add('d-none'));
  document.querySelectorAll('.sidebar-link').forEach(l => l.classList.remove('active'));
  const pane = document.getElementById('tab-' + tab);
  const link = document.querySelector(`.sidebar-link[data-tab="${tab}"]`);
  if (pane) pane.classList.remove('d-none');
  if (link) link.classList.add('active');

  if (tab === 'dashboard')     { loadStats(); loadDashboardTransactions(); }
  if (tab === 'transactions')  { if (!_loadedTabs.has('transactions')) { loadApps(); _loadedTabs.add('transactions'); } loadTransactions(); }
  if (tab === 'apps')          { loadApps(); }
  if (tab === 'international') { loadInternationalTransactions(); }
  if (tab === 'logs')          { loadLogs(); }
}

// ── Date defaults ─────────────────────────────────────────────
function initDateDefaults() {
  const to = new Date(), from = new Date();
  from.setDate(from.getDate() - 30);
  
  // Format as YYYY-MM-DD using local timezone, avoiding UTC shift of toISOString()
  const fmt = d => {
    const year = d.getFullYear();
    const month = String(d.getMonth() + 1).padStart(2, '0');
    const day = String(d.getDate()).padStart(2, '0');
    return `${year}-${month}-${day}`;
  };

  ['filter-from','filter-to','txn-filter-from','txn-filter-to','intl-filter-from','intl-filter-to'].forEach((id, i) => {
    const el = document.getElementById(id);
    if (el) el.value = fmt(i % 2 === 0 ? from : to);
  });
}

// ── Dashboard Overview ────────────────────────────────────────
async function loadStats() {
  try {
    const from = document.getElementById('filter-from')?.value || '';
    const to   = document.getElementById('filter-to')?.value   || '';
    
    const params = new URLSearchParams();
    if (from) params.set('from', from);
    if (to)   params.set('to', to);

    const res  = await adminFetch('/api/admin/stats?' + params.toString());
    if (!res.ok) return;
    const stats = await res.json();
    setMetric('metric-total',   stats.total_count);
    setMetric('metric-success', stats.success_count);
    setMetric('metric-failed',  stats.failed_count);
    setMetric('metric-pending', stats.pending_count);
    setMetric('metric-amount',  parseFloat(stats.total_amount || 0).toFixed(2));
    renderStatusChart(stats.success_count, stats.failed_count, stats.pending_count);

    // Load volume trend with date filters
    const volRes = await adminFetch('/api/admin/stats/daily?' + params.toString());
    if (volRes.ok) {
      const volData = await volRes.json();
      const volumes = volData.data || [];
      renderVolumeChart(
        volumes.map(v => v.day),
        volumes.map(v => v.count)
      );
    }
  } catch (e) { console.error('loadStats:', e); }
}

function setMetric(id, val) { const el = document.getElementById(id); if (el) el.textContent = val ?? '—'; }

// ── Dashboard live transactions ───────────────────────────────
async function loadDashboardTransactions() {
  const tbody = document.getElementById('live-transactions-body');
  if (!tbody) return;
  tbody.innerHTML = '<tr><td colspan="7" class="text-center text-muted py-3"><span class="spinner-border spinner-border-sm"></span></td></tr>';
  try {
    const from = document.getElementById('filter-from')?.value || '';
    const to   = document.getElementById('filter-to')?.value   || '';
    const params = new URLSearchParams({ limit: 20 });
    if (from) params.set('from', from);
    if (to)   params.set('to', to);
    const res  = await adminFetch('/api/admin/transactions?' + params.toString());
    const data = await res.json();
    renderTransactionRows(tbody, data.data || [], 7);
  } catch (e) { tbody.innerHTML = emptyRow(7, 'Failed to load transactions.'); }
}

// ── Past Transactions tab ─────────────────────────────────────
async function loadTransactions() {
  const tbody  = document.getElementById('past-transactions-body');
  if (!tbody) return;
  const appId  = document.getElementById('filter-app')?.value    || '';
  const status = document.getElementById('filter-status')?.value || '';
  const from   = document.getElementById('txn-filter-from')?.value || '';
  const to     = document.getElementById('txn-filter-to')?.value   || '';
  const search = document.getElementById('txn-search')?.value      || '';
  const payload = { limit: 200 };
  if (appId)  payload.app_id = appId;
  if (status) payload.status = status;
  if (from)   payload.from = from;
  if (to)     payload.to = to;
  if (search) payload.search = search;

  tbody.innerHTML = '<tr><td colspan="7" class="text-center text-muted py-3"><span class="spinner-border spinner-border-sm"></span></td></tr>';
  try {
    const res  = await adminFetch('/api/admin/transactions/search', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
    const data = await res.json();
    renderTransactionRows(tbody, data.data || [], 7);
  } catch (e) { tbody.innerHTML = emptyRow(7, 'Failed to load transactions.'); }
}

function renderTransactionRows(tbody, rows, cols) {
  if (!rows.length) { tbody.innerHTML = emptyRow(cols, 'No transactions found.'); return; }
  tbody.innerHTML = '';
  for (const row of rows) {
    const tr = document.createElement('tr');
    tr.innerHTML = `
      <td class="font-monospace">${esc(row.order_id || '')}</td>
      <td class="font-monospace">${esc(row.stan_number || row.id || '')}</td>
      <td class="font-monospace">${esc(row.bfs_txn_id || '')}</td>
      <td class="text-end">${esc(row.amount || '')} <span class="text-muted">${esc(row.currency || '')}</span></td>
      <td>${statusBadge(row.status || '')}</td>
      <td class="small text-muted">${esc(row.remitter_account || '')} → ${esc(row.beneficiary_account || '')}</td>
      <td class="small text-muted">${esc(row.created_at || '')}</td>
    `;
    tr.addEventListener('click', () => openTransactionModal(row));
    tbody.appendChild(tr);
  }
}

// ── Transaction Detail Modal ──────────────────────────────────
async function openTransactionModal(row) {
  // Show modal immediately with basic data
  const setEl = (id, val) => { const el = document.getElementById(id); if (el) el.textContent = val || '—'; };
  setEl('detail-txn-id',          row.id);
  setEl('detail-external-app-id', row.external_app_id);
  setEl('detail-order-id',        row.order_id);
  setEl('detail-inquiry-id',      row.inquiry_id);
  setEl('detail-stan',            row.stan_number);
  setEl('detail-bfs-txn-id',      row.bfs_txn_id);
  setEl('detail-amount',          row.amount ? `${row.amount} ${row.currency || ''}` : '—');
  setEl('detail-status',          row.status);
  setEl('detail-remitter',        row.remitter_account);
  setEl('detail-beneficiary',     row.beneficiary_account);
  setEl('detail-txn-datetime',    row.transaction_datetime);
  setEl('detail-created-at',      row.created_at);

  // Clear extended fields while loading
  ['detail-bfs-request-id','detail-bfs-order-no','detail-txn-fee','detail-remitter-name',
   'detail-remitter-phone','detail-remitter-bank','detail-email','detail-payment-desc',
   'detail-error-code','detail-error-message','detail-updated-at','detail-completed-at'
  ].forEach(id => setEl(id, '…'));

  showModal('modal-txn-detail');

  // Fetch full detail via POST so transaction id is not sent in URL (CWE-598)
  if (row.id) {
    try {
      const res = await adminFetch('/api/admin/transactions/detail', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id: row.id }),
      });
      if (res.ok) {
        const full = await res.json();
        setEl('detail-bfs-request-id', full.bfs_request_id);
        setEl('detail-bfs-order-no',   full.bfs_order_no);
        setEl('detail-txn-fee',        full.transaction_fee);
        setEl('detail-remitter-name',  full.remitter_name);
        setEl('detail-remitter-phone', full.remitter_phone);
        setEl('detail-remitter-bank',  full.remitter_bank);
        setEl('detail-email',          full.email_id);
        setEl('detail-payment-desc',   full.payment_desc);
        setEl('detail-error-code',     full.error_code);
        setEl('detail-error-message',  full.error_message);
        setEl('detail-updated-at',     full.updated_at);
        setEl('detail-completed-at',   full.completed_at);
      }
    } catch (e) { console.warn('Could not fetch full transaction:', e); }
  }

  // Load related logs
  loadRelatedLogs(row);
}

async function loadRelatedLogs(row) {
  const container = document.getElementById('related-logs-body');
  if (!container) return;
  container.innerHTML = '<tr><td colspan="5" class="text-center text-muted py-2"><span class="spinner-border spinner-border-sm me-1"></span>Searching logs…</td></tr>';

  // Search by STAN or order_id
  const searchTerm = row.stan_number || row.order_id || '';
  if (!searchTerm) { container.innerHTML = emptyRow(5, 'No search term available.'); return; }

  try {
    const res  = await adminFetch('/api/admin/logs/search', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ search: searchTerm, limit: 20 }),
    });
    const data = await res.json();
    const rows = data.data || [];
    if (!rows.length) { container.innerHTML = emptyRow(5, 'No related logs found.'); return; }
    container.innerHTML = '';
    for (const log of rows) {
      const tr = document.createElement('tr');
      tr.innerHTML = `
        <td>${methodBadge(log.method)}</td>
        <td class="small font-monospace">${esc(log.path)}</td>
        <td>${httpBadge(log.status)}</td>
        <td class="small text-muted">${esc(log.duration_ms)} ms</td>
        <td class="small text-muted">${esc(log.created_at)}</td>
      `;
      container.appendChild(tr);
    }
  } catch (e) { container.innerHTML = emptyRow(5, 'Failed to load related logs.'); }
}

// ── Connected Apps tab ────────────────────────────────────────
async function loadApps() {
  const tbody  = document.getElementById('apps-table-body');
  const select = document.getElementById('filter-app');
  if (!tbody) return;
  tbody.innerHTML = '<tr><td colspan="7" class="text-center text-muted py-3"><span class="spinner-border spinner-border-sm"></span></td></tr>';
  try {
    const res  = await adminFetch('/api/admin/apps');
    const data = await res.json();
    const rows = data.data || [];
    if (select) {
      select.innerHTML = '<option value="">All Apps</option>';
      rows.forEach(r => {
        const opt = document.createElement('option');
        opt.value = r.id; opt.textContent = r.name;
        select.appendChild(opt);
      });
    }
    if (!rows.length) { tbody.innerHTML = emptyRow(7, 'No apps yet. Create one to get started.'); return; }
    tbody.innerHTML = '';
    for (const row of rows) {
      const active = row.is_active ? '<span class="badge bg-success">Active</span>' : '<span class="badge bg-secondary">Inactive</span>';
      const contact = [row.contact_name, row.contact_email].filter(Boolean).join(', ') || '—';
      const tr = document.createElement('tr');
      tr.innerHTML = `
        <td class="font-monospace small">${esc(row.id)}</td>
        <td class="fw-semibold">${esc(row.name)}</td>
        <td><code class="small">${esc((row.api_key || '').slice(0, 22))}…</code></td>
        <td class="small">${esc(contact)}</td>
        <td>${active}</td>
        <td class="small text-muted">${row.created_at ? new Date(row.created_at).toLocaleDateString() : '—'}</td>
        <td>
          <div class="d-flex gap-1">
            <button class="btn btn-sm btn-outline-primary"   data-action="edit"  data-id="${esc(row.id)}">Edit</button>
            <button class="btn btn-sm btn-outline-danger"    data-action="regen" data-id="${esc(row.id)}">Regen Key</button>
            <button class="btn btn-sm btn-outline-secondary" data-action="delete" data-id="${esc(row.id)}" data-name="${esc(row.name)}"><i class="bi bi-trash"></i></button>
          </div>
        </td>
      `;
      tbody.appendChild(tr);
    }
    tbody.querySelectorAll('[data-action]').forEach(btn => {
      btn.addEventListener('click', e => {
        e.stopPropagation();
        const id = btn.dataset.id, action = btn.dataset.action, name = btn.dataset.name;
        if (action === 'edit') editApp(id);
        if (action === 'regen') regenKey(id);
        if (action === 'delete') deleteApp(id, name);
      });
    });
  } catch (e) { tbody.innerHTML = emptyRow(7, 'Failed to load apps.'); }
}

async function editApp(id) {
  try {
    const res = await adminFetch(`/api/admin/apps/${id}`);
    const app = await res.json();
    openEditAppModal(app);
  } catch (e) { showToast('Failed to load app details.', 'danger'); }
}

async function regenKey(id) {
  if (!confirm('Regenerating the API key will invalidate the existing key immediately. Continue?')) return;
  try {
    const res  = await adminFetch(`/api/admin/apps/${id}/regenerate-key`, { method: 'POST' });
    const data = await res.json();
    if (res.ok) { 
      let decodedKey = data.api_key || '';
      let decodedSecret = data.api_secret || '';
      try { decodedKey = atob(decodedKey.replace(/\*/g, '')); } catch (e) { /* fallback to raw */ }
      try { decodedSecret = atob(decodedSecret.replace(/\*/g, '')); } catch (e) { /* fallback to raw */ }
      openCredentialsModal(data.app_id || id, decodedKey, decodedSecret); 
      showToast('API key regenerated.', 'success'); 
    }
    else showToast(data.error || 'Failed to regenerate key.', 'danger');
  } catch (e) { showToast('Network error.', 'danger'); }
}

async function deleteApp(id, name) {
  if (!confirm(`Are you sure you want to delete the app "${name}"?\nThis will permanently revoke its API access.`)) return;
  try {
    const res = await adminFetch(`/api/admin/apps/${id}`, { method: 'DELETE' });
    if (res.ok) { 
      showToast('App deleted successfully.', 'success'); 
      loadApps(); 
    } else {
      const data = await res.json();
      showToast(data.error || 'Failed to delete app.', 'danger');
    }
  } catch (e) { 
    showToast('Network error while deleting app.', 'danger'); 
  }
}

async function createApp(event) {
  event.preventDefault();
  const submitBtn = event.target.querySelector('[type="submit"]');
  const spinner   = submitBtn.querySelector('.spinner-border');
  submitBtn.disabled = true;
  if (spinner) spinner.classList.remove('d-none');
  const payload = {
    name:          document.getElementById('new-app-name').value,
    contact_name:  document.getElementById('new-contact-name').value,
    contact_email: document.getElementById('new-contact-email').value,
    contact_phone: document.getElementById('new-contact-phone').value,
  };
  try {
    const res  = await adminFetch('/api/admin/apps/create', { method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify(payload) });
    const data = await res.json();
    if (res.ok) { 
      hideModal('modal-create-app'); 
      document.getElementById('create-app-form').reset(); 
      let decodedKey = data.api_key || '';
      let decodedSecret = data.api_secret || '';
      try { decodedKey = atob(decodedKey.replace(/\*/g, '')); } catch (e) { /* fallback to raw */ }
      try { decodedSecret = atob(decodedSecret.replace(/\*/g, '')); } catch (e) { /* fallback to raw */ }
      openCredentialsModal(data.app_id, decodedKey, decodedSecret); 
      loadApps(); 
    }
    else showToast(data.error || 'Failed to create app.', 'danger');
  } catch (e) { showToast('Network error.', 'danger'); }
  finally { submitBtn.disabled = false; if (spinner) spinner.classList.add('d-none'); }
}

async function updateApp(event) {
  event.preventDefault();
  const id = document.getElementById('edit-app-id').value;
  const payload = {
    name:          document.getElementById('edit-app-name').value,
    contact_name:  document.getElementById('edit-contact-name').value,
    contact_email: document.getElementById('edit-contact-email').value,
    contact_phone: document.getElementById('edit-contact-phone').value,
    is_active:     document.getElementById('edit-app-active').checked,
  };
  try {
    const res = await adminFetch(`/api/admin/apps/${id}`, { method:'PUT', headers:{'Content-Type':'application/json'}, body:JSON.stringify(payload) });
    if (res.ok) { hideModal('modal-edit-app'); showToast('App updated.', 'success'); loadApps(); }
    else { const data = await res.json(); showToast(data.error || 'Failed to update app.', 'danger'); }
  } catch (e) { showToast('Network error.', 'danger'); }
}

// ── International tab ─────────────────────────────────────────
async function loadInternationalTransactions() {
  const tbody = document.getElementById('intl-table-body');
  if (!tbody) return;

  const status = document.getElementById('intl-filter-status')?.value || '';
  const from   = document.getElementById('intl-filter-from')?.value || '';
  const to     = document.getElementById('intl-filter-to')?.value   || '';
  const search = document.getElementById('intl-search')?.value      || '';
  const payload = { limit: 200 };
  if (status) payload.status = status;
  if (from) payload.from = from;
  if (to) payload.to = to;
  if (search) payload.search = search;

  tbody.innerHTML = '<tr><td colspan="7" class="text-center text-muted py-3"><span class="spinner-border spinner-border-sm"></span></td></tr>';
  try {
    const res  = await adminFetch('/api/admin/international/transactions/search', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
    const data = await res.json();
    const rows = data.data || [];
    if (!rows.length) { tbody.innerHTML = emptyRow(7, 'No international transactions yet.'); return; }
    tbody.innerHTML = '';
    for (const row of rows) {
      const referenceID = row.reference_id || row.ReferenceID || '';
      const paymentID = row.payment_id || row.PaymentID || '';
      const merchant = row.merchant_id || row.MerchantID || '—';
      const amount = row.amount ?? row.Amount ?? '';
      const currency = row.currency || row.Currency || '';
      const statusValue = row.status || row.Status || '';
      const createdAt = row.created_at || row.CreatedAt || '';
      const completedAt = row.completed_at || row.CompletedAt || '—';
      const tr = document.createElement('tr');
      tr.innerHTML = `
        <td class="font-monospace small">${esc(referenceID)}</td>
        <td class="font-monospace small">${esc(paymentID)}</td>
        <td>${esc(merchant)}</td>
        <td class="text-end">${esc(amount)} <span class="text-muted">${esc(currency)}</span></td>
        <td>${statusBadge(statusValue)}</td>
        <td class="small text-muted">${esc(createdAt)}</td>
        <td class="small text-muted">${esc(completedAt)}</td>
      `;
      tbody.appendChild(tr);
    }
  } catch (e) { tbody.innerHTML = emptyRow(7, 'Failed to load international transactions.'); }
}

// ── System Logs tab ───────────────────────────────────────────
let _logsPage = 0;          // current offset (multiples of LOG_PAGE_SIZE)
let _logsAllLoaded = false; // true once server returns fewer rows than page
let _logsLoading = false;   // prevents double-fetches on rapid clicks
const LOG_PAGE_SIZE = 50;

async function loadLogs(reset = true) {
  const tbody = document.getElementById('logs-table-body');
  if (!tbody) return;
  if (_logsLoading) return;

  // Reset pagination state when filters change
  if (reset) {
    _logsPage = 0;
    _logsAllLoaded = false;
    tbody.innerHTML = '<tr><td colspan="7" class="text-center text-muted py-3"><span class="spinner-border spinner-border-sm"></span></td></tr>';
  }
  if (!reset && _logsAllLoaded) return;

  const status = document.getElementById('filter-status-code')?.value || '0';
  const method = document.getElementById('filter-log-method')?.value  || '';
  const path   = document.getElementById('filter-log-path')?.value    || '';
  const search = document.getElementById('log-search')?.value         || '';
  const payload = { limit: LOG_PAGE_SIZE, offset: _logsPage * LOG_PAGE_SIZE };
  if (parseInt(status, 10) > 0) payload.status = parseInt(status, 10);
  if (method) payload.method = method;
  if (path)   payload.path = path;
  if (search) payload.search = search;

  // Remove existing Load More button while fetching
  document.getElementById('logs-load-more-row')?.remove();
  _logsLoading = true;

  try {
    const res  = await adminFetch('/api/admin/logs/search', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
    const data = await res.json();
    const rows = data.data || [];

    if (reset) tbody.innerHTML = '';
    if (reset && !rows.length) { tbody.innerHTML = emptyRow(7, 'No logs found.'); return; }

    for (const row of rows) {
      const tr = document.createElement('tr');
      tr.innerHTML = `
        <td class="font-monospace small">${esc(row.id)}</td>
        <td>${methodBadge(row.method)}</td>
        <td class="small text-muted font-monospace">${esc(row.path)}</td>
        <td>${httpBadge(row.status)}</td>
        <td class="small text-muted">${esc(row.duration_ms)} ms</td>
        <td class="small text-muted">${esc(row.ip)}</td>
        <td class="small text-muted">${esc(row.created_at)}</td>
      `;
      tr.addEventListener('click', () => openLogModal(row));
      tbody.appendChild(tr);
    }

    if (rows.length > 0) _logsPage += 1;
    _logsAllLoaded = rows.length < LOG_PAGE_SIZE;

    // Show Load More if a full page was returned
    if (!_logsAllLoaded) {
      const loadMoreRow = document.createElement('tr');
      loadMoreRow.id = 'logs-load-more-row';
      const cell = document.createElement('td');
      cell.colSpan = 7;
      cell.className = 'text-center py-2';
      const btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'btn btn-sm btn-outline-secondary';
      btn.innerHTML = '<i class="bi bi-chevron-down me-1"></i>Load More';
      btn.addEventListener('click', () => loadLogs(false));
      cell.appendChild(btn);
      loadMoreRow.appendChild(cell);
      tbody.appendChild(loadMoreRow);
    }
  } catch (e) {
    if (reset) tbody.innerHTML = emptyRow(7, 'Failed to load logs.');
    else console.error('loadLogs more:', e);
  } finally {
    _logsLoading = false;
  }
}

// ── Initialisation ────────────────────────────────────────────
document.addEventListener('DOMContentLoaded', () => {
  initTabSwitching();
  initDateDefaults();

  document.getElementById('apply-filters')?.addEventListener('click', () => { loadStats(); loadDashboardTransactions(); });
  document.getElementById('filter-txns-btn')?.addEventListener('click', loadTransactions);
  document.getElementById('filter-logs-btn')?.addEventListener('click', loadLogs);
  document.getElementById('refresh-logs-btn')?.addEventListener('click', loadLogs);
  document.getElementById('refresh-intl-btn')?.addEventListener('click', loadInternationalTransactions);
  document.getElementById('refresh-apps-btn')?.addEventListener('click', loadApps);

  // Allow pressing Enter in search boxes
  document.getElementById('txn-search')?.addEventListener('keydown', e => { if (e.key === 'Enter') loadTransactions(); });
  document.getElementById('log-search')?.addEventListener('keydown', e => { if (e.key === 'Enter') loadLogs(); });
  document.getElementById('intl-search')?.addEventListener('keydown', e => { if (e.key === 'Enter') loadInternationalTransactions(); });

  document.getElementById('filter-intl-btn')?.addEventListener('click', loadInternationalTransactions);

  document.getElementById('create-app-form')?.addEventListener('submit', createApp);
  document.getElementById('edit-app-form')?.addEventListener('submit', updateApp);
  document.getElementById('logout-btn')?.addEventListener('click', e => { e.preventDefault(); logout(); });
  document.getElementById('copy-all-creds')?.addEventListener('click', copyAllCredentials);
  document.getElementById('create-app-btn')?.addEventListener('click', openCreateAppModal);

  switchTab('dashboard');
});
