'use strict';

/**
 * openvibe-skill-runner@1.0.0: run one tool of a declared skill as an OpenVibe Run job.
 *
 * The Node's job worker runs this file as the job's function (declared in worker.functions by
 * `openvibe-node skills on`). The job's args are one JSON line on stdin:
 *
 *   { tool, entry, input }
 *
 * `tool` is the skill tool's name (for the error it reports), `entry` the name of the job input holding the tool's
 * code, and `input` the JSON the tool gets on its own stdin. Nothing is fetched and no path comes from the server:
 * the entry must be a regular file in the job's working directory, where the worker staged the inputs it checked.
 *
 * The tool runs with this Node's own node binary, cwd the job directory, an empty environment, and:
 *
 *   --permission --allow-fs-read=<the job directory> --disallow-code-generation-from-strings
 *
 * and with none of --allow-child-process, --allow-worker, --allow-addons or --allow-wasi: the process permission
 * model therefore denies file writes, reads outside the job directory, child processes, workers and addons, and
 * V8 refuses eval/new Function. The network and every kernel limit are the job sandbox's (docs/worker.md): this
 * file adds no network of its own.
 *
 * The tool writes exactly one JSON value to stdout, at most 64 KiB; this runner passes it back as `output`. More
 * than 64 KiB stops the tool and the result says `limit`. stderr is kept, at most 16 KiB, as `stderr`. The result is
 * written to fd 3, the worker's result channel:
 *
 *   { ok: true, output, exit: 0 [, stderr, stderr_truncated] }
 *   { ok: false, error, exit: <code or null> [, stderr, stderr_truncated] }
 *
 * A failed tool is still a job result: this process exits 0 whenever it wrote the result (the worker keeps a result
 * only from a function that exited 0), so the person sees what happened instead of a bare job failure.
 */

const fs = require('fs');
const path = require('path');
const { spawn } = require('child_process');

const MAX_STDOUT = 64 * 1024;
const MAX_STDERR = 16 * 1024;
const TOOL_NAME = /^[a-z][a-z0-9_]{0,31}$/;
// A job input's name (platform.job@1's file-name pattern): one path component, no traversal, no absolute path.
const ENTRY_NAME = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;

/** The result on fd 3 (the worker's result channel), or one JSON line on stdout outside a job (tests). */
function writeResult(doc) {
    const text = JSON.stringify(doc);
    try {
        fs.writeSync(3, text);
        return true;
    } catch {
        try {
            fs.writeSync(1, text + '\n');
            return true;
        } catch {
            return false;
        }
    }
}

/** Ends with a result that is not a tool output: nothing ran, or the tool did not produce one JSON value. */
function fail(error, extra) {
    const doc = Object.assign({ ok: false, error: String(error), exit: null }, extra || {});
    process.exitCode = writeResult(doc) ? 0 : 1;
    return doc;
}

/** The job's args as one JSON value: whatever the worker read from its stdin (the contract passes them unchanged). */
function readStdin() {
    return new Promise((resolve) => {
        const chunks = [];
        process.stdin.on('data', (c) => chunks.push(c));
        process.stdin.on('end', () => resolve(Buffer.concat(chunks)));
        process.stdin.on('error', () => resolve(Buffer.concat(chunks)));
    });
}

function parseArgs(raw) {
    let a;
    try {
        a = JSON.parse(String(raw).trim() || '{}');
    } catch {
        throw new Error('the job args are not JSON');
    }
    if (!a || typeof a !== 'object' || Array.isArray(a)) throw new Error('the job args must be an object');
    if (typeof a.tool !== 'string' || !TOOL_NAME.test(a.tool)) throw new Error('tool must be a tool name');
    if (typeof a.entry !== 'string' || !ENTRY_NAME.test(a.entry) || a.entry === '.' || a.entry === '..') {
        throw new Error('entry must be the name of one of the job inputs');
    }
    if (!Object.prototype.hasOwnProperty.call(a, 'input')) throw new Error('input is missing');
    return a;
}

/** The entry file: an input the worker staged as a regular file here, never a symlink or another kind of file. */
function entryFile(dir, entry) {
    const file = path.join(dir, entry);
    let st;
    try {
        st = fs.lstatSync(file);
    } catch {
        throw new Error(`entry ${entry} is not one of the job inputs`);
    }
    if (!st.isFile()) throw new Error(`entry ${entry} is not a regular file input`);
    return file;
}

/** Runs the tool and resolves with its result document (never rejects). */
function runTool(tool, file, input, dir) {
    return new Promise((resolve) => {
        const out = [];
        let outLen = 0;
        const err = [];
        let errLen = 0;
        let errCut = false;
        let overLimit = false;
        let spawnError = null;

        const child = spawn(process.execPath, [
            '--permission',
            `--allow-fs-read=${dir}`,
            '--disallow-code-generation-from-strings',
            file,
        ], { cwd: dir, env: {}, stdio: ['pipe', 'pipe', 'pipe'], windowsHide: true });

        child.on('error', (e) => {
            spawnError = e.message;
        });
        child.stdout.on('data', (chunk) => {
            if (overLimit) return;
            if (outLen + chunk.length > MAX_STDOUT) {
                overLimit = true;
                try { child.kill('SIGKILL'); } catch { /* already gone */ }
                return;
            }
            out.push(chunk);
            outLen += chunk.length;
        });
        child.stderr.on('data', (chunk) => {
            if (errLen >= MAX_STDERR) {
                errCut = true;
                return;
            }
            const room = MAX_STDERR - errLen;
            if (chunk.length > room) {
                err.push(chunk.subarray(0, room));
                errLen = MAX_STDERR;
                errCut = true;
                return;
            }
            err.push(chunk);
            errLen += chunk.length;
        });
        child.stdin.on('error', () => { /* the tool exited without reading its input */ });
        child.stdin.end(JSON.stringify(input));

        const done = (code, signal) => {
            const stderr = Buffer.concat(err).toString('utf8');
            const extra = stderr ? { stderr, stderr_truncated: errCut } : {};
            if (overLimit) return resolve({ ok: false, error: `limit: the tool wrote more than ${MAX_STDOUT} bytes to stdout`, exit: null, ...extra });
            if (spawnError) return resolve({ ok: false, error: `could not start the tool: ${spawnError}`, exit: null, ...extra });
            if (signal) return resolve({ ok: false, error: `the tool was killed by ${signal}`, exit: null, ...extra });
            if (code !== 0) return resolve({ ok: false, error: `the tool exited with code ${code}`, exit: code, ...extra });
            const text = Buffer.concat(out).toString('utf8').trim();
            if (!text) return resolve({ ok: false, error: 'the tool wrote no output', exit: code, ...extra });
            let output;
            try {
                output = JSON.parse(text);
            } catch {
                return resolve({ ok: false, error: 'the tool output is not one JSON value', exit: code, ...extra });
            }
            return resolve({ ok: true, output, exit: code, ...extra });
        };
        child.on('close', done);
    });
}

async function main() {
    let args;
    try {
        args = parseArgs(await readStdin());
    } catch (e) {
        fail(e.message);
        return;
    }
    let dir, file;
    try {
        dir = fs.realpathSync(process.cwd());
        file = entryFile(dir, args.entry);
    } catch (e) {
        fail(e.message);
        return;
    }
    const doc = await runTool(args.tool, file, args.input, dir);
    process.exitCode = writeResult(doc) ? 0 : 1;
}

main().catch((e) => fail(`the skill runner failed: ${e && e.message ? e.message : e}`));
