const { DatabaseSync } = require('node:sqlite');
const path = require('path');

const DB_PATH = path.join(__dirname, 'database.db');
const db = new DatabaseSync(DB_PATH);
db.exec('PRAGMA journal_mode = WAL');

// Ensure price columns exist (migration for existing databases)
const migrateCols = [
  ['price', 'TEXT'], ['price_value', 'REAL'],
  ['price_original', 'TEXT'], ['price_original_value', 'REAL'],
  ['price_currency', 'TEXT'], ['price_discount', 'TEXT'], ['price_updated_at', 'TEXT'],
];
for (const [col, type] of migrateCols) {
  try { db.exec(`ALTER TABLE dlc ADD COLUMN ${col} ${type}`); } catch (e) {}
}

const DELAY_MS = 1500; // delay between requests to avoid rate limiting

function sleep(ms) {
  return new Promise(resolve => setTimeout(resolve, ms));
}

async function fetchPrice(steamUrl, cc = 'us') {
  try {
    // Append ?cc= to force Steam to return prices in the desired currency
    const sep = steamUrl.includes('?') ? '&' : '?';
    const url = `${steamUrl}${sep}cc=${cc}`;
    const resp = await fetch(url, {
      headers: {
        'User-Agent': 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36',
        'Accept-Language': 'en-US,en;q=0.9',
        'Cookie': `wants_mature_content=1; birthtime=568022401; lastagecheckage=1-0-1988; steamCountry=${cc}`,
      },
      redirect: 'follow',
    });

    if (!resp.ok) {
      return { error: `HTTP ${resp.status}` };
    }

    const html = await resp.text();

    // Extract prices from the HTML
    // IMPORTANT: Steam pages have multiple purchase blocks — the individual DLC,
    // bundles, and recommended DLC sections. We isolate the first
    // game_area_purchase_game block (the actual DLC) and parse only that.
    let price = null;
    let priceOriginal = null;
    let discount = null;
    let currency = null;

    // Extract the first purchase block (the actual DLC, not bundles/recommendations)
    // Use btn_addtocart as the end boundary since it's always present in purchase blocks
    const purchaseBlock = html.match(/class="game_area_purchase_game\s*"[\s\S]*?btn_addtocart/);
    const block = purchaseBlock ? purchaseBlock[0] : '';

    // 1. Look for a sale/discount price within the purchase block
    const saleMatch = block.match(/game_purchase_discount[^>]*data-price-final="(\d+)"[^>]*data-discount="(\d+)"[\s\S]*?discount_original_price[^>]*>([^<]+)<[\s\S]*?discount_final_price[^>]*>([^<]+)</);
    if (saleMatch) {
      price = saleMatch[4].trim();
      priceOriginal = saleMatch[3].trim();
      discount = saleMatch[2] + '%';
    }

    // 2. Non-discounted price within the purchase block
    if (!price) {
      const priceMatch = block.match(/game_purchase_price[^>]*>([^<]+)</);
      if (priceMatch) {
        price = priceMatch[1].trim();
      }
    }

    // 3. Fallback: look for game_purchase_action with data-price-final across all purchase blocks
    if (!price) {
      const actionMatch = html.match(/class="game_area_purchase_game\s*"[\s\S]*?data-price-final="(\d+)"/);
      if (actionMatch) {
        // Find the formatted price near this block
        const nearBlock = html.match(/class="game_area_purchase_game\s*"[\s\S]*?game_purchase_price[^>]*>([^<]+)</);
        if (nearBlock) {
          price = nearBlock[1].trim();
        }
      }
    }

    // 4. Fallback: meta itemprop price with $ formatting
    if (!price) {
      const metaMatch = html.match(/<meta itemprop="price" content="([^"]+)"/);
      if (metaMatch) {
        price = '$' + metaMatch[1];
      }
    }

    // Extract currency from price string
    if (price) {
      const currMatch = price.match(/^([A-Z]{2,3}\$?|\$|£|€)/);
      if (currMatch) currency = currMatch[1];
    }

    // Handle "Free" or "Free To Play"
    if (!price) {
      if (html.includes('freeText') || html.includes('Free to Play') || html.includes('Free To Play')) {
        price = 'Free';
        currency = '';
      }
    }

    // Extract numeric values
    function toNum(s) {
      if (!s) return null;
      const m = s.match(/[\d,]+\.?\d*/);
      if (!m) return null;
      return parseFloat(m[0].replace(/,/g, ''));
    }

    return { price, priceValue: toNum(price), priceOriginal, priceOriginalValue: toNum(priceOriginal), discount, currency };
  } catch (e) {
    return { error: e.message };
  }
}

async function main() {
  const dlcs = db.prepare(`
    SELECT id, content_name, steam_link
    FROM dlc
    WHERE steam_link IS NOT NULL AND steam_link != ''
    ORDER BY content_name
  `).all();

  console.log(`Found ${dlcs.length} DLCs with Steam links`);

  const updateStmt = db.prepare(`
    UPDATE dlc SET price = ?, price_value = ?, price_original = ?, price_original_value = ?,
      price_currency = ?, price_discount = ?, price_updated_at = ? WHERE id = ?
  `);

  let updated = 0;
  let errors = 0;
  const now = new Date().toISOString();

  for (let i = 0; i < dlcs.length; i++) {
    const dlc = dlcs[i];
    const pct = Math.round(((i + 1) / dlcs.length) * 100);
    process.stdout.write(`\r[${pct}%] ${i + 1}/${dlcs.length} - ${dlc.content_name.substring(0, 50)}...`);

    const result = await fetchPrice(dlc.steam_link);

    if (result.error) {
      console.log(` ERROR: ${result.error}`);
      errors++;
    } else if (result.price) {
      updateStmt.run(
        result.price || null,
        result.priceValue || null,
        result.priceOriginal || null,
        result.priceOriginalValue || null,
        result.currency || null,
        result.discount || null,
        now,
        dlc.id,
      );
      updated++;
    } else {
      console.log(` NO PRICE FOUND`);
      errors++;
    }

    if (i < dlcs.length - 1) {
      await sleep(DELAY_MS);
    }
  }

  console.log(`\n\nDone! Updated ${updated} prices, ${errors} errors.`);

  // Show sample
  const samples = db.prepare(`
    SELECT content_name, price, price_original, price_discount
    FROM dlc WHERE price IS NOT NULL
    ORDER BY content_name LIMIT 10
  `).all();
  console.log('\nSample prices:');
  samples.forEach(s => {
    const sale = s.price_discount ? ` (was ${s.price_original}, -${s.price_discount})` : '';
    console.log(`  ${s.content_name}: ${s.price}${sale}`);
  });

  db.close();
}

// Allow running standalone or being required
if (require.main === module) {
  main().catch(e => { console.error(e); process.exit(1); });
}

module.exports = { fetchPrice, main };
