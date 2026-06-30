//! Cooked Texture2D extractor. Reads a UE 4.27 cooked `*.uasset` /
//! `*.uexp` pair and writes mip-0 as a PNG. Mirrors hud-go's
//! `internal/pak/uasset/texture.go`.
//!
//! Supported pixel formats:
//!   * `PF_B8G8R8A8` — straight BGRA8 → RGBA8 channel swap.
//!   * `PF_DXT1`     — BC1 8 bytes/block, 4×4 pixels.
//!   * `PF_DXT5`     — BC3 16 bytes/block (BC4 alpha + BC1 colour).
//!
//! Why this exists: route + train-class thumbnails ship cooked under
//! their pak. Decoding them gives hud the same imagery as the in-game
//! UI without going through an external converter.

use std::fs;
use std::path::Path;

use image::{ImageBuffer, Rgba};

use crate::uasset::{Reader, Umap};

#[derive(Debug, Clone, serde::Serialize)]
pub struct Texture2DInfo {
    pub size_x:       u32,
    pub size_y:       u32,
    pub pixel_format: String,
    pub num_mips:     u32,
}

/// Decode the top mip of `uasset_path`'s Texture2D and write it as a
/// PNG at `out_path`.
pub fn extract_texture_to_png(uasset_path: &Path, out_path: &Path) -> Result<Texture2DInfo, String> {
    let u = Umap::read(uasset_path)
        .map_err(|e| format!("read uasset: {e}"))?;
    if u.exports.is_empty() {
        return Err("no exports".into());
    }
    // Pick the first non-default export. Texture2D uassets typically have
    // exactly one (the texture instance).
    let exp = u.exports.iter()
        .find(|e| !e.object_name.is_empty() && !e.object_name.starts_with("Default__"))
        .ok_or_else(|| "no non-default export".to_string())?;

    let slice = u.property_slice(exp)
        .ok_or_else(|| format!("export {} has no .uexp data", exp.object_name))?;
    let mut pr = Reader::new(slice, &u.names);

    // Walk the FPropertyTag stream past its "None" terminator.
    while let Some(t) = pr.read_tag() {
        if t.size < 0 || (t.size as usize) > pr.remaining() { break; }
        pr.skip(t.size as usize);
    }

    // 8 bytes of class-serialization overhead (FStripDataFlags Global +
    // Class) before bCooked. Verified empirically on TC's
    // TrainingCentre_Small_1676x468.uasset.
    if pr.remaining() < 8 { return Err("uexp truncated before texture body".into()); }
    pr.skip(8);
    let b_cooked = pr.i32();
    if b_cooked != 1 {
        return Err(format!("bCooked={b_cooked}, expected 1 (post-properties layout drift?)"));
    }
    let pix_fmt = pr.fname();
    if pix_fmt == "None" {
        return Err("no platform data (first PixelFormatName is None)".into());
    }
    if pix_fmt != "PF_DXT1" && pix_fmt != "PF_DXT5" && pix_fmt != "PF_B8G8R8A8" {
        return Err(format!(
            "unsupported pixel format {pix_fmt:?} (only PF_DXT1, PF_DXT5, PF_B8G8R8A8 implemented)"
        ));
    }
    let _skip_offset = pr.i64();   // absolute file offset to skip past this PlatformData — unused
    let size_x = pr.i32();
    let size_y = pr.i32();
    let num_slices = pr.i32();
    if num_slices != 1 {
        return Err(format!("NumSlices={num_slices} (expected 1 for Texture2D)"));
    }
    let pix_fmt_str = pr.fstr();
    if pix_fmt_str != pix_fmt {
        return Err(format!("PixelFormatString={pix_fmt_str:?} != FName {pix_fmt:?}"));
    }
    let _first_mip = pr.i32();
    let num_mips = pr.i32();
    if num_mips < 1 || num_mips > 32 {
        return Err(format!("NumMips={num_mips}, sanity check failed"));
    }

    // UE serialises mips largest-first — mip 0 is what we want.
    let mut top_mip: Vec<u8> = Vec::new();
    let mut top_size = (0i32, 0i32);
    for m in 0..num_mips {
        let mip_cooked = pr.i32();
        if mip_cooked != 1 {
            return Err(format!("mip {m} bCooked={mip_cooked}"));
        }
        // FByteBulkData header (cooked, inline payload).
        let _bulk_flags = pr.i32();
        let elem_count  = pr.i32() as usize;
        let _size_on_disk = pr.i32();
        let _offset_in_file = pr.i64();
        if pr.remaining() < elem_count {
            return Err(format!("mip {m} payload truncated"));
        }
        let payload_start = pr.tell();
        // Copy out the payload bytes (cheap; only top mip kept).
        let payload = pr.data[payload_start..payload_start + elem_count].to_vec();
        pr.skip(elem_count);
        let mip_size_x = pr.i32();
        let mip_size_y = pr.i32();
        let _mip_size_z = pr.i32();
        if m == 0 {
            if mip_size_x != size_x || mip_size_y != size_y {
                return Err(format!(
                    "mip 0 size {mip_size_x}x{mip_size_y} != header {size_x}x{size_y}"
                ));
            }
            top_mip  = payload;
            top_size = (mip_size_x, mip_size_y);
        }
    }
    if top_mip.is_empty() {
        return Err("mip 0 has no pixel data".into());
    }

    let w = top_size.0 as usize;
    let h = top_size.1 as usize;
    let rgba: Vec<u8> = match pix_fmt.as_str() {
        "PF_B8G8R8A8" => {
            let expected = w * h * 4;
            if top_mip.len() != expected {
                return Err(format!(
                    "mip 0 byte count {} != expected BGRA8 size {expected} ({}x{})",
                    top_mip.len(), w, h
                ));
            }
            let mut out = vec![0u8; expected];
            for i in (0..expected).step_by(4) {
                out[i    ] = top_mip[i + 2]; // R ← B
                out[i + 1] = top_mip[i + 1]; // G
                out[i + 2] = top_mip[i    ]; // B ← R
                out[i + 3] = top_mip[i + 3]; // A
            }
            out
        }
        "PF_DXT1" => {
            let w_blocks = (w + 3) / 4;
            let h_blocks = (h + 3) / 4;
            let expected = w_blocks * h_blocks * 8;
            if top_mip.len() != expected {
                return Err(format!(
                    "mip 0 byte count {} != expected DXT1 size {expected} ({}x{} → {}x{} blocks)",
                    top_mip.len(), w, h, w_blocks, h_blocks
                ));
            }
            decode_dxt1(&top_mip, w, h)
        }
        "PF_DXT5" => {
            let w_blocks = (w + 3) / 4;
            let h_blocks = (h + 3) / 4;
            let expected = w_blocks * h_blocks * 16;
            if top_mip.len() != expected {
                return Err(format!(
                    "mip 0 byte count {} != expected DXT5 size {expected} ({}x{} → {}x{} blocks)",
                    top_mip.len(), w, h, w_blocks, h_blocks
                ));
            }
            decode_dxt5(&top_mip, w, h)
        }
        _ => unreachable!(),
    };

    let img: ImageBuffer<Rgba<u8>, Vec<u8>> = ImageBuffer::from_raw(top_size.0 as u32, top_size.1 as u32, rgba)
        .ok_or_else(|| "RGBA buffer size mismatch".to_string())?;
    if let Some(parent) = out_path.parent() {
        fs::create_dir_all(parent).map_err(|e| format!("mkdir {}: {e}", parent.display()))?;
    }
    img.save(out_path).map_err(|e| format!("encode PNG: {e}"))?;

    Ok(Texture2DInfo {
        size_x:       top_size.0 as u32,
        size_y:       top_size.1 as u32,
        pixel_format: pix_fmt,
        num_mips:     num_mips as u32,
    })
}

