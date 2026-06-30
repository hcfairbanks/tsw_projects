// Drag-to-resize table columns. Call makeColumnsResizable(table, key) after each
// header (re)build — the header is rebuilt on every sort/search, so widths are
// persisted (keyed by `key` + column index) and re-applied. Widths are saved to
// localStorage, so once the user resizes a column it becomes the default and
// survives app restarts.
//
// CSS contract: a `.col-resizer` grip is appended to each <th>; style it in
// shell.css. The host <th> must be a positioning context — `position: sticky`
// (which the shell tables already use for the header) qualifies, so we don't
// touch th positioning here.

const STORE = {}; // key -> { [colIndex]: widthPx }

function loadWidths(key) {
  try { return JSON.parse(localStorage.getItem('colWidths:' + key) || '{}') || {}; }
  catch (e) { return {}; }
}
function saveWidths(key) {
  try { localStorage.setItem('colWidths:' + key, JSON.stringify(STORE[key] || {})); }
  catch (e) { /* storage unavailable — widths still persist in-memory this session */ }
}

export function makeColumnsResizable(table, key = 'tbl') {
  if (!table) return;
  const headRow = table.tHead && table.tHead.rows[0];
  if (!headRow) return;
  // Load persisted widths once per key, then keep using the in-memory copy.
  const store = STORE[key] || (STORE[key] = loadWidths(key));

  Array.from(headRow.cells).forEach((th, idx) => {
    if (th.querySelector('.col-resizer')) return; // already wired this th
    // Re-apply a persisted width from an earlier drag.
    if (store[idx] != null) {
      th.style.width = store[idx] + 'px';
      th.style.minWidth = store[idx] + 'px';
    }
    const grip = document.createElement('span');
    grip.className = 'col-resizer';
    th.appendChild(grip);

    let startX = 0, startW = 0, moved = false;
    const onMove = (e) => {
      moved = true;
      const w = Math.max(40, startW + (e.clientX - startX));
      th.style.width = w + 'px';
      th.style.minWidth = w + 'px';
      store[idx] = w;
    };
    const onUp = () => {
      document.removeEventListener('mousemove', onMove);
      document.removeEventListener('mouseup', onUp);
      document.body.style.userSelect = '';
      document.body.style.cursor = '';
      if (moved) {
        saveWidths(key); // persist the new width as the default
        // The browser still fires a `click` on the <th> right after the drag,
        // which would trigger the column sort. Swallow that one click (capture
        // phase, before the th's own sort handler sees it).
        const swallow = (ev) => {
          ev.stopPropagation();
          ev.preventDefault();
          th.removeEventListener('click', swallow, true);
        };
        th.addEventListener('click', swallow, true);
        // Safety net: if no click follows (drag ended off the th), clean up.
        setTimeout(() => th.removeEventListener('click', swallow, true), 50);
      }
    };
    grip.addEventListener('mousedown', (e) => {
      e.preventDefault();
      e.stopPropagation(); // don't start a sort on mousedown
      startX = e.clientX;
      startW = th.offsetWidth;
      moved = false;
      document.addEventListener('mousemove', onMove);
      document.addEventListener('mouseup', onUp);
      document.body.style.userSelect = 'none';
      document.body.style.cursor = 'col-resize';
    });
    // A plain click on the grip (no drag) must not bubble to the sort handler.
    grip.addEventListener('click', (e) => e.stopPropagation());
  });
}
