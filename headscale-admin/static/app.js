(function () {
  'use strict';
  var isEn = document.documentElement.lang.indexOf('en') === 0;
  var THEME_NAMES = {
    light: ['浅色', 'Light'], dark: ['深色', 'Dark'], cyberpunk: ['赛博朋克', 'Cyberpunk'],
    ocean: ['海洋', 'Ocean'], sunset: ['日落', 'Sunset'], forest: ['森林', 'Forest'],
    midnight: ['午夜', 'Midnight'], dracula: ['德古拉', 'Dracula'], nord: ['北欧', 'Nord'],
    rose: ['玫瑰', 'Rose'], coffee: ['咖啡', 'Coffee'], terminal: ['终端', 'Terminal'],
    pastel: ['柔和', 'Pastel'], synthwave: ['合成波', 'Synthwave']
  };

  // 下拉菜单
  document.querySelectorAll('.dropdown-btn').forEach(function (btn) {
    btn.addEventListener('click', function (e) {
      e.stopPropagation();
      var box = btn.parentElement;
      var wasOpen = box.classList.contains('open');
      document.querySelectorAll('.dropdown').forEach(function (d) { d.classList.remove('open'); });
      if (!wasOpen) box.classList.add('open');
    });
  });
  document.addEventListener('click', function () {
    document.querySelectorAll('.dropdown').forEach(function (d) { d.classList.remove('open'); });
  });
  document.querySelectorAll('#theme-menu .dropdown-item').forEach(function (item) {
    item.addEventListener('click', function () {
      var name = item.getAttribute('data-theme');
      document.documentElement.dataset.theme = name;
      try { localStorage.setItem('hsa-theme', name); } catch (e) {}
      var label = document.getElementById('theme-label');
      if (label) label.textContent = (THEME_NAMES[name] || [name, name])[isEn ? 1 : 0];
      document.querySelectorAll('#theme-menu .dropdown-item').forEach(function (b) {
        b.classList.toggle('active', b.getAttribute('data-theme') === name);
      });
    });
  });
  (function initTheme() {
    var t = document.documentElement.dataset.theme || 'light';
    var label = document.getElementById('theme-label');
    if (label && THEME_NAMES[t]) label.textContent = THEME_NAMES[t][isEn ? 1 : 0];
    var item = document.querySelector('#theme-menu .dropdown-item[data-theme="' + t + '"]');
    if (item) item.classList.add('active');
  })();

  // 移动端侧栏
  var sidebar = document.getElementById('sidebar');
  var overlay = document.getElementById('sidebar-overlay');
  var menuBtn = document.getElementById('menu-btn');
  function closeSidebar() {
    if (sidebar) sidebar.classList.remove('show');
    if (overlay) overlay.classList.remove('show');
  }
  if (menuBtn) menuBtn.addEventListener('click', function (e) {
    e.stopPropagation();
    if (sidebar) sidebar.classList.toggle('show');
    if (overlay) overlay.classList.toggle('show');
  });
  if (overlay) overlay.addEventListener('click', closeSidebar);

  // 网速面板
  if (!document.getElementById('netmon')) return;
  function fmtRate(v) {
    if (v >= 1024) return (v / 1024).toFixed(2) + ' MB/s';
    if (v >= 1) return v.toFixed(1) + ' KB/s';
    return Math.round(v * 1024) + ' B/s';
  }
  function fmtBytes(v) {
    if (v >= 1073741824) return (v / 1073741824).toFixed(2) + ' GB';
    if (v >= 1048576) return (v / 1048576).toFixed(1) + ' MB';
    if (v >= 1024) return (v / 1024).toFixed(1) + ' KB';
    return v + ' B';
  }
  function points(vals, max, close) {
    if (!vals || !vals.length) return '';
    var w = 600, h = 100, step = vals.length > 1 ? w / (vals.length - 1) : w;
    var out = vals.map(function (v, i) {
      return (i * step).toFixed(1) + ',' + (h - Math.min(1, v / max) * h * 0.9).toFixed(1);
    });
    if (close) { out.push(w + ',' + h); out.push('0,' + h); }
    return out.join(' ');
  }
  function setText(id, value) {
    var el = document.getElementById(id);
    if (el) el.textContent = value;
  }
  function renderNet(s) {
    if (!s) return;
    setText('net-down', fmtRate(s.total_rx));
    setText('net-up', fmtRate(s.total_tx));
    setText('net-total-down', fmtBytes(s.total_rx_bytes));
    setText('net-total-up', fmtBytes(s.total_tx_bytes));
    setText('net-time', isEn
      ? 'Updated at ' + s.updated + ', refreshes every 3s'
      : '更新于 ' + s.updated + '，每 3 秒自动刷新');
    var status = document.getElementById('net-status');
    if (status) {
      status.className = 'badge ' + (s.available ? 'success' : 'danger');
      status.textContent = s.available
        ? (isEn ? 'Live' : '实时')
        : (isEn ? 'Counters unavailable' : '无法读取网卡流量');
    }
    var rx = (s.history || []).map(function (p) { return p.rx; });
    var tx = (s.history || []).map(function (p) { return p.tx; });
    var max = Math.max.apply(null, rx.concat(tx).concat([1]));
    setText('spark-down', '');
    setText('spark-up', '');
    var d = document.getElementById('spark-down');
    var u = document.getElementById('spark-up');
    var f = document.getElementById('spark-down-fill');
    if (d) d.setAttribute('points', points(rx, max, false));
    if (u) u.setAttribute('points', points(tx, max, false));
    if (f) f.setAttribute('points', points(rx, max, true));
    var body = document.getElementById('net-ifaces');
    if (body) {
      body.innerHTML = '';
      (s.ifaces || []).forEach(function (i) {
        var tr = document.createElement('tr');
        [[i.name, 'b'], [fmtRate(i.rx_rate), 'ok'], [fmtRate(i.tx_rate), 'accent'],
         [fmtBytes(i.rx_total), 'muted'], [fmtBytes(i.tx_total), 'muted']].forEach(function (pair) {
          var td = document.createElement('td');
          td.className = pair[1];
          td.textContent = pair[0];
          tr.appendChild(td);
        });
        body.appendChild(tr);
      });
    }
  }
  function refresh() {
    fetch('/api/netmon', { credentials: 'same-origin' })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(renderNet)
      .catch(function () {});
  }
  refresh();
  setInterval(refresh, 3000);
})();

