const XLSX = require('xlsx');
const { DatabaseSync } = require('node:sqlite');
const path = require('path');

const DB_PATH   = path.join(__dirname, 'database.db');
const XLSX_PATH = path.join(__dirname, 'dlc_data.xlsx');

const db = new DatabaseSync(DB_PATH);

const wb    = XLSX.readFile(XLSX_PATH);
const sheet = wb.Sheets['All Content'];
const range = XLSX.utils.decode_range(sheet['!ref']);

const headers = [];
for (let c = range.s.c; c <= range.e.c; c++) {
  const cell = sheet[XLSX.utils.encode_cell({ r: 0, c })];
  headers.push(cell ? String(cell.v) : '');
}

const cols = {
  manual:    headers.indexOf('MANUALS'),
  timetable: headers.indexOf('Wonterail TimeTables PDF Links'),
};
const nameCol = headers.indexOf('Content Name');

// Collect { contentName, docType, url }
const rows = [];
for (let r = 1; r <= range.e.r; r++) {
  const nameCell = sheet[XLSX.utils.encode_cell({ r, c: nameCol })];
  if (!nameCell || !nameCell.v) continue;
  const contentName = String(nameCell.v).trim();

  for (const [docType, colIdx] of Object.entries(cols)) {
    if (colIdx === -1) continue;
    const cell = sheet[XLSX.utils.encode_cell({ r, c: colIdx })];
    if (cell && cell.l && cell.l.Target) {
      rows.push({ contentName, docType, url: cell.l.Target });
    }
  }
}

console.log('Rows with URLs found:', rows.length);

if (!rows.length) { console.log('Nothing to update'); db.close(); process.exit(0); }

const findDlc = db.prepare('SELECT id FROM dlc WHERE content_name = ?');
const updateUrl = db.prepare("UPDATE document_link SET url = ? WHERE dlc_id = ? AND doc_type = ? AND (url IS NULL OR url = '')");

let updated = 0;
for (const row of rows) {
  const dlc = findDlc.get(row.contentName);
  if (!dlc) {
    console.log('DLC not found:', row.contentName);
    continue;
  }
  const result = updateUrl.run(row.url, dlc.id, row.docType);
  updated += result.changes;
}

console.log('Updated', updated, 'document URLs');

const stats = db.prepare("SELECT doc_type, COUNT(*) as total, COUNT(url) as with_url FROM document_link GROUP BY doc_type").all();
stats.forEach(r => console.log(r.doc_type + ':', r.with_url + '/' + r.total));

db.close();
