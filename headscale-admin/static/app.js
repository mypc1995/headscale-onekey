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