// 配置页：按证书模式显示对应字段
(function () {
  var sel = document.getElementById('tls_mode');
  if (!sel) return;
  function sync() {
    document.querySelectorAll('[data-tls-mode]').forEach(function (el) {
      el.hidden = el.getAttribute('data-tls-mode') !== sel.value;
    });
  }
  sel.addEventListener('change', sync);
  sync();
})();

// 运维页：本机资源、节点状态与告警。数据由 /api/monitor 每 5 秒下发，
// 节点列表本身在服务端每 15 秒采样一次，这里只负责渲染。
(function () {
  var root = document.getElementById('monitor');
  if (!root) return;
  var isEn = document.documentElement.lang.indexOf('en') === 0;
  var cpuThreshold = parseFloat(root.getAttribute('data-cpu')) || 85;
  var memThreshold = parseFloat(root.getAttribute('data-mem')) || 90;
  var rows = document.getElementById('node-rows');
  var routesLabel = rows ? (rows.getAttribute('data-routes') || (isEn ? 'Routes' : '路由')) : '';
  var speedNA = rows ? (rows.getAttribute('data-speed-na') || '') : '';

  function setText(id, v) {
    var el = document.getElementById(id);
    if (el && v !== undefined && v !== null) el.textContent = v;
  }
  function pct(v) { return Math.round(v) + '%'; }
  function level(v, th) { return v >= 97 ? 'danger' : v >= th ? 'warn' : ''; }
  function bar(id, v, th) {
    var el = document.getElementById(id);
    if (!el) return;
    el.style.width = Math.max(0, Math.min(100, v)) + '%';
    el.className = level(v, th);
  }
  function spark(id, vals, max) {
    var el = document.getElementById(id);
    if (!el) return;
    if (!vals || !vals.length) { el.setAttribute('points', ''); return; }
    var w = 600, h = 90, step = vals.length > 1 ? w / (vals.length - 1) : w;
    el.setAttribute('points', vals.map(function (v, i) {
      return (i * step).toFixed(1) + ',' + (h - Math.min(1, v / max) * h * 0.92).toFixed(1);
    }).join(' '));
  }

  function renderHost(h, th) {
    if (!h || !h.available) return;
    setText('host-cpu', pct(h.cpu_percent));
    setText('host-mem', pct(h.mem_percent));
    bar('host-cpu-bar', h.cpu_percent, (th && th.cpu) || cpuThreshold);
    bar('host-mem-bar', h.mem_percent, (th && th.mem) || memThreshold);
    var hist = h.history || [];
    spark('host-spark-cpu', hist.map(function (p) { return p.cpu; }), 100);
    spark('host-spark-mem', hist.map(function (p) { return p.mem; }), 100);
    setText('host-load', h.load_text);
    setText('host-mem-text', h.mem_detail_text);
    setText('host-uptime', h.uptime_text);
    var upd = document.getElementById('host-updated');
    if (upd) {
      var tpl = upd.getAttribute('data-tpl') || '__T__';
      upd.textContent = tpl.replace('__T__', h.updated);
    }
  }

  function cell(text, cls) {
    var td = document.createElement('td');
    if (cls) td.className = cls;
    if (text !== undefined && text !== null) td.textContent = text;
    return td;
  }
  function nodeRow(n) {
    var tr = document.createElement('tr');
    var name = cell(null, 'node-name');
    var b = document.createElement('b');
    b.textContent = n.name || ('#' + n.id);
    name.appendChild(b);
    var id = document.createElement('div');
    id.className = 'muted small';
    id.textContent = '#' + n.id;
    name.appendChild(id);
    tr.appendChild(name);

    tr.appendChild(cell(n.user || '—', 'small'));
    tr.appendChild(cell(n.ips && n.ips.length ? n.ips.join(', ') : '—', 'small mono nowrap'));

    var status = cell(null);
    var badge = document.createElement('span');
    badge.className = 'badge ' + (n.state === 'online' ? 'success' : n.state === 'expired' ? 'danger' : 'muted');
    if (n.state === 'online') {
      var dot = document.createElement('span');
      dot.className = 'dot';
      badge.appendChild(dot);
    }
    badge.appendChild(document.createTextNode(n.state_text || n.state));
    status.appendChild(badge);
    tr.appendChild(status);

    var speed = cell(null, 'small node-speed');
    if (n.speed_avail) {
      var down = document.createElement('span');
      down.className = 'ok';
      down.textContent = '↓ ' + (n.speed_down_text || '—');
      speed.appendChild(down);
      speed.appendChild(document.createElement('br'));
      var up = document.createElement('span');
      up.className = 'accent';
      up.textContent = '↑ ' + (n.speed_up_text || '—');
      speed.appendChild(up);
    } else {
      var na = document.createElement('span');
      na.className = 'muted';
      na.textContent = '—';
      if (speedNA) na.title = speedNA;
      speed.appendChild(na);
    }
    tr.appendChild(speed);

    var seen = cell(n.last_seen_text, 'small node-time');
    var abs = document.createElement('div');
    abs.className = 'muted';
    abs.textContent = n.last_seen_abs;
    seen.appendChild(abs);
    tr.appendChild(seen);

    var client = cell(n.version || '—', 'small node-client');
    var os = document.createElement('div');
    os.className = 'muted';
    os.textContent = n.os_text + ' · ' + n.register_text;
    client.appendChild(os);
    tr.appendChild(client);

    tr.appendChild(cell(n.expiry_text, 'small node-expiry'));

    var tagsRoutes = cell(null, 'small');
    if (n.tags && n.tags.length) {
      tagsRoutes.appendChild(document.createTextNode(n.tags.join(', ')));
      tagsRoutes.appendChild(document.createElement('br'));
    }
    var routes = document.createElement('span');
    routes.className = 'muted';
    routes.textContent = routesLabel + ' ' + n.approved_routes + ' / ' + n.available_routes;
    tagsRoutes.appendChild(routes);
    tr.appendChild(tagsRoutes);
    return tr;
  }

  function renderNodes(n) {
    if (!n) return;
    setText('node-total', n.total);
    setText('node-online', n.online);
    setText('node-offline', n.offline);
    setText('node-expired', n.expired);
    var status = document.getElementById('node-status');
    if (status) {
      status.className = 'badge ' + (n.error ? 'warning' : 'success');
      status.textContent = n.error
        ? (isEn ? 'Unavailable' : '读取失败')
        : (isEn ? 'Sampled every 15s' : '每 15 秒采集');
    }
    var err = document.getElementById('node-error');
    if (err) err.hidden = !n.error;
    if (n.error) {
      var et = document.getElementById('node-error-text');
      if (et) et.textContent = (et.getAttribute('data-tpl') || '__E__').replace('__E__', n.error);
    }
    if (rows) {
      rows.textContent = '';
      (n.nodes || []).forEach(function (item) { rows.appendChild(nodeRow(item)); });
    }
    var table = document.getElementById('node-table');
    var empty = document.getElementById('node-empty');
    var has = n.nodes && n.nodes.length > 0;
    if (table) table.hidden = !has;
    if (empty) empty.hidden = has;
  }

  function renderNodeMeta(n, updated) {
    var el = document.getElementById('node-updated');
    if (el && updated) {
      el.textContent = (el.getAttribute('data-tpl') || '__T__').replace('__T__', updated);
    }
  }

  function renderAlerts(list) {
    var card = document.getElementById('alerts');
    var box = document.getElementById('alert-list');
    if (!card || !box) return;
    var items = list || [];
    box.textContent = '';
    items.forEach(function (a) {
      var item = document.createElement('div');
      item.className = 'alert-item ' + (a.level || 'info');
      var dot = document.createElement('span');
      dot.className = 'dot';
      var text = document.createElement('span');
      text.textContent = a.text;
      item.appendChild(dot);
      item.appendChild(text);
      box.appendChild(item);
    });
    setText('alert-count', items.length);
    card.hidden = items.length === 0;
  }

  function refresh() {
    fetch('/api/monitor', { credentials: 'same-origin' })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (d) {
        if (!d) return;
        renderHost(d.host, d.thresholds);
        renderNodes(d.nodes);
        renderNodeMeta(d.nodes, d.updated);
        renderAlerts(d.alerts);
      })
      .catch(function () {});
  }
  refresh();
  setInterval(refresh, 5000);
})();

