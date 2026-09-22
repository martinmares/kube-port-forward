(() => {
  'use strict';

  const elements = {
    clusterPill: document.getElementById('cluster-pill'),
    clusterPillLabel: document.getElementById('cluster-pill-label'),
    clusterSummary: document.getElementById('cluster-summary'),
    resourceSummary: document.getElementById('resource-summary'),
    connectionAlert: document.getElementById('connection-alert'),
    liveState: document.getElementById('live-state'),
    body: document.getElementById('forwards-body'),
    mobile: document.getElementById('forwards-mobile'),
    search: document.getElementById('forward-search'),
    statusFilter: document.getElementById('status-filter'),
    resourceFilter: document.getElementById('resource-filter'),
    showHistory: document.getElementById('show-history'),
    visibleCount: document.getElementById('visible-count'),
    copyToast: document.getElementById('copy-toast'),
    diagnosticCard: document.getElementById('diagnostic-card'),
    diagnosticSubtitle: document.getElementById('diagnostic-subtitle'),
    diagnosticStatus: document.getElementById('diagnostic-status'),
    diagnosticStop: document.getElementById('diagnostic-stop'),
    diagnosticUnconfigured: document.getElementById('diagnostic-unconfigured'),
    diagnosticContent: document.getElementById('diagnostic-content'),
    diagnosticError: document.getElementById('diagnostic-error'),
    diagnosticConfigForm: document.getElementById('diagnostic-config-form'),
    diagnosticProgress: document.getElementById('diagnostic-progress'),
    diagnosticProgressTitle: document.getElementById('diagnostic-progress-title'),
    diagnosticProgressDetail: document.getElementById('diagnostic-progress-detail'),
    diagnosticReady: document.getElementById('diagnostic-ready'),
    diagnosticStart: document.getElementById('diagnostic-start'),
    diagnosticImage: document.getElementById('diagnostic-image'),
    diagnosticPullPolicy: document.getElementById('diagnostic-pull-policy'),
    diagnosticTTL: document.getElementById('diagnostic-ttl'),
    diagnosticMaxConnections: document.getElementById('diagnostic-max-connections'),
    diagnosticConnectTimeout: document.getElementById('diagnostic-connect-timeout'),
    diagnosticIdleTimeout: document.getElementById('diagnostic-idle-timeout'),
    diagnosticPullSecrets: document.getElementById('diagnostic-pull-secrets'),
    diagnosticPolicy: document.getElementById('diagnostic-policy'),
    diagnosticConfigPath: document.getElementById('diagnostic-config-path'),
    diagnosticTunnelForm: document.getElementById('diagnostic-tunnel-form'),
    diagnosticTunnels: document.getElementById('diagnostic-tunnels'),
    tunnelName: document.getElementById('tunnel-name'),
    tunnelHost: document.getElementById('tunnel-host'),
    tunnelPort: document.getElementById('tunnel-port'),
    tunnelLocalPort: document.getElementById('tunnel-local-port'),
    tunnelMode: document.getElementById('tunnel-mode'),
    tunnelTLSOptions: document.getElementById('tunnel-tls-options'),
    tunnelTLSSkipVerify: document.getElementById('tunnel-tls-skip-verify'),
    tunnelAdd: document.getElementById('tunnel-add'),
  };

  const statusMeta = {
    forwarding: { label: 'Forwarding', badge: 'bg-green-lt text-green', icon: 'ti-plug-connected', rank: 0 },
    connecting: { label: 'Connecting', badge: 'bg-blue-lt text-blue', icon: 'ti-loader-2', rank: 1 },
    reconnecting: { label: 'Reconnecting', badge: 'bg-yellow-lt text-yellow', icon: 'ti-refresh', rank: 2 },
    terminated: { label: 'Terminated', badge: 'bg-secondary-lt text-secondary', icon: 'ti-history', rank: 3 },
  };

  let currentState = null;
  let lastUpdate = null;
  let eventStreamConnected = false;
  let diagnosticFormLoaded = false;
  let diagnosticRequestError = '';

  function escapeHTML(value) {
    return String(value ?? '')
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#039;');
  }

  function splitResources(value) {
    return String(value || '')
      .split(',')
      .map(resource => resource.trim())
      .filter(Boolean);
  }

  function resourceBadges(value) {
    const resources = splitResources(value);
    if (!resources.length) return '<span class="text-secondary">—</span>';
    return '<div class="resource-list">' + resources.map(resource =>
      '<span class="badge bg-azure-lt text-azure" title="' + escapeHTML(resource) + '">' + escapeHTML(resource) + '</span>'
    ).join('') + '</div>';
  }

  function applyTheme(theme) {
    const dark = theme === 'dark' || (theme === 'auto' && window.matchMedia('(prefers-color-scheme: dark)').matches);
    document.documentElement.setAttribute('data-bs-theme', dark ? 'dark' : 'light');
    const icons = { light: 'ti-sun', dark: 'ti-moon', auto: 'ti-device-desktop' };
    document.getElementById('theme-icon').className = 'ti ' + (icons[theme] || icons.auto);
  }

  function setupTheme() {
    const saved = localStorage.getItem('theme') || 'auto';
    applyTheme(saved);
    document.querySelectorAll('[data-theme]').forEach(button => {
      button.addEventListener('click', () => {
        const theme = button.dataset.theme || 'auto';
        localStorage.setItem('theme', theme);
        applyTheme(theme);
      });
    });
    window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
      if ((localStorage.getItem('theme') || 'auto') === 'auto') applyTheme('auto');
    });
  }

  function renderHeader(state) {
    const cluster = state.cluster || {};
    const resources = state.resources || [];
    const namespaces = [...new Set(resources.map(item => item.namespace).filter(Boolean))];
    const clusterName = cluster.clusterName || cluster.contextName || 'Kubernetes cluster';

    elements.clusterPillLabel.textContent = clusterName;
    elements.clusterPill.title = cluster.serverURL || clusterName;
    elements.clusterPill.className = 'badge bg-green-lt text-green cluster-pill';
    const pillDot = elements.clusterPill.querySelector('.status-dot');
    if (pillDot) pillDot.className = 'status-dot status-dot-animated bg-green me-1';

    const context = cluster.contextName || 'unknown context';
    const namespace = namespaces.join(', ') || cluster.namespace || 'default namespace';
    const server = cluster.serverURL || 'unknown API server';
    elements.clusterSummary.textContent = context + ' · ' + namespace + ' · ' + server;
    elements.clusterSummary.title = elements.clusterSummary.textContent;

    elements.resourceSummary.innerHTML = resources.map(item =>
      '<span class="badge bg-secondary-lt text-secondary">' +
      '<i class="ti ti-target-arrow me-1"></i>' + escapeHTML(item.resource) +
      '</span>'
    ).join('');
  }

  function renderMetrics(pods) {
    const counts = { forwarding: 0, connecting: 0, reconnecting: 0, terminated: 0 };
    pods.forEach(pod => { if (Object.hasOwn(counts, pod.status)) counts[pod.status] += 1; });
    Object.entries(counts).forEach(([status, count]) => {
      document.getElementById('metric-' + status).textContent = String(count);
    });

    const active = counts.forwarding + counts.connecting + counts.reconnecting;
    document.title = active > 0 ? '(' + counts.forwarding + '/' + active + ') kube-port-forward' : 'kube-port-forward';
    const color = active === 0 ? '#6c7a91' : counts.forwarding === active ? '#2fb344' : counts.forwarding > 0 ? '#f59f00' : '#d63939';
    setFavicon(color);
  }

  function updateResourceFilter(pods) {
    const selected = elements.resourceFilter.value;
    const resources = [...new Set(pods.flatMap(pod => splitResources(pod.resource)))].sort();
    elements.resourceFilter.innerHTML = '<option value="all">All resources</option>' + resources.map(resource =>
      '<option value="' + escapeHTML(resource) + '">' + escapeHTML(resource) + '</option>'
    ).join('');
    elements.resourceFilter.value = resources.includes(selected) ? selected : 'all';
  }

  function filteredPods(pods) {
    const query = elements.search.value.trim().toLowerCase();
    const status = elements.statusFilter.value;
    const resource = elements.resourceFilter.value;
    const showHistory = elements.showHistory.checked || status === 'terminated';

    return pods.filter(pod => {
      if (!showHistory && pod.status === 'terminated') return false;
      if (status !== 'all' && pod.status !== status) return false;
      if (resource !== 'all' && !splitResources(pod.resource).includes(resource)) return false;
      if (!query) return true;
      return [pod.podName, pod.resource, pod.localPort, pod.remotePort, pod.nodeName, pod.podIP, pod.ready, pod.status]
        .join(' ').toLowerCase().includes(query);
    }).sort((a, b) => {
      const rankA = statusMeta[a.status]?.rank ?? 99;
      const rankB = statusMeta[b.status]?.rank ?? 99;
      return rankA - rankB || a.localPort - b.localPort || String(a.podName).localeCompare(String(b.podName));
    });
  }

  function readyBadge(ready) {
    const parts = String(ready || '').split('/');
    const healthy = parts.length === 2 && parts[0] === parts[1] && parts[1] !== '0';
    const css = healthy ? 'bg-green-lt text-green' : 'bg-yellow-lt text-yellow';
    return '<span class="badge ' + css + '">' + escapeHTML(ready || '—') + '</span>';
  }

  function statusBadge(status) {
    const meta = statusMeta[status] || { label: status || 'Unknown', badge: 'bg-secondary-lt text-secondary', icon: 'ti-help' };
    return '<span class="badge ' + meta.badge + '"><i class="ti ' + meta.icon + ' me-1"></i>' + escapeHTML(meta.label) + '</span>';
  }

  function renderRows() {
    if (!currentState) return;
    const allPods = currentState.pods || [];
    const pods = filteredPods(allPods);
    elements.visibleCount.textContent = String(pods.length);

    if (!allPods.length) {
      elements.body.innerHTML = emptyRow('ti-plug-connected-x', 'No forwards found', 'Waiting for matching pods and declared container ports.');
      elements.mobile.innerHTML = emptyState('ti-plug-connected-x', 'No forwards found', 'Waiting for matching pods and declared container ports.');
      return;
    }
    if (!pods.length) {
      const hiddenHistory = !elements.showHistory.checked && allPods.every(pod => pod.status === 'terminated');
      elements.body.innerHTML = emptyRow(
        hiddenHistory ? 'ti-history' : 'ti-filter-off',
        hiddenHistory ? 'Only history is available' : 'No matching forwards',
        hiddenHistory ? 'Enable “Show history” to display terminated tunnels.' : 'Try changing the search or filter settings.'
      );
      elements.mobile.innerHTML = emptyState(
        hiddenHistory ? 'ti-history' : 'ti-filter-off',
        hiddenHistory ? 'Only history is available' : 'No matching forwards',
        hiddenHistory ? 'Enable “Show history” to display terminated tunnels.' : 'Try changing the search or filter settings.'
      );
      return;
    }

    elements.body.innerHTML = pods.map(pod => {
      const terminated = pod.status === 'terminated';
      const restartsClass = pod.restarts === 0 ? 'text-secondary' : pod.restarts < 5 ? 'text-yellow' : 'text-red';
      const attempts = pod.attempt > 1 ? '<span class="text-secondary small ms-1">attempt ' + escapeHTML(pod.attempt) + '</span>' : '';
      const actions = terminated ? '<span class="text-secondary">—</span>' :
        '<div class="action-list">' +
          '<a class="btn btn-sm btn-icon btn-ghost-primary" href="' + escapeHTML(pod.url) + '" target="_blank" rel="noopener" title="Open endpoint" aria-label="Open ' + escapeHTML(pod.url) + '"><i class="ti ti-external-link"></i></a>' +
          '<button class="btn btn-sm btn-icon btn-ghost-secondary" type="button" data-copy-url="' + escapeHTML(pod.url) + '" title="Copy endpoint URL" aria-label="Copy ' + escapeHTML(pod.url) + '"><i class="ti ti-copy"></i></button>' +
        '</div>';

      return '<tr class="' + (terminated ? 'is-terminated' : '') + '">' +
        '<td class="status-column">' + statusBadge(pod.status) + '</td>' +
        '<td class="resource-column">' + resourceBadges(pod.resource) + '</td>' +
        '<td class="pod-column pod-cell">' +
          '<span class="fw-semibold pod-name" title="' + escapeHTML(pod.podName) + '">' + escapeHTML(pod.podName) + '</span>' +
          '<div class="mobile-meta text-secondary small">' +
            '<span><i class="ti ti-server me-1"></i>' + escapeHTML(pod.nodeName || '—') + '</span>' +
            '<span><i class="ti ti-clock me-1"></i><span data-start="' + escapeHTML(pod.startTime || '') + '">' + age(pod.startTime) + '</span></span>' +
          '</div>' +
        '</td>' +
        '<td class="port-column"><span class="port-mapping"><span class="port-local">127.0.0.1:' + escapeHTML(pod.localPort) + '</span><i class="ti ti-arrow-right text-secondary"></i><strong>' + escapeHTML(pod.remotePort) + '</strong></span>' + attempts + '</td>' +
        '<td class="ready-column d-none d-xl-table-cell">' + readyBadge(pod.ready) + '<div class="small mt-1 restart-count ' + restartsClass + '">' + escapeHTML(pod.restarts) + ' restarts</div></td>' +
        '<td class="node-column d-none d-xxl-table-cell node-cell"><code class="node-name" title="' + escapeHTML(pod.nodeName || '') + '">' + escapeHTML(pod.nodeName || '—') + '</code><div class="pod-ip text-secondary small">' + escapeHTML(pod.podIP || '—') + '</div></td>' +
        '<td class="age-column d-none d-xxl-table-cell text-secondary" data-start="' + escapeHTML(pod.startTime || '') + '">' + age(pod.startTime) + '</td>' +
        '<td class="actions-column text-end">' + actions + '</td>' +
      '</tr>';
    }).join('');
    elements.mobile.innerHTML = pods.map(renderMobileCard).join('');
  }

  function renderDiagnostic(diagnostic) {
    const configured = Boolean(diagnostic?.configured);
    elements.diagnosticUnconfigured.classList.toggle('d-none', configured);
    elements.diagnosticContent.classList.toggle('d-none', !configured);
    if (!configured) {
      elements.diagnosticSubtitle.textContent = 'No local diagnostic profile loaded';
      elements.diagnosticStatus.className = 'badge bg-secondary-lt text-secondary';
      elements.diagnosticStatus.textContent = 'Unavailable';
      return;
    }

    if (!diagnosticFormLoaded) {
      elements.diagnosticImage.value = diagnostic.image || '';
      elements.diagnosticPullPolicy.value = diagnostic.pullPolicy || 'IfNotPresent';
      elements.diagnosticTTL.value = diagnostic.ttl || '8h';
      elements.diagnosticMaxConnections.value = diagnostic.maxConnections || 64;
      elements.diagnosticConnectTimeout.value = diagnostic.connectTimeout || '10s';
      elements.diagnosticIdleTimeout.value = diagnostic.idleTimeout || '15m';
      elements.diagnosticPullSecrets.value = (diagnostic.imagePullSecrets || []).join(', ');
      elements.diagnosticPolicy.value = diagnostic.policyYAML || '';
      elements.diagnosticConfigPath.textContent = diagnostic.configPath ? 'Loaded from ' + diagnostic.configPath : '';
      elements.diagnosticConfigPath.title = diagnostic.configPath || '';
      diagnosticFormLoaded = true;
    }

    const status = diagnostic.status || 'stopped';
    const statusStyles = {
      stopped: ['Stopped', 'badge bg-secondary-lt text-secondary'],
      starting: ['Starting', 'badge bg-blue-lt text-blue'],
      ready: ['Ready', 'badge bg-green-lt text-green'],
      stopping: ['Stopping', 'badge bg-yellow-lt text-yellow'],
      error: ['Error', 'badge bg-red-lt text-red'],
    };
    const statusStyle = statusStyles[status] || [status, 'badge bg-secondary-lt text-secondary'];
    elements.diagnosticStatus.textContent = statusStyle[0];
    elements.diagnosticStatus.className = statusStyle[1];
    elements.diagnosticSubtitle.textContent = diagnostic.podName
      ? diagnostic.podName + ' · ' + diagnostic.namespace
      : 'Private cluster egress gateway · ' + diagnostic.namespace;
    elements.diagnosticSubtitle.title = elements.diagnosticSubtitle.textContent;

    const showConfig = status === 'stopped' || status === 'error';
    const showProgress = status === 'starting' || status === 'stopping';
    const showReady = status === 'ready';
    elements.diagnosticConfigForm.classList.toggle('d-none', !showConfig);
    elements.diagnosticProgress.classList.toggle('d-none', !showProgress);
    elements.diagnosticReady.classList.toggle('d-none', !showReady);
    elements.diagnosticStop.classList.toggle('d-none', status === 'stopped');
    elements.diagnosticStop.disabled = status === 'stopping';
    setDiagnosticStopBusy(status === 'stopping');
    if (status === 'stopping') {
      elements.diagnosticProgressTitle.textContent = 'Stopping diagnostic pod…';
      elements.diagnosticProgressDetail.textContent = 'Waiting for Kubernetes to remove the Pod, ConfigMap, and Secret.';
    } else {
      elements.diagnosticProgressTitle.textContent = 'Starting diagnostic pod…';
      elements.diagnosticProgressDetail.textContent = 'Creating the ConfigMap, waiting for readiness, and opening a private Kubernetes port-forward.';
    }

    const error = diagnosticRequestError || diagnostic.error || '';
    elements.diagnosticError.textContent = error;
    elements.diagnosticError.classList.toggle('d-none', !error);
    renderDiagnosticTunnels(diagnostic.tunnels || []);
  }

  function setDiagnosticStopBusy(busy) {
    elements.diagnosticStop.innerHTML = busy
      ? '<span class="spinner-border spinner-border-sm me-1" aria-hidden="true"></span><span>Stopping…</span>'
      : '<i class="ti ti-player-stop me-1"></i><span>Stop gateway</span>';
  }

  function renderDiagnosticTunnels(tunnels) {
    if (!tunnels.length) {
      elements.diagnosticTunnels.innerHTML = emptyState(
        'ti-route-off',
        'No exposed endpoints',
        'Add a hostname and port above. Local ports are bound only to 127.0.0.1.'
      );
      return;
    }
    elements.diagnosticTunnels.innerHTML = '<div class="diagnostic-tunnel-list">' + tunnels.map(tunnel => {
      const localValue = tunnel.url || tunnel.address;
      const insecureTLS = tunnel.mode === 'https' && tunnel.tlsSkipVerify
        ? '<span class="badge bg-red-lt text-red mt-1 ms-1">TLS verify off</span>'
        : '';
      const open = tunnel.url
        ? '<a class="btn btn-sm btn-icon btn-ghost-primary" href="' + escapeHTML(tunnel.url) + '" target="_blank" rel="noopener" title="Open endpoint" aria-label="Open ' + escapeHTML(tunnel.url) + '"><i class="ti ti-external-link"></i></a>'
        : '';
      return '<div class="diagnostic-tunnel-item">' +
        '<div class="min-w-0"><div class="fw-semibold text-truncate">' + escapeHTML(tunnel.name) + '</div><span class="badge bg-purple-lt text-purple mt-1">' + escapeHTML(String(tunnel.mode).toUpperCase()) + '</span>' + insecureTLS + '</div>' +
        '<code class="diagnostic-tunnel-target" title="' + escapeHTML(tunnel.host + ':' + tunnel.port) + '">' + escapeHTML(tunnel.host) + ':' + escapeHTML(tunnel.port) + '</code>' +
        '<code class="diagnostic-tunnel-address text-primary" title="' + escapeHTML(localValue) + '">' + escapeHTML(localValue) + '</code>' +
        '<div class="action-list text-end">' + open +
          '<button class="btn btn-sm btn-icon btn-ghost-secondary" type="button" data-copy-url="' + escapeHTML(localValue) + '" title="Copy local endpoint" aria-label="Copy ' + escapeHTML(localValue) + '"><i class="ti ti-copy"></i></button>' +
          '<button class="btn btn-sm btn-icon btn-ghost-danger" type="button" data-delete-tunnel="' + escapeHTML(tunnel.id) + '" title="Stop tunnel" aria-label="Stop ' + escapeHTML(tunnel.name) + '"><i class="ti ti-trash"></i></button>' +
        '</div></div>';
    }).join('') + '</div>';
  }

  function renderMobileCard(pod) {
    const terminated = pod.status === 'terminated';
    const restartsClass = pod.restarts === 0 ? 'text-secondary' : pod.restarts < 5 ? 'text-yellow' : 'text-red';
    const actions = terminated ? '' :
      '<div class="action-list ms-auto">' +
        '<a class="btn btn-sm btn-icon btn-ghost-primary" href="' + escapeHTML(pod.url) + '" target="_blank" rel="noopener" title="Open endpoint" aria-label="Open ' + escapeHTML(pod.url) + '"><i class="ti ti-external-link"></i></a>' +
        '<button class="btn btn-sm btn-icon btn-ghost-secondary" type="button" data-copy-url="' + escapeHTML(pod.url) + '" title="Copy endpoint URL" aria-label="Copy ' + escapeHTML(pod.url) + '"><i class="ti ti-copy"></i></button>' +
      '</div>';

    return '<article class="mobile-forward-item ' + (terminated ? 'is-terminated' : '') + '">' +
      '<div class="d-flex align-items-center gap-2 mb-2">' + statusBadge(pod.status) + actions + '</div>' +
      '<div class="fw-semibold mobile-forward-name mb-2">' + escapeHTML(pod.podName) + '</div>' +
      '<div class="mb-3">' + resourceBadges(pod.resource) + '</div>' +
      '<div class="port-mapping mb-3"><span class="port-local">127.0.0.1:' + escapeHTML(pod.localPort) + '</span><i class="ti ti-arrow-right text-secondary"></i><strong>' + escapeHTML(pod.remotePort) + '</strong>' +
        (pod.attempt > 1 ? '<span class="text-secondary">· attempt ' + escapeHTML(pod.attempt) + '</span>' : '') + '</div>' +
      '<div class="mobile-forward-meta">' +
        '<div class="mobile-forward-meta-item"><div class="text-secondary">Ready</div>' + readyBadge(pod.ready) + '</div>' +
        '<div class="mobile-forward-meta-item"><div class="text-secondary">Restarts</div><span class="' + restartsClass + '">' + escapeHTML(pod.restarts) + '</span></div>' +
        '<div class="mobile-forward-meta-item"><div class="text-secondary">Node</div><code title="' + escapeHTML(pod.nodeName || '') + '">' + escapeHTML(pod.nodeName || '—') + '</code></div>' +
        '<div class="mobile-forward-meta-item"><div class="text-secondary">Pod IP</div><code>' + escapeHTML(pod.podIP || '—') + '</code></div>' +
        '<div class="mobile-forward-meta-item"><div class="text-secondary">Age</div><span data-start="' + escapeHTML(pod.startTime || '') + '">' + age(pod.startTime) + '</span></div>' +
      '</div>' +
    '</article>';
  }

  function emptyRow(icon, title, subtitle) {
    return '<tr><td colspan="8">' + emptyState(icon, title, subtitle) + '</td></tr>';
  }

  function emptyState(icon, title, subtitle) {
    return '<div class="empty py-5">' +
      '<div class="empty-icon"><i class="ti ' + icon + '"></i></div>' +
      '<p class="empty-title">' + escapeHTML(title) + '</p>' +
      '<p class="empty-subtitle text-secondary">' + escapeHTML(subtitle) + '</p>' +
    '</div>';
  }

  function render(state) {
    currentState = state;
    lastUpdate = new Date();
    const pods = state.pods || [];
    renderHeader(state);
    renderMetrics(pods);
    renderDiagnostic(state.diagnostic || {});
    updateResourceFilter(pods);
    renderRows();
    updateLiveState();
  }

  function age(value) {
    if (!value) return '—';
    const seconds = Math.max(0, Math.floor((Date.now() - new Date(value).getTime()) / 1000));
    if (!Number.isFinite(seconds)) return '—';
    if (seconds < 60) return seconds + 's';
    const minutes = Math.floor(seconds / 60);
    if (minutes < 60) return minutes + 'm';
    const hours = Math.floor(minutes / 60);
    if (hours < 24) return hours + 'h ' + (minutes % 60) + 'm';
    const days = Math.floor(hours / 24);
    return days + 'd ' + (hours % 24) + 'h';
  }

  function relativeUpdate() {
    if (!lastUpdate) return 'waiting for first state';
    const seconds = Math.max(0, Math.floor((Date.now() - lastUpdate.getTime()) / 1000));
    if (seconds < 2) return 'updated just now';
    if (seconds < 60) return 'updated ' + seconds + 's ago';
    return 'updated ' + Math.floor(seconds / 60) + 'm ago';
  }

  function updateLiveState() {
    const dotClass = eventStreamConnected ? 'bg-green' : 'bg-yellow';
    const label = eventStreamConnected ? 'Live updates connected · ' + relativeUpdate() : 'Reconnecting to live updates…';
    elements.liveState.innerHTML = '<span class="status-dot status-dot-animated ' + dotClass + '"></span>' + escapeHTML(label);
    elements.connectionAlert.classList.toggle('d-none', eventStreamConnected);
  }

  function refreshTimes() {
    document.querySelectorAll('[data-start]').forEach(element => {
      element.textContent = age(element.dataset.start);
    });
    updateLiveState();
  }

  function setFavicon(color) {
    const svg = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><circle cx="8" cy="8" r="7" fill="' + color + '"/></svg>';
    let link = document.querySelector('link[rel="icon"]');
    if (!link) {
      link = document.createElement('link');
      link.rel = 'icon';
      document.head.appendChild(link);
    }
    link.href = 'data:image/svg+xml,' + encodeURIComponent(svg);
  }

  async function copyURL(url) {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(url);
    } else {
      const input = document.createElement('textarea');
      input.value = url;
      input.style.position = 'fixed';
      input.style.opacity = '0';
      document.body.appendChild(input);
      input.select();
      document.execCommand('copy');
      input.remove();
    }
    tabler.Toast.getOrCreateInstance(elements.copyToast, { delay: 1800 }).show();
  }

  async function apiRequest(path, options = {}) {
    const response = await fetch(path, {
      ...options,
      headers: {
        'Content-Type': 'application/json',
        'X-Kube-Port-Forward': '1',
        ...(options.headers || {}),
      },
    });
    if (!response.ok) {
      let message = response.statusText;
      try {
        const body = await response.json();
        message = body.error || message;
      } catch (_) {
        // Keep the HTTP status text when an intermediary returned a non-JSON response.
      }
      throw new Error(message);
    }
    if (response.status === 204) return null;
    return response.json();
  }

  function setDiagnosticRequestError(error) {
    diagnosticRequestError = error ? String(error.message || error) : '';
    if (currentState) renderDiagnostic(currentState.diagnostic || {});
  }

  setupTheme();
  function updateTunnelTLSOptions() {
    const isHTTPS = elements.tunnelMode.value === 'https';
    elements.tunnelTLSOptions.classList.toggle('d-none', !isHTTPS);
    if (!isHTTPS) elements.tunnelTLSSkipVerify.checked = false;
  }
  elements.tunnelMode.addEventListener('change', updateTunnelTLSOptions);
  updateTunnelTLSOptions();
  [elements.search, elements.statusFilter, elements.resourceFilter, elements.showHistory].forEach(control => {
    control.addEventListener(control === elements.search ? 'input' : 'change', renderRows);
  });
  document.addEventListener('click', event => {
    const button = event.target.closest('[data-copy-url]');
    if (!button) return;
    copyURL(button.dataset.copyUrl).catch(error => console.warn('Could not copy URL', error));
  });
  document.addEventListener('click', event => {
    const button = event.target.closest('[data-delete-tunnel]');
    if (!button) return;
    button.disabled = true;
    setDiagnosticRequestError(null);
    apiRequest('/api/diagnostic/tunnels/' + encodeURIComponent(button.dataset.deleteTunnel), { method: 'DELETE' })
      .catch(setDiagnosticRequestError)
      .finally(() => { button.disabled = false; });
  });

  elements.diagnosticConfigForm.addEventListener('submit', event => {
    event.preventDefault();
    setDiagnosticRequestError(null);
    elements.diagnosticStart.disabled = true;
    const payload = {
      image: elements.diagnosticImage.value.trim(),
      pullPolicy: elements.diagnosticPullPolicy.value,
      imagePullSecrets: elements.diagnosticPullSecrets.value.split(',').map(value => value.trim()).filter(Boolean),
      ttl: elements.diagnosticTTL.value.trim(),
      maxConnections: Number(elements.diagnosticMaxConnections.value),
      connectTimeout: elements.diagnosticConnectTimeout.value.trim(),
      idleTimeout: elements.diagnosticIdleTimeout.value.trim(),
      policyYAML: elements.diagnosticPolicy.value,
    };
    apiRequest('/api/diagnostic/start', { method: 'POST', body: JSON.stringify(payload) })
      .catch(setDiagnosticRequestError)
      .finally(() => { elements.diagnosticStart.disabled = false; });
  });

  elements.diagnosticStop.addEventListener('click', () => {
    setDiagnosticRequestError(null);
    elements.diagnosticStop.disabled = true;
    setDiagnosticStopBusy(true);
    apiRequest('/api/diagnostic/stop', { method: 'POST', body: '{}' })
      .catch(setDiagnosticRequestError)
      .finally(() => {
        elements.diagnosticStop.disabled = false;
        if (currentState?.diagnostic?.status !== 'stopping') setDiagnosticStopBusy(false);
      });
  });

  elements.diagnosticTunnelForm.addEventListener('submit', event => {
    event.preventDefault();
    setDiagnosticRequestError(null);
    elements.tunnelAdd.disabled = true;
    const payload = {
      name: elements.tunnelName.value.trim(),
      host: elements.tunnelHost.value.trim(),
      port: Number(elements.tunnelPort.value),
      localPort: elements.tunnelLocalPort.value ? Number(elements.tunnelLocalPort.value) : 0,
      mode: elements.tunnelMode.value,
      tlsSkipVerify: elements.tunnelMode.value === 'https' && elements.tunnelTLSSkipVerify.checked,
    };
    apiRequest('/api/diagnostic/tunnels', { method: 'POST', body: JSON.stringify(payload) })
      .then(() => {
        elements.tunnelName.value = '';
        elements.tunnelHost.value = '';
        elements.tunnelPort.value = '';
        elements.tunnelLocalPort.value = '';
        elements.tunnelHost.focus();
      })
      .catch(setDiagnosticRequestError)
      .finally(() => { elements.tunnelAdd.disabled = false; });
  });

  const events = new EventSource('/events');
  events.onopen = () => {
    eventStreamConnected = true;
    updateLiveState();
  };
  events.onmessage = event => {
    try {
      render(JSON.parse(event.data));
    } catch (error) {
      console.warn('Invalid live state', error);
    }
  };
  events.onerror = () => {
    eventStreamConnected = false;
    updateLiveState();
  };

  setInterval(refreshTimes, 1000);
})();
