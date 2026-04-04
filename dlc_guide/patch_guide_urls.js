const XLSX = require('xlsx');
const { DatabaseSync } = require('node:sqlite');
const path = require('path');

const DB_PATH   = path.join(__dirname, 'database.db');
const XLSX_PATH = path.join(__dirname, 'dlc_data.xlsx');

const db = new DatabaseSync(DB_PATH);
db.exec('PRAGMA foreign_keys = ON');

const wb    = XLSX.readFile(XLSX_PATH);
const sheet = wb.Sheets['All Content'];
const range = XLSX.utils.decode_range(sheet['!ref']);

const headers = [];
for (let c = range.s.c; c <= range.e.c; c++) {
  const cell = sheet[XLSX.utils.encode_cell({ r: 0, c })];
  headers.push(cell ? String(cell.v) : '');
}

const guideColIdx = headers.indexOf('Collectable Guides 📖');
const nameColIdx  = headers.indexOf('Content Name');

if (guideColIdx === -1) { console.error('Guide column not found'); process.exit(1); }

// Collect rows: { contentName, url }
const rows = [];
for (let r = 1; r <= range.e.r; r++) {
  const guideCell = sheet[XLSX.utils.encode_cell({ r, c: guideColIdx })];
  const nameCell  = sheet[XLSX.utils.encode_cell({ r, c: nameColIdx })];
  if (guideCell && guideCell.l && guideCell.l.Target && nameCell && nameCell.v) {
    rows.push({ contentName: String(nameCell.v).trim(), url: guideCell.l.Target });
  }
}

console.log('Rows with guide URLs in xlsx:', rows.length);

if (!rows.length) { console.log('Nothing to update'); db.close(); process.exit(0); }

const findDlc = db.prepare('SELECT id FROM dlc WHERE content_name = ?');
const updateUrl = db.prepare("UPDATE document_link SET url = ? WHERE dlc_id = ? AND doc_type = 'guide' AND (url IS NULL OR url = '')");

let updated = 0;
for (const row of rows) {
  const dlc = findDlc.get(row.contentName);
  if (!dlc) {
    console.log('DLC not found:', row.contentName);
    continue;
  }
  const result = updateUrl.run(row.url, dlc.id);
  updated += result.changes;
}

console.log('Updated', updated, 'guide URLs');
db.close();
