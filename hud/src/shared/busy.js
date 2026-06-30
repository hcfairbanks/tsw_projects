// Full-screen "busy" overlay: a spinning ring with a live elapsed-time
// counter in its center, plus an optional label underneath. Used to cover
// long-running operations (route import / delete) so the user sees progress
// is happening and how long it's taken.
//
//   const busy = showBusy('Importing route…');
//   try { await work(); } finally { busy.close(); }
//
// The ring spins; the timer text stays upright (it's a sibling overlaid on
// the ring, not a child of the rotating element).

export function showBusy(label) {
  const ov = document.createElement('div');
  ov.className = 'busy-overlay';
  ov.innerHTML = `
    <div class="busy-box">
      <div class="busy-spinner">
        <div class="busy-ring"></div>
        <span class="busy-time">0.0s</span>
      </div>
      <div class="busy-label"></div>
    </div>`;
  ov.querySelector('.busy-label').textContent = label || '';
  document.body.appendChild(ov);

  const start = (window.performance && performance.now) ? performance.now() : Date.now();
  const timeEl = ov.querySelector('.busy-time');
  const fmt = (s) => s < 60
    ? s.toFixed(1) + 's'
    : Math.floor(s / 60) + ':' + String(Math.floor(s % 60)).padStart(2, '0');
  const tick = () => {
    const now = (window.performance && performance.now) ? performance.now() : Date.now();
    timeEl.textContent = fmt((now - start) / 1000);
  };
  tick();
  const timer = setInterval(tick, 100);

  let closed = false;
  return {
    close() {
      if (closed) return;
      closed = true;
      clearInterval(timer);
      ov.remove();
    },
  };
}
