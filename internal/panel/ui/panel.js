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

    // Codes on this computer: each agent CLI's state in words, and the tasks with Open and (while working) Stop.
    var AGENT_STATE = { ready: 'ready', signin: 'sign in', missing: 'not installed' };
    var TASK_STATE = { running: 'Working', waiting: 'Waiting for you', done: 'Done', failed: 'Didn\u2019t finish', stopped: 'Stopped' };
    function drawCodes(c, machine) {
        var sec = root.querySelector('[data-lk-sec="codes"]');
        sec.hidden = !c;
        if (!c) return;
        var agents = c.agents || [], tasks = c.tasks || [];
        $('[data-lk-agents]').replaceChildren.apply($('[data-lk-agents]'), agents.map(function (a) {
            var li = el('li', 'lk-agent'); li.setAttribute('data-state', a.state || 'missing');
            li.append(document.createTextNode(a.name || a.id), el('small', '', AGENT_STATE[a.state] || a.state || ''));
            if (a.state === 'signin' && a.hint) {
                var code = el('code', '', a.hint); var copy = el('button', '', 'Copy'); copy.type = 'button';
                copy.setAttribute('aria-label', 'Copy ' + a.hint);
                copy.addEventListener('click', function () { if (navigator.clipboard) navigator.clipboard.writeText(a.hint).then(function () { copy.textContent = 'Copied'; }); });
                li.append(code, copy);
            }
            return li;
        }));
        $('[data-lk-agents]').hidden = agents.length === 0;
        var open = tasks.filter(function (t) { return t.state === 'running' || t.state === 'waiting'; }).length;
        $('[data-lk-codes-n]').textContent = open ? '(' + open + ' working)' : '';
        $('[data-lk-tasks]').replaceChildren.apply($('[data-lk-tasks]'), tasks.map(function (t) {
            var li = el('li'); var main = el('span');
            var meta = el('small');
            var st = el('span', 'lk-state', TASK_STATE[t.state] || t.state || ''); st.setAttribute('data-state', t.state || '');
            meta.append(st);
            if (t.agent) meta.append(el('span', '', t.agent));
            if (t.started_at) meta.append(el('span', '', ago(t.started_at)));
            main.append(el('b', '', t.title || 'A coding task'));
            if (t.folder) main.append(el('code', '', t.folder));
            main.append(meta);
            var acts = el('span', 'lk-task-acts');
            if (t.url && /^https:\/\/openvibe\.codes\//.test(t.url)) {
                var a = el('a', 'lk-btn', t.state === 'waiting' ? 'Answer' : 'Open'); a.href = t.url; a.target = '_blank'; a.rel = 'noopener';
                if (t.state === 'waiting') a.className = 'lk-btn is-primary';
                acts.append(a);
            }
            if (t.state === 'running') {
                var stop = el('button', 'lk-btn is-stop', 'Stop'); stop.type = 'button'; stop.setAttribute('aria-label', 'Stop ' + (t.title || 'this task'));
                stop.addEventListener('click', function () { stop.disabled = true; send('codes/tasks/' + encodeURIComponent(t.id) + '/stop').catch(function () { stop.disabled = false; }); });
                acts.append(stop);
            }
            li.append(main, acts); return li;
        }));
        $('[data-lk-tasks-none]').hidden = tasks.length > 0;
        var nw = $('[data-lk-codes-new]');
        if (machine && machine.id) nw.href = 'https://openvibe.codes/?machine=' + encodeURIComponent(machine.id);
    }
    function draw(s) {
        var m = s.machine || {};
        var running = Array.isArray(s.running) ? s.running : [];
        root.setAttribute('aria-busy', 'false');
        root.setAttribute('data-state', m.paused ? 'paused' : running.length ? 'running' : 'online');
        root.setAttribute('data-needs', m.needs_you ? '1' : '0');
        root.style.setProperty('--fill', String(Math.max(10, 100 - Math.min(running.length, 4) * 22)));
        $('[data-lk-down]').hidden = true;
        $('[data-lk-first]').hidden = !s.first_run;
        $('[data-lk-name]').textContent = m.name || 'This computer';
        drawCodes(s.codes, m);
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
            box.addEventListener('change', function () {
                // Terminal and Desktop say what turning them on means, and wait for a yes.
                if (box.checked && c.confirm && !window.confirm(c.confirm)) { box.checked = false; return; }
                send('capabilities/' + encodeURIComponent(c.id), 'POST', { on: box.checked }).catch(function () { box.checked = !box.checked; });
            });
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
    // The first run: the uses ticked, or Not now; the daemon then turns on what each says and stops asking.
    $('[data-lk-first-form]').addEventListener('submit', function (e) {
        e.preventDefault();
        var uses = Array.prototype.map.call(this.querySelectorAll('input[name=use]:checked'), function (i) { return i.value; });
        send('first-run', 'POST', { uses: uses });
    });
    $('[data-lk-first-skip]').addEventListener('click', function () { send('first-run', 'POST', { uses: [], skip: true }); });
    $('[data-lk-pause]').addEventListener('click', function () { var on = this.getAttribute('aria-pressed') !== 'true'; send('pause', 'POST', { paused: on }); });
    $('[data-lk-share-form]').addEventListener('submit', function (e) {
        e.preventDefault(); var port = Number(this.port.value);
        if (!(port >= 1 && port <= 65535)) return;
        var f = this; send('shares', 'POST', { port: port }).then(function () { f.reset(); });
    });
    var drop = $('[data-lk-drop]'), say = $('[data-lk-drop-say]'), targetSelect = $('[data-lk-send-target]');
    var sendKey = null, lockerModule = null;
    function panelApi(path, method, body) {
        return fetch('/panel/api/' + path, { method: method || 'GET', headers: Object.assign({ 'X-Link-Panel': token }, body ? { 'Content-Type': 'application/json' } : {}),
            body: body ? JSON.stringify(body) : undefined, credentials: 'same-origin' }).then(function (r) {
            if (!r.ok) return r.json().catch(function () { return {}; }).then(function (x) { throw new Error(x.error || 'Link said ' + r.status); });
            return r.status === 204 ? null : r.json();
        });
    }
    function base64(bytes) { var s = ''; for (var i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode.apply(null, bytes.subarray(i, i + 0x8000)); return btoa(s); }
    function unbase64(s) { var raw = atob(s), out = new Uint8Array(raw.length); for (var i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i); return out; }
    function packed(name, mime, data) {
        var meta = new TextEncoder().encode(JSON.stringify({ name: name, mime: mime || 'application/octet-stream' }));
        var out = new Uint8Array(4 + meta.length + data.length); new DataView(out.buffer).setUint32(0, meta.length); out.set(meta, 4); out.set(data, 4 + meta.length); return out;
    }
    function moduleLocker() { return lockerModule || (lockerModule = import('https://openvibe.link/locker.mjs').then(function (m) { return m.locker; })); }
    function loadTargets() {
        panelApi('send/targets').then(function (body) {
            var old = targetSelect.value; targetSelect.replaceChildren(new Option('Choose one of your machines', ''));
            (body.machines || []).forEach(function (m) { var opt = new Option(m.name + (m.online ? '' : ' (offline)'), m.id); opt.dataset.publicKey = JSON.stringify(m.public_key); targetSelect.add(opt); });
            if (old) targetSelect.value = old;
            if (targetSelect.options.length === 1) say('Open Link on another paired machine to turn on receiving.');
        }).catch(function (e) { if (targetSelect.options.length === 1) { targetSelect.replaceChildren(new Option('No receiving machine is ready', '')); say(e.message); } });
    }
    function sendPayload(name, mime, data) {
        var opt = targetSelect.options[targetSelect.selectedIndex];
        if (!opt || !opt.value) { say('Choose one of your machines first.'); return Promise.resolve(); }
        if (data.length > 20 * 1024 * 1024) { say('Choose a file smaller than 20 MB for now.'); return Promise.resolve(); }
        say('Encrypting on this computer…');
        return moduleLocker().then(function (locker) {
            var publicKey = JSON.parse(opt.dataset.publicKey || '{}');
            var content = packed(name, mime, data);
            return locker.seal(content, { lock: 'people', keys: [{ subject: { type: 'node', id: opt.value }, publicKey: publicKey }] }).then(function (sealed) {
                say('Sending encrypted data…');
                return panelApi('files', 'POST', { target_node: opt.value, envelope: base64(sealed.bytes) });
            });
        }).then(function () { say('Sent. The receiving machine will ask before saving it.'); loadTargets(); })
            .catch(function (e) { say(e.message || 'Link could not send this item.'); });
    }
    function sendFile(file) { return file.arrayBuffer().then(function (b) { return sendPayload(file.name, file.type, new Uint8Array(b)); }); }
    function sendNote() { var text = $('[data-lk-send-note]').value; if (!text.trim()) { say('Write a note first.'); return; } sendPayload('note.txt', 'text/plain;charset=utf-8', new TextEncoder().encode(text)).then(function () { $('[data-lk-send-note]').value = ''; }); }
    function downloadAccepted(item) {
        return Promise.all([moduleLocker(), panelApi('send/key')]).then(function (parts) {
            var locker = parts[0], envelope = unbase64(item.envelope), own = parts[1];
            return locker.open(envelope, { privateKey: own.private_key.privateKey || own.private_key, subject: own.subject });
        }).then(function (bytes) {
            var view = bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes), n = new DataView(view.buffer, view.byteOffset, view.byteLength).getUint32(0);
            if (n < 2 || n > view.length - 4) throw new Error('The encrypted item is incomplete.');
            var info = JSON.parse(new TextDecoder().decode(view.subarray(4, 4 + n))), fileBytes = view.subarray(4 + n);
            var safeName = String(info.name || 'received-file').replace(/[\\/:*?"<>|\u0000-\u001f]/g, '_').slice(0, 180) || 'received-file';
            var url = URL.createObjectURL(new Blob([fileBytes], { type: info.mime || 'application/octet-stream' }));
            var a = document.createElement('a'); a.href = url; a.download = safeName; a.click(); setTimeout(function () { URL.revokeObjectURL(url); }, 30000);
            return panelApi('files/' + encodeURIComponent(item.id) + '/complete', 'POST', {}).then(function () { say('Accepted. Your browser saved ' + safeName + ' to Downloads.'); });
        });
    }
    function loadInbox() {
        panelApi('send/inbox').then(function (body) {
            var list = $('[data-lk-send-inbox]');
            list.replaceChildren.apply(list, (body.items || []).map(function (item) {
                var li = el('li', 'lk-send-item'), label = el('span', '', 'Encrypted item · ' + ago(item.created_at));
                var actions = el('div'), accept = el('button', '', 'Accept'), decline = el('button', '', 'Decline');
                accept.type = decline.type = 'button';
                accept.addEventListener('click', function () { accept.disabled = true; say('Opening the encrypted item…'); downloadAccepted(item).catch(function (e) { accept.disabled = false; say(e.message || 'The item could not be opened. It is still waiting.'); }); });
                decline.addEventListener('click', function () { decline.disabled = true; panelApi('files/' + encodeURIComponent(item.id) + '/decline', 'POST', {}).then(loadInbox).catch(function (e) { decline.disabled = false; say(e.message); }); });
                actions.append(accept, decline); li.append(label, actions); return li;
            }));
        }).catch(function () {});
    }
    var dropTarget = $('[data-lk-drop]');
    ['dragenter', 'dragover'].forEach(function (t) { drop.addEventListener(t, function (e) { e.preventDefault(); drop.classList.add('is-over'); }); });
    ['dragleave', 'drop'].forEach(function (t) { drop.addEventListener(t, function () { drop.classList.remove('is-over'); }); });
    dropTarget.addEventListener('drop', function (e) { e.preventDefault(); if (e.dataTransfer.files[0]) sendFile(e.dataTransfer.files[0]); });
    $('[data-lk-files]').addEventListener('change', function () { if (this.files[0]) sendFile(this.files[0]); this.value = ''; });
    $('[data-lk-send-note-button]').addEventListener('click', sendNote);
    loadTargets(); loadInbox(); setInterval(function () { if (document.visibilityState === 'visible') { loadTargets(); loadInbox(); } }, 3000);
    function tick() { if (document.visibilityState === 'visible') load(); }
    load();
    timer = setInterval(tick, 3000);
    document.addEventListener('visibilitychange', tick);
})();