// ============================================================== BC decoders

fn decode_dxt1(src: &[u8], w: usize, h: usize) -> Vec<u8> {
    let mut dst = vec![0u8; w * h * 4];
    let w_blocks = (w + 3) / 4;
    let h_blocks = (h + 3) / 4;
    for by in 0..h_blocks {
        for bx in 0..w_blocks {
            let off = (by * w_blocks + bx) * 8;
            let c0   = u16::from_le_bytes(src[off..off + 2].try_into().unwrap());
            let c1   = u16::from_le_bytes(src[off + 2..off + 4].try_into().unwrap());
            let bits = u32::from_le_bytes(src[off + 4..off + 8].try_into().unwrap());
            let (r0, g0, b0) = unpack565(c0);
            let (r1, g1, b1) = unpack565(c1);
            let mut pal_r = [0u8; 4];
            let mut pal_g = [0u8; 4];
            let mut pal_b = [0u8; 4];
            pal_r[0] = r0; pal_g[0] = g0; pal_b[0] = b0;
            pal_r[1] = r1; pal_g[1] = g1; pal_b[1] = b1;
            if c0 > c1 {
                // 4-colour mode: 1/3 and 2/3 interpolation.
                pal_r[2] = ((2 * r0 as u32 +     r1 as u32) / 3) as u8;
                pal_g[2] = ((2 * g0 as u32 +     g1 as u32) / 3) as u8;
                pal_b[2] = ((2 * b0 as u32 +     b1 as u32) / 3) as u8;
                pal_r[3] = ((    r0 as u32 + 2 * r1 as u32) / 3) as u8;
                pal_g[3] = ((    g0 as u32 + 2 * g1 as u32) / 3) as u8;
                pal_b[3] = ((    b0 as u32 + 2 * b1 as u32) / 3) as u8;
            } else {
                // 3-colour + transparent black @ index 3. Route-map textures
                // ship no alpha — keeping it opaque black avoids holes.
                pal_r[2] = ((r0 as u32 + r1 as u32) / 2) as u8;
                pal_g[2] = ((g0 as u32 + g1 as u32) / 2) as u8;
                pal_b[2] = ((b0 as u32 + b1 as u32) / 2) as u8;
                pal_r[3] = 0; pal_g[3] = 0; pal_b[3] = 0;
            }
            for py in 0..4 {
                let yy = by * 4 + py;
                if yy >= h { break; }
                for px in 0..4 {
                    let xx = bx * 4 + px;
                    if xx >= w { continue; }
                    let idx = ((bits >> ((py * 4 + px) * 2)) & 0x3) as usize;
                    let di = (yy * w + xx) * 4;
                    dst[di    ] = pal_r[idx];
                    dst[di + 1] = pal_g[idx];
                    dst[di + 2] = pal_b[idx];
                    dst[di + 3] = 255;
                }
            }
        }
    }
    dst
}

