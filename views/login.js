'use strict';

const loginState = {
  captchaEnabled: false,
  captchaSiteKey: '',
  captchaAfterFailures: 0,
  failedAttempts: 0,
  captchaToken: '',
  captchaWidgetID: null,
  captchaLoading: false
};

const alertEl = document.getElementById('login-alert');
const captchaWrap = document.getElementById('captcha-wrap');
const captchaWidget = document.getElementById('captcha-widget');
const captchaStatus = document.getElementById('captcha-status');

function showAlert(kind, text) {
  alertEl.className = `alert alert-${kind}`;
  alertEl.textContent = text;
}

function setCaptchaStatus(kind, text) {
  if (!captchaStatus) return;
  captchaStatus.className = 'small mt-2';
  if (kind === 'error') {
    captchaStatus.classList.add('text-danger');
  } else {
    captchaStatus.classList.add('text-muted');
  }
  captchaStatus.textContent = text || '';
}

function usesRecaptchaKeyFormat() {
  return loginState.captchaSiteKey.startsWith('6L');
}

function shouldRequireCaptcha() {
  if (!loginState.captchaEnabled) return false;
  return loginState.failedAttempts >= loginState.captchaAfterFailures;
}

function resetCaptchaIfRendered() {
  if (window.turnstile && loginState.captchaWidgetID !== null) {
    window.turnstile.reset(loginState.captchaWidgetID);
  }
  loginState.captchaToken = '';
}

function renderCaptchaWidget() {
  if (!loginState.captchaEnabled || !window.turnstile || loginState.captchaWidgetID !== null) return;
  try {
    loginState.captchaWidgetID = window.turnstile.render(captchaWidget, {
      sitekey: loginState.captchaSiteKey,
      callback: function (token) {
        loginState.captchaToken = token || '';
      },
      'expired-callback': function () {
        loginState.captchaToken = '';
        setCaptchaStatus('error', 'Security check expired. Please verify again.');
      },
      'error-callback': function () {
        loginState.captchaToken = '';
        setCaptchaStatus('error', 'Security check failed to load. Disable blockers or verify Turnstile keys.');
      }
    });
    setCaptchaStatus('info', '');
  } catch (_) {
    loginState.captchaWidgetID = null;
    setCaptchaStatus('error', 'Security check failed to render. This app requires Cloudflare Turnstile keys.');
  }
}

async function loadTurnstile() {
  if (!loginState.captchaEnabled || window.turnstile || loginState.captchaLoading) return;
  loginState.captchaLoading = true;
  setCaptchaStatus('info', 'Loading security check...');
  await new Promise((resolve, reject) => {
    const script = document.createElement('script');
    script.src = 'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit';
    script.async = true;
    script.defer = true;
    script.onload = resolve;
    script.onerror = function () {
      reject(new Error('failed to load turnstile'));
    };
    document.head.appendChild(script);
  }).finally(() => {
    loginState.captchaLoading = false;
  });
}

async function ensureCaptchaReady() {
  if (!loginState.captchaEnabled) {
    setCaptchaStatus('error',
      'No security check is configured on this gateway. ' +
      'LOGIN_CAPTCHA_SITE_KEY and LOGIN_CAPTCHA_SECRET must BOTH be set — ' +
      'one alone turns it off silently.');
    return false;
  }
  if (usesRecaptchaKeyFormat()) {
    setCaptchaStatus('error', 'Detected reCAPTCHA key (starts with 6L). Use Cloudflare Turnstile site key.');
    return false;
  }
  if (!window.turnstile) {
    try {
      await loadTurnstile();
    } catch (_) {
      setCaptchaStatus('error',
        'Could not load challenges.cloudflare.com. An ad blocker, an offline ' +
        'machine or a Content-Security-Policy without challenges.cloudflare.com ' +
        'in script-src will each do this.');
      return false;
    }
  }
  renderCaptchaWidget();
  if (loginState.captchaWidgetID === null) {
    setCaptchaStatus('error',
      'Turnstile loaded but refused to render. The most common cause is this ' +
      'hostname not being listed against the site key in the Cloudflare ' +
      'dashboard. Site key in use: ' + loginState.captchaSiteKey);
    return false;
  }
  return Boolean(window.turnstile && loginState.captchaWidgetID !== null);
}

