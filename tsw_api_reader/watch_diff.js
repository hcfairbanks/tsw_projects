// Fast change detector for the TSW CommAPI.
// Collects readable scalar endpoints under ROOT (skipping the huge physics
// "Simulation" subtree), takes a baseline, then re-polls concurrently for
// WATCH_MS and prints any value that changes — meant to catch a short
// passenger-loading fill while doors are open.
// Usage: node watch_diff.js "CurrentDrivableActor" 60000
const axios = require('axios');
const fs = require('fs');
const path = require('path');

const ROOT = process.argv[2] || 'CurrentDrivableActor';
const WATCH_MS = parseInt(process.argv[3] || '60000', 10);
const CONC = 16;
const BASE = 'http://localhost:31270';
const keyPath = path.join(process.env.USERPROFILE, 'Documents', 'My Games', 'TrainSimWorld6', 'Saved', 'Config', 'CommAPIKey.txt');
const KEY = fs.readFileSync(keyPath, 'utf8').trim();
const HDR = { headers: { DTGCommKey: KEY }, timeout: 5000 };

// Subtrees to skip: pure physics/control internals that change constantly
// and aren't passenger-loading state.
const SKIP = /(^|\/)Simulation($|\/)/i;

function flatten(obj, prefix, out) {
  if (obj === null || obj === undefined) return;
  if (typeof obj !== 'object') { out[prefix] = obj; return; }
  for (const k of Object.keys(obj)) {
    const v = obj[k];
    if (v !== null && typeof v === 'object') flatten(v, prefix ? `${prefix}.${k}` : k, out);
    else out[prefix ? `${prefix}.${k}` : k] = v;
  }
}

async function listNode(p) {
  try {
    const r = await axios.get(`${BASE}/list/${encodeURI(p)}`, HDR);
    return r.data && r.data.Result === 'Success' ? r.data : null;
  } catch { return null; }
}

const endpoints = [];
async function collect(p, depth) {
  if (depth > 6 || SKIP.test(p)) return;
  const d = await listNode(p);
  if (!d) return;
  if (Array.isArray(d.Endpoints)) {
    for (const e of d.Endpoints) {
      // Only stored Property values — Function getters return uninitialized
      // by-reference/converter junk that changes every call (pure noise).
      if (e.Name.startsWith('Property.')) {
        endpoints.push(`${p}.${e.Name}`);
      }
    }
  }
  if (Array.isArray(d.Nodes)) {
    for (const n of d.Nodes) {
      const child = n.NodePath ? n.NodePath.replace(/^Root\//, '') : `${p}/${n.Name}`;
      if (!SKIP.test(child)) await collect(child, depth + 1);
    }
  }
}

async function getOne(ep) {
  try {
    const r = await axios.get(`${BASE}/get/${encodeURI(ep)}`, HDR);
    if (r.data && r.data.Result === 'Success' && r.data.Values) {
      const leaves = {};
      flatten(r.data.Values, '', leaves);
      const out = {};
      for (const k of Object.keys(leaves)) out[`${ep}::${k}`] = leaves[k];
      return out;
    }
  } catch {}
  return {};
}

async function snapshot() {
  const snap = {};
  for (let i = 0; i < endpoints.length; i += CONC) {
    const batch = endpoints.slice(i, i + CONC);
    const results = await Promise.all(batch.map(getOne));
    for (const r of results) Object.assign(snap, r);
  }
  return snap;
}

(async () => {
  console.log(`Collecting endpoints under "${ROOT}" (skipping Simulation)...`);
  await collect(ROOT, 0);
  console.log(`Found ${endpoints.length} readable endpoints. Taking baseline...`);
  const t0 = Date.now();
  const base = await snapshot();
  console.log(`Baseline: ${Object.keys(base).length} scalars in ${((Date.now()-t0)/1000).toFixed(1)}s. Watching ${WATCH_MS/1000}s — KEEP DOORS OPEN.`);
  const seen = new Set();
  const start = Date.now();
  let pass = 0;
  while (Date.now() - start < WATCH_MS) {
    pass++;
    const cur = await snapshot();
    for (const k of Object.keys(cur)) {
      if (k in base && cur[k] !== base[k] && !seen.has(k)) {
        console.log(`CHANGED  ${k}\n   ${base[k]}  ->  ${cur[k]}  (pass ${pass}, +${((Date.now()-start)/1000).toFixed(0)}s)`);
        seen.add(k);
      }
    }
  }
  console.log(`\nDone. ${seen.size} endpoints changed during the window.`);
})();