fn decode_dxt5(src: &[u8], w: usize, h: usize) -> Vec<u8> {
    let mut dst = vec![0u8; w * h * 4];
    let w_blocks = (w + 3) / 4;
    let h_blocks = (h + 3) / 4;
    for by in 0..h_blocks {
        for bx in 0..w_blocks {
            let off = (by * w_blocks + bx) * 16;
            // Alpha endpoints + 48-bit index stream.
            let a0 = src[off];
            let a1 = src[off + 1];
            let a_bits: u64 =
                  (src[off + 2] as u64)
                | (src[off + 3] as u64) << 8
                | (src[off + 4] as u64) << 16
                | (src[off + 5] as u64) << 24
                | (src[off + 6] as u64) << 32
                | (src[off + 7] as u64) << 40;
            let mut a_pal = [0u8; 8];
            a_pal[0] = a0;
            a_pal[1] = a1;
            if a0 > a1 {
                for i in 1..=6 {
                    a_pal[i + 1] = (((7 - i as u32) * a0 as u32 + i as u32 * a1 as u32) / 7) as u8;
                }
            } else {
                for i in 1..=4 {
                    a_pal[i + 1] = (((5 - i as u32) * a0 as u32 + i as u32 * a1 as u32) / 5) as u8;
                }
                a_pal[6] = 0;
                a_pal[7] = 255;
            }
            // Colour sub-block: BC1 in 4-colour mode regardless of c0/c1.
            let c0   = u16::from_le_bytes(src[off + 8..off + 10].try_into().unwrap());
            let c1   = u16::from_le_bytes(src[off + 10..off + 12].try_into().unwrap());
            let bits = u32::from_le_bytes(src[off + 12..off + 16].try_into().unwrap());
            let (r0, g0, b0) = unpack565(c0);
            let (r1, g1, b1) = unpack565(c1);
            let mut pal_r = [0u8; 4];
            let mut pal_g = [0u8; 4];
            let mut pal_b = [0u8; 4];
            pal_r[0] = r0; pal_g[0] = g0; pal_b[0] = b0;
            pal_r[1] = r1; pal_g[1] = g1; pal_b[1] = b1;
            pal_r[2] = ((2 * r0 as u32 +     r1 as u32) / 3) as u8;
            pal_g[2] = ((2 * g0 as u32 +     g1 as u32) / 3) as u8;
            pal_b[2] = ((2 * b0 as u32 +     b1 as u32) / 3) as u8;
            pal_r[3] = ((    r0 as u32 + 2 * r1 as u32) / 3) as u8;
            pal_g[3] = ((    g0 as u32 + 2 * g1 as u32) / 3) as u8;
            pal_b[3] = ((    b0 as u32 + 2 * b1 as u32) / 3) as u8;
            for py in 0..4 {
                let yy = by * 4 + py;
                if yy >= h { break; }
                for px in 0..4 {
                    let xx = bx * 4 + px;
                    if xx >= w { continue; }
                    let c_idx = ((bits >> ((py * 4 + px) * 2)) & 0x3) as usize;
                    let a_idx = ((a_bits >> ((py * 4 + px) * 3)) & 0x7) as usize;
                    let di = (yy * w + xx) * 4;
                    dst[di    ] = pal_r[c_idx];
                    dst[di + 1] = pal_g[c_idx];
                    dst[di + 2] = pal_b[c_idx];
                    dst[di + 3] = a_pal[a_idx];
                }
            }
        }
    }
    dst
}

