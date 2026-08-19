/* modals.js — Modal open / close / populate logic */

// ── Bootstrap modal references (lazy-initialised) ────────────
const _bsModal = {};
const _rawCredentials = {
  appId: '',
  apiKey: '',
  apiSecret: '',
};
function getBsModal(id) {
  if (!_bsModal[id]) {
    _bsModal[id] = new bootstrap.Modal(document.getElementById(id));
  }
  return _bsModal[id];
}

// ── Generic helpers ──────────────────────────────────────────
function showModal(id) { getBsModal(id).show(); }
function hideModal(id) { getBsModal(id).hide(); }

function maskCredentialForDisplay(value) {
  const cred = String(value || '').trim();
  if (!cred) return '';
  if (cred.length <= 8) return '*'.repeat(cred.length);
  return `${cred.slice(0, 4)}${'*'.repeat(cred.length - 8)}${cred.slice(-4)}`;
}

// ── Create App Modal ─────────────────────────────────────────
function openCreateAppModal() {
  document.getElementById('create-app-form').reset();
  showModal('modal-create-app');
}

// ── Credentials Modal (one-time display after app creation) ──
function openCredentialsModal(appId, apiKey, apiSecret) {
  _rawCredentials.appId = appId || '';
  _rawCredentials.apiKey = apiKey || '';
  _rawCredentials.apiSecret = apiSecret || '';

  document.getElementById('cred-app-id').value    = _rawCredentials.appId;
  document.getElementById('cred-api-key').value   = maskCredentialForDisplay(_rawCredentials.apiKey);
  document.getElementById('cred-api-secret').value = maskCredentialForDisplay(_rawCredentials.apiSecret);
  showModal('modal-credentials');
}

// Clear secrets when the credentials modal is dismissed
document.addEventListener('DOMContentLoaded', () => {
  const credModal = document.getElementById('modal-credentials');
  if (credModal) {
    credModal.addEventListener('hidden.bs.modal', () => {
      _rawCredentials.appId = '';
      _rawCredentials.apiKey = '';
      _rawCredentials.apiSecret = '';
      document.getElementById('cred-api-secret').value = '';
      document.getElementById('cred-api-key').value    = '';
      document.getElementById('cred-app-id').value     = '';
    });
  }
});

// Copy All button in credentials modal
function copyAllCredentials() {
  const appId = _rawCredentials.appId || document.getElementById('cred-app-id').value;
  const apiKey = _rawCredentials.apiKey || document.getElementById('cred-api-key').value;
  const apiSecret = _rawCredentials.apiSecret || document.getElementById('cred-api-secret').value;
  const text =
    `App ID: ${appId}\n` +
    `API Key: ${apiKey}\n` +
    `API Secret: ${apiSecret}`;
  navigator.clipboard.writeText(text).then(() => showToast('Credentials copied to clipboard!', 'success'));
}

// ── Edit App Modal ────────────────────────────────────────────
function openEditAppModal(app) {
  document.getElementById('edit-app-id').value          = app.id;
  document.getElementById('edit-app-name').value        = app.name;
  document.getElementById('edit-contact-name').value    = app.contact_name  || '';
  document.getElementById('edit-contact-email').value   = app.contact_email || '';
  document.getElementById('edit-contact-phone').value   = app.contact_phone || '';
  document.getElementById('edit-app-active').checked    = !!app.is_active;
  showModal('modal-edit-app');
}

// ── Transaction Details Modal is implemented in app.js (includes async detail fetch) ─────────────────────────────────



// ── Log Details Modal ─────────────────────────────────────────
function openLogModal(row) {
  const set = (id, val) => {
    const el = document.getElementById(id);
    if (el) el.textContent = val || '—';
  };
  set('detail-log-id',         row.id);
  set('detail-log-method',     row.method);
  set('detail-log-path',       row.path);
  set('detail-log-status',     row.status);
  set('detail-log-duration',   row.duration_ms != null ? `${row.duration_ms} ms` : '—');
  set('detail-log-ip',         row.ip);
  set('detail-log-request-id', row.request_id);
  set('detail-log-created-at', row.created_at);

  const fmt = (v) => {
    if (!v) return '';
    if (typeof v === 'object') return JSON.stringify(v, null, 2);
    try { return JSON.stringify(JSON.parse(v), null, 2); } catch { return String(v); }
  };
  document.getElementById('log-request-body').textContent  = fmt(row.request_body);
  document.getElementById('log-response-body').textContent = fmt(row.response_body);
  showModal('modal-log-detail');
}