// 密钥页：客户端一键接入卡片 + 密钥显示/隐藏。
// server_url 与 key 由服务端注入到 data 属性，命令在这里实时拼装，
// 缺任一项时显示占位提示并禁用复制按钮，避免复制到半截命令。
(function () {
  var card = document.getElementById('join');
  if (!card) return;
  var EYE = '<svg xmlns="http://www.w3.org/2000/svg" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M2 12s3-7 10-7 10 7 10 7-3 7-10 7-10-7-10-7Z"/><circle cx="12" cy="12" r="3"/></svg>';
  var EYE_OFF = '<svg xmlns="http://www.w3.org/2000/svg" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M9.88 9.88a3 3 0 1 0 4.24 4.24"/><path d="M10.73 5.08A10.43 10.43 0 0 1 12 5c7 0 10 7 10 7a13.16 13.16 0 0 1-1.67 2.68"/><path d="M6.61 6.61A13.53 13.53 0 0 0 2 12s3 7 10 7a9.74 9.74 0 0 0 5.39-1.61"/><line x1="2" y1="2" x2="22" y2="22"/></svg>';

  var state = {
    url: card.getAttribute('data-server-url') || '',
    key: card.getAttribute('data-join-key') || '',
    show: false
  };
  var phServer = card.getAttribute('data-ph-server') || '<server_url>';
  var phKey = card.getAttribute('data-ph-key') || '<key>';
  var labelCopy = card.getAttribute('data-label-copy') || 'Copy';
  var labelCopied = card.getAttribute('data-label-copied') || 'Copied';
  var labelShow = card.getAttribute('data-label-show') || 'Show key';
  var labelHide = card.getAttribute('data-label-hide') || 'Hide key';

  var cmdEl = document.getElementById('join-cmd');
  var copyBtn = document.getElementById('join-copy');
  var copyLabel = document.getElementById('join-copy-label');
  var keyValue = document.getElementById('join-key-value');
  var toggle = document.getElementById('join-toggle');

  function mask(v) { return v ? '••••••••••••••••' : ''; }
  function part(text, cls) {
    var el = document.createElement('span');
    el.className = cls;
    el.textContent = text;
    return el;
  }
  function command() {
    if (!state.url || !state.key) return '';
    return 'tailscale up --login-server=' + state.url + ' --authkey=' + state.key;
  }
  function render() {
    if (cmdEl) {
      cmdEl.textContent = '';
      cmdEl.appendChild(document.createTextNode('tailscale up --login-server='));
      cmdEl.appendChild(part(state.url || phServer, state.url ? 'join-part' : 'join-ph'));
      cmdEl.appendChild(document.createTextNode(' --authkey='));
      cmdEl.appendChild(part(state.key ? (state.show ? state.key : mask(state.key)) : phKey, state.key ? 'join-part' : 'join-ph'));
    }
    if (copyBtn) copyBtn.disabled = !command();
    if (keyValue) keyValue.textContent = state.show ? state.key : mask(state.key);
    if (toggle) {
      toggle.innerHTML = state.show ? EYE_OFF : EYE;
      toggle.title = state.show ? labelHide : labelShow;
    }
  }

  function copyText(text, done) {
    function fallback() {
      var ta = document.createElement('textarea');
      ta.value = text;
      ta.style.position = 'fixed';
      ta.style.opacity = '0';
      document.body.appendChild(ta);
      ta.select();
      try { document.execCommand('copy'); } catch (e) {}
      document.body.removeChild(ta);
      done();
    }
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(done, fallback);
    } else {
      fallback();
    }
  }
  function flashCopied(btn, labelEl) {
    if (!labelEl) return;
    labelEl.textContent = labelCopied;
    if (btn) btn.classList.add('copied');
    setTimeout(function () {
      labelEl.textContent = labelCopy;
      if (btn) btn.classList.remove('copied');
    }, 1600);
  }

  if (copyBtn) {
    copyBtn.addEventListener('click', function () {
      var full = command();
      if (!full) return;
      copyText(full, function () { flashCopied(copyBtn, copyLabel); });
    });
  }
  if (toggle) {
    toggle.addEventListener('click', function () {
      state.show = !state.show;
      render();
    });
  }

  // 密钥列表：显示/隐藏、复制、填入命令。
  document.addEventListener('click', function (e) {
    var t = e.target.closest ? e.target.closest('.key-reveal, .key-copy, .key-use') : null;
    if (!t) return;
    var cellEl = t.closest('.key-cell');
    var textEl = cellEl ? cellEl.querySelector('.key-text') : null;
    var key = textEl ? (textEl.getAttribute('data-key') || '') : '';
    if (!key) return;
    if (t.classList.contains('key-reveal')) {
      var shown = textEl.classList.toggle('shown');
      textEl.textContent = shown ? key : '••••••••••••';
      t.innerHTML = shown ? EYE_OFF : EYE;
      t.title = shown ? labelHide : labelShow;
    } else if (t.classList.contains('key-copy')) {
      copyText(key, function () {
        t.classList.add('copied');
        setTimeout(function () { t.classList.remove('copied'); }, 1200);
      });
    } else if (t.classList.contains('key-use')) {
      state.key = key;
      state.show = true;
      render();
      card.scrollIntoView({ behavior: 'smooth', block: 'start' });
    }
  });

  render();
})();