/// Expand an RGB565 colour to per-channel u8 using the standard
/// DXT bit-replication so output matches what the GPU would render.
#[inline]
fn unpack565(c: u16) -> (u8, u8, u8) {
    let r5 = ((c >> 11) & 0x1f) as u8;
    let g6 = ((c >>  5) & 0x3f) as u8;
    let b5 = ( c        & 0x1f) as u8;
    let r = (r5 << 3) | (r5 >> 2);
    let g = (g6 << 2) | (g6 >> 4);
    let b = (b5 << 3) | (b5 >> 2);
    (r, g, b)
}

/// Sanitise a thumbnail name for the filesystem. Byte-for-byte port
/// of hud-go's `pak.SanitiseThumbnailName`:
///   * keep `[A-Za-z0-9-]` and `_` as-is
///   * convert ` ` (space) to `_`
///   * **drop** every other character (including `.`, `&`, `,`, etc.)
/// The previous Rust impl kept `.` and converted other invalid chars
/// to `_`, which produced filenames like `BR_1462_DB` where hud-go
/// produced `BR1462_DB`. The importer / `train_classes.thumbnail_path`
/// uses hud-go's form, so paths diverged silently and the writer's
/// fs::read(...) missed the cached PNGs.
pub fn sanitise_thumbnail_name(name: &str) -> String {
    let mut out = String::with_capacity(name.len());
    for ch in name.chars() {
        match ch {
            'a'..='z' | 'A'..='Z' | '0'..='9' | '-' | '_' => out.push(ch),
            ' ' => out.push('_'),
            _ => {} // drop everything else
        }
    }
    if out.is_empty() { "unknown".to_string() } else { out }
}
