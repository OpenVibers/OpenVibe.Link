/* OpenVibe Link's window: draws what the daemon reports (GET /panel/api/state every 3 s while visible) and sends the
   person's choices back. Every write carries the panel's session header (the daemon gives it in the page's
   address fragment, #t=…, and refuses writes without it). Text only: nothing from the daemon is ever parsed as HTML. */
(function () {
    'use strict';
    var root = document.querySelector('.lk');
    if (!root) return;
    var token = (/[#&]t=([A-Za-z0-9_-]{16,128})/.exec(location.hash) || [])[1] || '';
    var KIND = { agent: '✦', desk: '▣', share: '⇄', model: '◆' };
    var $ = function (sel) { return root.querySelector(sel); };
    function el(tag, cls, text) { var n = document.createElement(tag); if (cls) n.className = cls; if (text != null) n.textContent = text; return n; }
    function ago(iso) {
        var t = Date.parse(iso); if (!isFinite(t)) return '';
        var s = Math.max(0, (Date.now() - t) / 1000);
        return s < 60 ? 'just now' : s < 3600 ? Math.floor(s / 60) + ' min' : s < 86400 ? Math.floor(s / 3600) + ' h' : Math.floor(s / 86400) + ' d';
    }
    function hhmm(iso) { var d = new Date(iso); return isNaN(d) ? '' : d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }); }
    function send(path, method, body) {
        return fetch('/panel/api/' + path, { method: method || 'POST', headers: { 'Content-Type': 'application/json', 'X-Link-Panel': token }, body: body ? JSON.stringify(body) : undefined, credentials: 'same-origin' })
            .then(function (r) { if (!r.ok) throw new Error('Link said ' + r.status); return r.status === 204 ? null : r.json().catch(function () { return null; }); })
            .then(function () { load(); });
    }

    function draw(s) {
        var m = s.machine || {};
        var running = Array.isArray(s.running) ? s.running : [];
        root.setAttribute('aria-busy', 'false');
        root.setAttribute('data-state', m.paused ? 'paused' : running.length ? 'running' : 'online');
        root.setAttribute('data-needs', m.needs_you ? '1' : '0');
        root.style.setProperty('--fill', String(Math.max(10, 100 - Math.min(running.length, 4) * 22)));
        $('[data-lk-down]').hidden = true;
        $('[data-lk-name]').textContent = m.name || 'This computer';
        $('[data-lk-status]').textContent = m.paused ? 'Paused: nothing runs until you resume'
            : m.needs_you ? (m.needs_you === 1 ? 'Online · something needs you' : 'Online · ' + m.needs_you + ' things need you')
                : running.length ? 'Online · ' + running.length + ' running' : 'Online · ready';
        var pause = $('[data-lk-pause]');
        pause.setAttribute('aria-pressed', m.paused ? 'true' : 'false');
        $('[data-lk-pause-label]').textContent = m.paused ? 'Resume' : 'Pause everything';

        $('[data-lk-run-n]').textContent = running.length ? '(' + running.length + ')' : '';
        $('[data-lk-running]').replaceChildren.apply($('[data-lk-running]'), running.map(function (r) {
            var li = el('li'); var k = el('span', 'lk-kind', KIND[r.kind] || '•'); k.setAttribute('data-kind', r.kind || ''); k.setAttribute('aria-hidden', 'true');
            var t = el('span'); t.append(el('b', '', r.title || 'Working'), el('small', '', [r.detail, r.started_at ? ago(r.started_at) : ''].filter(Boolean).join(' · ')));
            var stop = el('button', 'lk-btn is-stop', 'Stop'); stop.type = 'button'; stop.setAttribute('aria-label', 'Stop ' + (r.title || 'this'));
            stop.addEventListener('click', function () { stop.disabled = true; send('running/' + encodeURIComponent(r.id) + '/stop').catch(function () { stop.disabled = false; }); });
            li.append(k, t, stop); return li;
        }));
        $('[data-lk-running-none]').hidden = running.length > 0;

        var caps = Array.isArray(s.capabilities) ? s.capabilities : [];
        $('[data-lk-caps]').replaceChildren.apply($('[data-lk-caps]'), caps.map(function (c) {
            var li = el('li', 'lk-cap'); var t = el('span'); t.append(el('b', '', c.name || c.id), el('small', '', c.who || ''));
            var sw = el('label', 'lk-switch'); var box = el('input'); box.type = 'checkbox'; box.checked = !!c.on; box.setAttribute('aria-label', c.name || c.id);
            box.addEventListener('change', function () { send('capabilities/' + encodeURIComponent(c.id), 'POST', { on: box.checked }).catch(function () { box.checked = !box.checked; }); });
            sw.append(box, el('i')); li.append(t, sw); return li;
        }));
        root.querySelector('[data-lk-sec="caps"]').hidden = caps.length === 0;

        var shares = Array.isArray(s.shares) ? s.shares : [];
        $('[data-lk-shares]').replaceChildren.apply($('[data-lk-shares]'), shares.map(function (sh) {
            var li = el('li'); var t = el('span'); t.append(el('b', '', 'Port ' + sh.port), el('small', '', sh.url || ''));
            var copy = el('button', 'lk-btn', 'Copy'); copy.type = 'button';
            copy.addEventListener('click', function () { if (navigator.clipboard) navigator.clipboard.writeText(sh.url || '').then(function () { copy.textContent = 'Copied'; setTimeout(function () { copy.textContent = 'Copy'; }, 1500); }, function () {}); });
            var end = el('button', 'lk-btn is-stop', 'End'); end.type = 'button';
            end.addEventListener('click', function () { send('shares/' + encodeURIComponent(sh.port), 'DELETE'); });
            li.append(t, copy, end); return li;
        }));

        var gpu = s.gpu;
        root.querySelector('[data-lk-sec="gpu"]').hidden = !gpu;
        if (gpu) {
            $('[data-lk-gpu-name]').textContent = [gpu.name, gpu.vram_gb ? gpu.vram_gb + ' GB' : ''].filter(Boolean).join(' · ');
            $('[data-lk-models]').replaceChildren.apply($('[data-lk-models]'), (gpu.models || []).map(function (mo) {
                var li = el('li'); var t = el('span'); t.append(el('b', '', mo.name), el('small', '', mo.loaded ? 'Loaded' : 'Installed'));
                li.append(t, el('span', 'lk-tps', mo.tps ? mo.tps + ' tok/s' : '')); return li;
            }));
        }

        var devices = Array.isArray(s.devices) ? s.devices : [];
        $('[data-lk-devices]').replaceChildren.apply($('[data-lk-devices]'), devices.map(function (d) {
            var li = el('li', (d.online ? 'is-on' : '') + (d.here ? ' is-here' : ''));
            li.append(el('b', '', d.name || 'A device'), el('small', '', (d.here ? 'This one · ' : '') + (d.online ? 'Online' : 'Offline') + (d.kind ? ' · ' + d.kind : '')));
            return li;
        }));
        root.querySelector('[data-lk-sec="devices"]').hidden = devices.length === 0;

        var act = Array.isArray(s.activity) ? s.activity.slice(0, 30) : [];
        $('[data-lk-activity]').replaceChildren.apply($('[data-lk-activity]'), act.map(function (a) {
            var li = el('li'); var t = el('time', '', hhmm(a.at)); t.setAttribute('datetime', a.at || ''); li.append(t, el('span', '', a.text || '')); return li;
        }));
        $('[data-lk-activity-none]').hidden = act.length > 0;

        var earn = $('[data-lk-earn]');
        earn.hidden = !(s.earnings_today && s.earnings_today.coins);
        if (!earn.hidden) earn.textContent = 'Earned today: ' + Number(s.earnings_today.coins).toLocaleString() + ' OpenCoins';
    }
    function down() {
        root.setAttribute('aria-busy', 'false');
        root.setAttribute('data-state', 'down');
        $('[data-lk-status]').textContent = 'Not connected';
        $('[data-lk-down]').hidden = false;
    }
    var timer = 0;
    function load() {
        return fetch('/panel/api/state', { credentials: 'same-origin', headers: { 'X-Link-Panel': token } })
            .then(function (r) { if (!r.ok) throw new Error(String(r.status)); return r.json(); }).then(draw).catch(down);
    }
    $('[data-lk-pause]').addEventListener('click', function () { var on = this.getAttribute('aria-pressed') !== 'true'; send('pause', 'POST', { paused: on }); });
    $('[data-lk-share-form]').addEventListener('submit', function (e) {
        e.preventDefault(); var port = Number(this.port.value);
        if (!(port >= 1 && port <= 65535)) return;
        var f = this; send('shares', 'POST', { port: port }).then(function () { f.reset(); });
    });
    var drop = $('[data-lk-drop]'), say = $('[data-lk-drop-say]');
    function sendFiles(files) {
        if (!files || !files.length) return;
        var fd = new FormData(); for (var i = 0; i < files.length; i++) fd.append('file', files[i]);
        say.textContent = 'Sending ' + files.length + ' file' + (files.length === 1 ? '' : 's') + '…';
        fetch('/panel/api/files', { method: 'POST', body: fd, headers: { 'X-Link-Panel': token }, credentials: 'same-origin' })
            .then(function (r) { say.textContent = r.ok ? 'Sent to your other devices.' : 'Link could not send them (' + r.status + ').'; }, function () { say.textContent = 'Link is not answering.'; });
    }
    ['dragenter', 'dragover'].forEach(function (t) { drop.addEventListener(t, function (e) { e.preventDefault(); drop.classList.add('is-over'); }); });
    ['dragleave', 'drop'].forEach(function (t) { drop.addEventListener(t, function () { drop.classList.remove('is-over'); }); });
    drop.addEventListener('drop', function (e) { e.preventDefault(); sendFiles(e.dataTransfer.files); });
    $('[data-lk-files]').addEventListener('change', function () { sendFiles(this.files); this.value = ''; });
    function tick() { if (document.visibilityState === 'visible') load(); }
    load();
    timer = setInterval(tick, 3000);
    document.addEventListener('visibilitychange', tick);
})();
