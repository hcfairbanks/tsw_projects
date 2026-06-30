// SVG icons for the Web HUD card grid. Each icon mirrors the egui
// paint_*_icon helpers in hud-rust/src/main.rs (around line 2029-2166) so
// the cards keep the same visual identity the user is used to.
//
// `<svg viewBox="0 0 60 60">` baseline — cards render the SVG at 64px tall
// inside their fixed-height header slot.

// Mobile/Tablet app-grid: a colourful grid of rounded squares to suggest
// a phone/tablet home screen. cols/rows chosen per device to match the
// egui paint_app_grid call. Stable palette + position seed so the squares
// don't shimmer when the SVG re-renders.
const APP_COLORS = [
  '#ff5a5a', '#ffb432', '#ffe650', '#78dc64',
  '#50b4dc', '#b482e6', '#ff82b4', '#dcdcdc',
];
function appGrid(x, y, w, h, cols, rows) {
  const cellW = w / cols;
  const cellH = h / rows;
  const app = Math.min(cellW, cellH) * 0.72;
  let s = '';
  for (let r = 0; r < rows; r++) {
    for (let c = 0; c < cols; c++) {
      const cx = x + (c + 0.5) * cellW;
      const cy = y + (r + 0.5) * cellH;
      const color = APP_COLORS[(r * 3 + c * 5 + 1) % APP_COLORS.length];
      s += `<rect x="${(cx - app / 2).toFixed(2)}" y="${(cy - app / 2).toFixed(2)}" `
        + `width="${app.toFixed(2)}" height="${app.toFixed(2)}" rx="1.5" ry="1.5" fill="${color}"/>`;
    }
  }
  return s;
}

export const ICONS = {
  // Phone: portrait, blue body, dark screen, 3x4 app grid, home dot.
  // Sized to read at parity with the other (landscape) icons — the
  // narrow portrait silhouette would otherwise look smaller than them.
  mobile() {
    return `<svg viewBox="0 0 60 60" width="44" height="78" xmlns="http://www.w3.org/2000/svg">
      <rect x="18" y="10" width="24" height="40" rx="5" ry="5" fill="#4682c8" stroke="#2850a0" stroke-width="1"/>
      <rect x="21" y="18" width="18" height="23" rx="2" ry="2" fill="#142848"/>
      ${appGrid(21, 18, 18, 23, 3, 4)}
      <circle cx="30" cy="46.5" r="2" fill="#b4c8e6"/>
    </svg>`;
  },

  // Tablet: landscape-leaning, green body, dark screen, 4x5 app grid,
  // top camera dot + bottom home-button outline.
  tablet() {
    return `<svg viewBox="0 0 60 60" width="60" height="58" xmlns="http://www.w3.org/2000/svg">
      <rect x="4" y="5" width="52" height="50" rx="6" ry="6" fill="#5096a0" stroke="#285a3c" stroke-width="1.2"/>
      <rect x="9" y="16" width="42" height="28" rx="3" ry="3" fill="#193223"/>
      ${appGrid(9, 16, 42, 28, 4, 5)}
      <circle cx="30" cy="10.5" r="2.3" fill="#c8e6d2"/>
      <circle cx="30" cy="49" r="4" fill="none" stroke="#c8e6d2" stroke-width="1.5"/>
    </svg>`;
  },

  // Desktop: dark bezel with blue screen, narrow stand bar, base.
  desktop() {
    return `<svg viewBox="0 0 60 60" width="58" height="54" xmlns="http://www.w3.org/2000/svg">
      <rect x="6" y="9" width="48" height="32" rx="2.5" ry="2.5" fill="#373c4b"/>
      <rect x="8" y="11" width="44" height="28" rx="1" ry="1" fill="#006ea0"/>
      <rect x="23.5" y="42" width="13" height="5" rx="0.8" ry="0.8" fill="#6e7382"/>
      <rect x="17" y="46.5" width="26" height="3" rx="1.5" ry="1.5" fill="#6e7382"/>
    </svg>`;
  },

  // Weather: yellow sun with rays + white cloud overlapping. Sun painted
  // AFTER cloud in the egui version so it overlaps; mirror that here.
  weather() {
    return `<svg viewBox="0 0 60 60" width="58" height="54" xmlns="http://www.w3.org/2000/svg">
      <g fill="#f5f8fc">
        <circle cx="26" cy="38" r="6"/>
        <circle cx="33" cy="35" r="7.5"/>
        <circle cx="41" cy="37" r="6"/>
        <rect x="22" y="38" width="24" height="7" rx="3.5" ry="3.5"/>
      </g>
      <g stroke="#ffc332" stroke-width="1.5" stroke-linecap="round">
        <line x1="24" y1="28" x2="24" y2="22"/>
        <line x1="24" y1="28" x2="30" y2="22"/>
        <line x1="24" y1="28" x2="30" y2="28"/>
        <line x1="24" y1="28" x2="30" y2="34"/>
        <line x1="24" y1="28" x2="24" y2="34"/>
        <line x1="24" y1="28" x2="18" y2="34"/>
        <line x1="24" y1="28" x2="18" y2="28"/>
        <line x1="24" y1="28" x2="18" y2="22"/>
      </g>
      <circle cx="24" cy="28" r="7" fill="#ffc332"/>
    </svg>`;
  },

  // Tracking map: green tile, white roads (one horizontal + one vertical
  // offset right), red teardrop pin on the left.
  map() {
    return `<svg viewBox="0 0 60 60" width="58" height="54" xmlns="http://www.w3.org/2000/svg">
      <rect x="6" y="13" width="48" height="34" rx="2" ry="2" fill="#5f965f"/>
      <line x1="9" y1="33" x2="51" y2="33" stroke="#e6e6e6" stroke-width="1.8" stroke-linecap="round"/>
      <line x1="37" y1="14" x2="37" y2="46" stroke="#e6e6e6" stroke-width="1.8" stroke-linecap="round"/>
      <g>
        <circle cx="20" cy="22" r="5" fill="#dc3c3c"/>
        <polygon points="15,25 25,25 20,33" fill="#dc3c3c"/>
        <circle cx="20" cy="22" r="2" fill="#fff"/>
      </g>
    </svg>`;
  },
};
