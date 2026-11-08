'use strict';

// Tests for openvibe-skill-runner@1.0.0: the declared function that runs one skill tool in the Node's job sandbox.
//
// Each test runs the runner as the job worker would: the args are one JSON line on stdin, the result is read from fd 3,
// the entry is a file in the job directory, and the runner is the parent of the tool. The runner is exercised directly,
// so the tests also prove the flags it gives the tool; the job sandbox's network and kernel limits are the worker's
// (internal/worker's Linux tests cover the sandbox itself).

const { test } = require('node:test');
const assert = require('node:assert/strict');
const { spawnSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const RUNNER = path.join(__dirname, 'skill_runner.js');
const SECRET = 'ov-skill-runner-test-secret';

/**
 * Runs the runner as a job: cwd dir (the job directory), input the args JSON, fd 3 a file. `setup` may add other files
 * (a symlink, say) after the named ones; `outside` is a scratch directory outside the job directory.
 */
function runJob(t, { files = {}, args, env = { ...process.env, OV_SKILL_SECRET: SECRET }, command, setup } = {}) {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'ov-skill-job-'));
    const outside = fs.mkdtempSync(path.join(os.tmpdir(), 'ov-skill-outside-'));
    t.after(() => {
        fs.rmSync(dir, { recursive: true, force: true });
        fs.rmSync(outside, { recursive: true, force: true });
    });
    for (const [name, body] of Object.entries(files)) fs.writeFileSync(path.join(dir, name), body);
    if (setup) setup(dir, outside);
    const resultPath = path.join(outside, 'result.json');
    const fd = fs.openSync(resultPath, 'w+');
    const cmd = command || [process.execPath, RUNNER];
    const r = spawnSync(cmd[0], cmd.slice(1), {
        cwd: dir, input: JSON.stringify(args), env, timeout: 20000,
        stdio: ['pipe', 'pipe', 'pipe', fd],
    });
    fs.closeSync(fd);
    const raw = fs.readFileSync(resultPath, 'utf8');
    let result = null;
    try { result = JSON.parse(raw); } catch { /* asserted by the caller */ }
    return { dir, outside, status: r.status, signal: r.signal, stdout: r.stdout.toString(), stderr: r.stderr.toString(), result, raw };
}

// A tool that answers one JSON value whatever comes in, and reports what it could and could not do.
const REPORT = `
const fs = require('fs');
const out = {};
const t = (name, fn) => { try { fn(); out[name] = 'ALLOWED'; } catch (e) { out[name] = e.code || e.name; } };
let input = '';
process.stdin.on('data', (d) => input += d).on('end', () => {
    out.input = JSON.parse(input);
    out.env = Object.keys(process.env);
    out.secret = process.env.OV_SKILL_SECRET === undefined ? 'absent' : 'present';
    out.cwd = process.cwd();
    out.argv = process.argv.slice(2);
    t('spawn', () => require('child_process').spawnSync('id'));
    t('worker', () => new (require('worker_threads').Worker)('1', { eval: true }));
    t('write', () => fs.writeFileSync('written.txt', 'x'));
    t('eval', () => eval('1+1'));
    t('function', () => Function('return 1')());
    t('read_entry', () => fs.readFileSync('tool.js', 'utf8'));
    out.read_outside = (() => { try { fs.readFileSync(out.input.outside, 'utf8'); return 'ALLOWED'; } catch (e) { return e.code || e.name; } })();
    out.write_outside = (() => { try { fs.writeFileSync(out.input.outside + '.written', 'x'); return 'ALLOWED'; } catch (e) { return e.code || e.name; } })();
    process.stdout.write(JSON.stringify(out));
});
`;

test('a good tool gets the input on stdin and its one JSON value comes back', (t) => {
    const r = runJob(t, {
        files: { 'tool.js': REPORT },
        args: { tool: 'report', entry: 'tool.js', input: { n: 7, s: 'héllo', nested: [true, null], outside: '/nonexistent' } },
    });
    assert.equal(r.status, 0, r.stderr);
    assert.equal(r.result.ok, true, r.raw);
    assert.deepEqual(r.result.output.input, { n: 7, s: 'héllo', nested: [true, null], outside: '/nonexistent' });
    assert.equal(r.result.exit, 0);
    assert.equal(r.result.stderr, undefined);
    assert.deepEqual(r.result.output.argv, []);
    assert.equal(r.result.output.cwd, fs.realpathSync(r.dir));
});

test('the tool gets no environment at all, not even the runner\'s', (t) => {
    const r = runJob(t, { files: { 'tool.js': REPORT }, args: { tool: 'report', entry: 'tool.js', input: { outside: '/nonexistent' } } });
    assert.equal(r.result.ok, true, r.raw);
    assert.deepEqual(r.result.output.env, []);
    assert.equal(r.result.output.secret, 'absent');
});