// A challenge that fails to appear must say so.
//
// Every way this can go wrong used to look identical from the page: no widget,
// no message, a login form that works. Missing keys, an unreachable Cloudflare,
// a blocker, a stale cached script — all silent, and all indistinguishable from
// "this deployment has no captcha". On an administrative console for a service
// that moves money, an absent guard that looks exactly like an intentional one
// is the worst of the options.
//
// So when a challenge is configured, the block is shown and it reports where it
// got to. Only a gateway with no captcha configured at all stays quiet.
function refreshCaptchaVisibility() {
  if (!loginState.captchaEnabled) {
    captchaWrap.classList.add('d-none');
    setCaptchaStatus('info', '');
    return;
  }
  if (!shouldRequireCaptcha()) {
    // Configured, but not demanded yet — only possible above a zero threshold.
    captchaWrap.classList.add('d-none');
    setCaptchaStatus('info', '');
    return;
  }
  captchaWrap.classList.remove('d-none');
  void ensureCaptchaReady();
}

async function loadLoginOptions() {
  try {
    const res = await fetch('/api/admin/login/options', { credentials: 'same-origin' });
    if (!res.ok) return;
    const data = await res.json().catch(() => ({}));
    loginState.captchaEnabled = Boolean(data.captcha_enabled && data.captcha_site_key);
    loginState.captchaSiteKey = String(data.captcha_site_key || '');
    const threshold = Number(data.captcha_after_failures);
    loginState.captchaAfterFailures = Number.isFinite(threshold) ? Math.max(0, Math.floor(threshold)) : 0;
  } catch (_) {}
}

(async function checkSession() {
  try {
    const res = await fetch('/api/admin/session', { credentials: 'same-origin' });
    if (res.ok) window.location.href = '/dashboard';
  } catch (_) {}
})();

(async function bootstrapLogin() {
  await loadLoginOptions();
  if (loginState.captchaEnabled) {
    void ensureCaptchaReady();
  }
  refreshCaptchaVisibility();
})();

document.getElementById('login-form').addEventListener('submit', async function (e) {
  e.preventDefault();
  const btn = document.getElementById('login-btn');
  const spinner = document.getElementById('login-spinner');

  btn.disabled = true;
  spinner.classList.remove('d-none');
  alertEl.className = 'alert d-none';

  // Refuse to submit credentials over plain HTTP (outside of localhost).
  // The server also enforces this, but checking here avoids sending any data at all.
  const isLocalhost = ['localhost', '127.0.0.1', '::1'].includes(window.location.hostname);
  if (window.location.protocol !== 'https:' && !isLocalhost) {
    showAlert('danger', 'Connection error. Please contact your system administrator.');
    btn.disabled = false;
    spinner.classList.add('d-none');
    return;
  }

  const rawUsername = document.getElementById('username').value;
  const rawPassword = document.getElementById('password').value;
  
  // Base64 encode credentials to hide plain text from casual network observation
  const username = btoa(unescape(encodeURIComponent(rawUsername)));
  const password = btoa(unescape(encodeURIComponent(rawPassword)));
  
  const requestBody = { username, password };
  if (shouldRequireCaptcha()) {
    const ready = await ensureCaptchaReady();
    if (!ready) {
      showAlert('danger', 'Security check failed to load. Refresh and try again.');
      btn.disabled = false;
      spinner.classList.add('d-none');
      return;
    }
    if (!loginState.captchaToken) {
      showAlert('danger', 'Please complete the security check.');
      btn.disabled = false;
      spinner.classList.add('d-none');
      return;
    }
    requestBody.captcha_token = loginState.captchaToken;
  }

  try {
    const res = await fetch('/api/admin/login', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(requestBody)
    });
    const data = await res.json().catch(() => ({}));
    if (typeof data.failed_attempts === 'number') {
      loginState.failedAttempts = Math.max(0, Math.floor(data.failed_attempts));
    } else if (!res.ok && res.status === 401) {
      loginState.failedAttempts += 1;
    }
    if (data.captcha_required === true) {
      if (!loginState.captchaEnabled) {
        await loadLoginOptions();
      }
      if (loginState.captchaEnabled) {
        loginState.failedAttempts = Math.max(loginState.failedAttempts, loginState.captchaAfterFailures);
      }
    }
    refreshCaptchaVisibility();

    if (!res.ok) {
      let msg = data.error || 'Invalid credentials. Please try again.';
      if (res.status === 429 && data.retry_after_seconds) {
        msg = `Too many attempts. Try again in ${data.retry_after_seconds}s.`;
      }
      showAlert('danger', msg);
      resetCaptchaIfRendered();
      return;
    }

    showAlert('success', 'Login successful. Redirecting…');
    setTimeout(() => window.location.href = '/dashboard', 400);
  } catch (_) {
    showAlert('danger', 'Network error. Please try again.');
  } finally {
    btn.disabled = false;
    spinner.classList.add('d-none');
  }
});