test('the tool cannot spawn a process, start a worker, write a file, read outside its input dir, or use eval', (t) => {
    const out = path.join(fs.mkdtempSync(path.join(os.tmpdir(), 'ov-skill-')),
        'outside.txt');
    fs.writeFileSync(out, 'not yours');
    t.after(() => fs.rmSync(path.dirname(out), { recursive: true, force: true }));
    const r = runJob(t, { files: { 'tool.js': REPORT }, args: { tool: 'report', entry: 'tool.js', input: { outside: out } } });
    assert.equal(r.result.ok, true, r.raw);
    const o = r.result.output;
    assert.equal(o.spawn, 'ERR_ACCESS_DENIED');
    assert.equal(o.worker, 'ERR_ACCESS_DENIED');
    assert.equal(o.write, 'ERR_ACCESS_DENIED');
    assert.equal(o.read_outside, 'ERR_ACCESS_DENIED');
    assert.equal(o.write_outside, 'ERR_ACCESS_DENIED');
    assert.equal(o.eval, 'EvalError');
    assert.equal(o.function, 'EvalError');
    assert.equal(o.read_entry, 'ALLOWED'); // its own entry is readable: the job directory is the one allowed tree
    assert.equal(fs.existsSync(path.join(r.dir, 'written.txt')), false);
    assert.equal(fs.existsSync(out + '.written'), false);
});

test('more than 64 KiB on stdout is stopped and the result says limit', (t) => {
    const r = runJob(t, {
        files: { 'big.js': `process.stdout.write('"' + 'x'.repeat(70000) + '"');` },
        args: { tool: 'big', entry: 'big.js', input: {} },
    });
    assert.equal(r.status, 0, r.stderr);
    assert.equal(r.result.ok, false);
    assert.match(r.result.error, /^limit:/);
    assert.equal(r.result.exit, null);
});

test('stderr is kept, cut at 16 KiB', (t) => {
    const r = runJob(t, {
        files: { 'noisy.js': `process.stderr.write('e'.repeat(20000)); process.stdout.write('{"ok":1}');` },
        args: { tool: 'noisy', entry: 'noisy.js', input: {} },
    });
    assert.equal(r.result.ok, true, r.raw);
    assert.equal(r.result.output.ok, 1);
    assert.equal(r.result.stderr.length, 16384);
    assert.equal(r.result.stderr_truncated, true);
});

test('a tool that exits non-zero hands back its code and stderr', (t) => {
    const r = runJob(t, {
        files: { 'fail.js': `process.stderr.write('it broke'); process.exit(7);` },
        args: { tool: 'fail', entry: 'fail.js', input: {} },
    });
    assert.equal(r.status, 0, r.stderr);
    assert.equal(r.result.ok, false);
    assert.equal(r.result.exit, 7);
    assert.match(r.result.error, /exited with code 7/);
    assert.equal(r.result.stderr, 'it broke');
});

test('stdout that is not one JSON value is a failed tool result, not a crash', (t) => {
    for (const [name, body] of [['two.js', 'process.stdout.write("{}{}")'], ['none.js', '']]) {
        const r = runJob(t, { files: { [name]: body }, args: { tool: 'x', entry: name, input: {} } });
        assert.equal(r.status, 0, r.stderr);
        assert.equal(r.result.ok, false);
        assert.match(r.result.error, /not one JSON value|wrote no output/);
    }
});

test('entry must be a regular file in the job directory, never a path or a symlink', (t) => {
    let r = runJob(t, {
        files: { 'tool.js': 'process.stdout.write("{}")' },
        args: { tool: 'x', entry: '../tool.js', input: {} },
    });
    assert.equal(r.result.ok, false);
    assert.match(r.result.error, /entry must be the name of one of the job inputs/);

    r = runJob(t, {
        args: { tool: 'x', entry: 'missing.js', input: {} },
    });
    assert.equal(r.result.ok, false);
    assert.match(r.result.error, /not one of the job inputs/);

    r = runJob(t, {
        args: { tool: 'x', entry: 'link.js', input: {} },
        setup: (dir, out) => {
            fs.writeFileSync(path.join(out, 'tool.js'), 'process.stdout.write("{}")');
            fs.symlinkSync(path.join(out, 'tool.js'), path.join(dir, 'link.js'));
        },
    });
    assert.equal(r.result.ok, false);
    assert.match(r.result.error, /not a regular file input/);
});

test('a bad tool name or missing input fails before anything runs', (t) => {
    for (const args of [
        { tool: 'Bad Name', entry: 'tool.js', input: {} },
        { tool: 'x', entry: 'tool.js' },
        { entry: 'tool.js', input: {} },
    ]) {
        const r = runJob(t, { files: { 'tool.js': 'process.stdout.write("{}")' }, args });
        assert.equal(r.status, 0, r.stderr);
        assert.equal(r.result.ok, false);
        assert.equal(r.result.exit, null);
    }
});

test('under net deny (its own network namespace) the tool opens no socket to the internet', (t) => {
    const probe = spawnSync('unshare', ['-rn', 'true'], { timeout: 5000 });
    if (probe.error || probe.status !== 0) {
        return t.skip('unshare -rn is not available here, so a network namespace cannot stand');
    }
    const tool = `
const net = require('net');
const s = net.connect({ host: '1.1.1.1', port: 80 });
const done = (o) => { process.stdout.write(JSON.stringify(o)); process.exit(0); };
s.on('connect', () => done({ reached: true }));
s.on('error', (e) => done({ reached: false, code: e.code }));
setTimeout(() => done({ reached: true, timeout: true }), 8000);
`;
    const r = runJob(t, {
        files: { 'net.js': tool },
        args: { tool: 'net', entry: 'net.js', input: {} },
        command: ['unshare', '-rn', process.execPath, RUNNER],
    });
    assert.equal(r.result.ok, true, r.raw);
    assert.equal(r.result.output.reached, false, r.raw);
    assert.ok(['ENETUNREACH', 'EHOSTUNREACH', 'ECONNREFUSED'].includes(r.result.output.code), r.raw);
});
