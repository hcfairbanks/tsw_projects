//! UAsset / UMap binary reader. Port of
//! hud-go-src/internal/pak/uasset/{umap.go, parse.go reader half}.
//!
//! Reads UE 4.27 `.uasset`/`.uexp` pairs into a minimal header view:
//! package summary, NameMap, ExportMap, ImportMap. The companion property
//! walker (port of `parse.go`'s tag-stream half) is staged for the next
//! slice; this module ships the foundation everything else builds on.

use std::fs;
use std::path::{Path, PathBuf};

/// `.uasset` magic. Every header file starts with these four little-endian
/// bytes, and the `.uexp` body ends with them as a trailer.
pub const UASSET_MAGIC: u32 = 0x9E2A83C1;

#[derive(Debug, Clone)]
pub struct ImportEntry {
    pub class_name:  String,
    pub object_name: String,
}

#[derive(Debug, Clone)]
pub struct ExportEntry {
    pub object_name:   String,
    /// FPackageIndex into the ImportMap (when negative) or ExportMap
    /// (when positive) — `0` means "this object IS the package".
    pub class_index:   i32,
    /// FPackageIndex of the owning Outer. Same sign convention as
    /// `class_index`; tracked so we can walk Component → Actor for
    /// transform composition later on.
    pub outer_index:   i32,
    /// Absolute offset into the `.uexp` body where this export's serial
    /// stream begins. `Umap::property_slice` resolves it.
    pub serial_offset: i64,
    pub serial_size:   i64,
}

/// Minimally-parsed `.uasset`/`.uexp` pair.
pub struct Umap {
    pub names:   Vec<String>,
    pub exports: Vec<ExportEntry>,
    pub imports: Vec<ImportEntry>,

    /// Body of the `.uexp` (trailing magic stripped). `property_slice`
    /// borrows ranges out of this.
    uexp:        Vec<u8>,
    /// Equals the summary's `TotalHeaderSize`. Serial offsets in the
    /// ExportMap are absolute file-byte indices, so subtracting this
    /// yields the offset into `uexp`.
    header_size: usize,
}

impl Umap {
    /// Resolve a UE `FPackageIndex` to a label:
    ///   * `0`        → ""
    ///   * positive   → ExportMap[idx-1].object_name
    ///   * negative   → ImportMap[-idx-1].object_name, or "" out of range
    pub fn resolve_fpackage_index(&self, idx: i32) -> String {
        if idx == 0 {
            return String::new();
        }
        if idx > 0 {
            let i = (idx - 1) as usize;
            if i < self.exports.len() {
                return self.exports[i].object_name.clone();
            }
        } else {
            let i = (-idx - 1) as usize;
            if i < self.imports.len() {
                return self.imports[i].object_name.clone();
            }
        }
        String::new()
    }

    /// Get the byte slice for one export's property stream. Returns `None`
    /// when the offset would land outside the `.uexp` body (would mean the
    /// export's data is in the `.uasset` itself — never happens for the
    /// exports we care about, but callers should still handle it).
    pub fn property_slice(&self, e: &ExportEntry) -> Option<&[u8]> {
        let off = (e.serial_offset as i64) - (self.header_size as i64);
        if off < 0 { return None }
        let off = off as usize;
        if off >= self.uexp.len() { return None }
        let mut end = off + (e.serial_size as usize);
        if end > self.uexp.len() {
            end = self.uexp.len();
        }
        Some(&self.uexp[off..end])
    }

    /// Open one `.uasset` (and its sibling `.uexp`) and parse just the
    /// header + maps. Mirrors hud-go's `ReadUmap`.
    ///
    /// Two-step pattern:
    ///   1. summary read (no NameMap needed — only fstr / skip / i32 / i64)
    ///   2. build the NameMap as a `Vec<String>` we own
    ///   3. fresh Reader bound to the populated NameMap to walk the
    ///      ExportMap + ImportMap (which use FName indices)
    pub fn read(uasset_path: &Path) -> Result<Umap, String> {
        let uasset_data = fs::read(uasset_path)
            .map_err(|e| format!("read {}: {e}", uasset_path.display()))?;
        let uexp_path = swap_extension(uasset_path, "uexp");
        let mut uexp_data = fs::read(&uexp_path)
            .map_err(|e| format!("read {}: {e}", uexp_path.display()))?;

        // .uexp ends with the same 4-byte magic as the asset header — strip
        // it so absolute offsets land at the right byte even at the tail.
        if uexp_data.len() >= 4 {
            let last4 = u32::from_le_bytes(
                uexp_data[uexp_data.len() - 4..].try_into().unwrap()
            );
            if last4 == UASSET_MAGIC {
                uexp_data.truncate(uexp_data.len() - 4);
            }
        }

        // ---- Step 1: summary (no NameMap) ----
        let empty_names: Vec<String> = Vec::new();
        let mut r = Reader::new(&uasset_data, &empty_names);

        if r.i32() as u32 != UASSET_MAGIC {
            return Err(format!("{}: bad magic", uasset_path.display()));
        }
        let legacy = r.i32();
        if legacy != -4 { r.skip(4); } // LegacyUE3Version
        r.skip(4); // FileVersionUE4
        r.skip(4); // FileVersionLicenseeUE4
        let cv_count = r.i32() as i64;
        r.skip((cv_count.max(0) as usize) * 20);

        let total_header_size = r.i32() as usize;
        let _ = r.fstr();   // FolderName
        r.skip(4);          // PackageFlags

        let name_count    = r.i32() as i64;
        let name_offset   = r.i32() as i64;
        r.skip(4); r.skip(4);              // GatherableText {Count, Offset}
        let export_count  = r.i32() as i64;
        let export_offset = r.i32() as i64;
        let import_count  = r.i32() as i64;
        let import_offset = r.i32() as i64;

        // ---- Step 2: NameMap ----
        r.seek(name_offset as usize);
        let name_count_us = name_count.max(0) as usize;
        let mut names = Vec::with_capacity(name_count_us);
        for _ in 0..name_count_us {
            names.push(r.fstr());
            r.skip(4); // NonCasePreservingHash + CasePreservingHash (uint16 each)
        }

        // ---- Step 3: ExportMap + ImportMap, with the NameMap populated ----
        let mut r2 = Reader::new(&uasset_data, &names);

        // UE 4.27 FObjectExport is fixed-size: ClassIndex SuperIndex
        // TemplateIndex OuterIndex ObjectName(FName=8B) SaveFlags
        // SerialSize SerialOffset ... + 60 B of trailing fields we skip.
        // Total 100 B per record.
        r2.seek(export_offset as usize);
        let export_count_us = export_count.max(0) as usize;
        let mut exports = Vec::with_capacity(export_count_us);
        for _ in 0..export_count_us {
            let class_index = r2.i32();
            r2.skip(4); // SuperIndex
            r2.skip(4); // TemplateIndex
            let outer_index = r2.i32();
            let object_name = r2.fname();
            r2.skip(4); // SaveFlags / ObjectFlags
            let serial_size   = r2.i64();
            let serial_offset = r2.i64();
            r2.skip(60); // 12 (3 bools) + 16 (PackageGuid) + 4 + 8 + 4 + 16
            exports.push(ExportEntry {
                object_name, class_index, outer_index,
                serial_offset, serial_size,
            });
        }

        // UE 4.27 stock FObjectImport: ClassPackage FName (8 B), ClassName
        // FName (8 B), OuterIndex i32 (4 B), ObjectName FName (8 B) = 28 B.
        // hud-go used to assume 40 B (mis-remembered PackageName +
        // bImportOptional that TSW6 doesn't carry) and drifted after the
        // first record; we keep the correct 28 B stride.
        let import_count_us = import_count.max(0) as usize;
        let mut imports = Vec::with_capacity(import_count_us);
        if import_count_us > 0 {
            r2.seek(import_offset as usize);
            for _ in 0..import_count_us {
                r2.skip(8); // ClassPackage FName
                let class_name = r2.fname();
                r2.skip(4); // OuterIndex
                let object_name = r2.fname();
                imports.push(ImportEntry { class_name, object_name });
            }
        }

        Ok(Umap {
            names, exports, imports,
            uexp: uexp_data,
            header_size: total_header_size,
        })
    }
}

fn swap_extension(p: &Path, new_ext: &str) -> PathBuf {
    let mut out = p.to_path_buf();
    out.set_extension(new_ext);
    out
}

/// Format a UE `FGuid` (16 bytes laid out as 4 little-endian u32s) the
/// same way hud-go's `fmtGUID(raw)` + `NormalizeGUID` chain does:
/// lowercase, no separators, 32 hex chars.
pub fn format_guid(raw: &[u8; 16]) -> String {
    let a = u32::from_le_bytes(raw[0..4].try_into().unwrap());
    let b = u32::from_le_bytes(raw[4..8].try_into().unwrap());
    let c = u32::from_le_bytes(raw[8..12].try_into().unwrap());
    let d = u32::from_le_bytes(raw[12..16].try_into().unwrap());
    format!("{:08x}{:08x}{:08x}{:08x}", a, b, c, d)
}

/// Lowercase + strip separators. Hud-go does this on every GUID before
/// hashing, so all maps key off the same form.
pub fn normalize_guid(s: &str) -> String {
    s.chars()
        .filter(|c| c.is_ascii_alphanumeric())
        .map(|c| c.to_ascii_lowercase())
        .collect()
}

impl Umap {
    /// Convenience: get a Reader scoped to one export's property stream.
    /// Returns `None` when the export's data is somehow outside the
    /// `.uexp` body (which doesn't happen in practice for cooked TSW6
    /// packages).
    pub fn property_reader<'a>(&'a self, e: &ExportEntry) -> Option<Reader<'a>> {
        let slice = self.property_slice(e)?;
        Some(Reader::new(slice, &self.names))
    }
}

// =================================================================== Reader
//
// Tiny little-endian cursor with FName / FString readers. Same surface as
// hud-go's unexported `reader`. The `name_map` is a borrow tied to the
// owning package's lifetime; the Umap parser does its two-step read
// (summary fields → build NameMap → fresh Reader for ExportMap/ImportMap
// with the populated NameMap) so we never need to mutate the NameMap
// mid-cursor.
pub struct Reader<'a> {
    pub data:     &'a [u8],
    pub pos:      usize,
    pub name_map: &'a [String],
}

impl<'a> Reader<'a> {
    pub fn new(data: &'a [u8], name_map: &'a [String]) -> Self {
        Self { data, pos: 0, name_map }
    }

    #[inline] pub fn remaining(&self) -> usize { self.data.len().saturating_sub(self.pos) }
    #[inline] pub fn tell(&self) -> usize { self.pos }
    #[inline] pub fn seek(&mut self, p: usize) { self.pos = p; }
    #[inline] pub fn skip(&mut self, n: usize) { self.pos += n; }

    #[inline]
    pub fn u8(&mut self) -> u8 {
        let v = self.data[self.pos];
        self.pos += 1;
        v
    }

    #[inline]
    pub fn i32(&mut self) -> i32 {
        let v = i32::from_le_bytes(self.data[self.pos..self.pos+4].try_into().unwrap());
        self.pos += 4;
        v
    }

    #[inline]
    pub fn i64(&mut self) -> i64 {
        let v = i64::from_le_bytes(self.data[self.pos..self.pos+8].try_into().unwrap());
        self.pos += 8;
        v
    }

    #[inline]
    pub fn f32(&mut self) -> f32 {
        let bits = u32::from_le_bytes(self.data[self.pos..self.pos+4].try_into().unwrap());
        self.pos += 4;
        f32::from_bits(bits)
    }

    #[inline]
    pub fn f64(&mut self) -> f64 {
        let bits = u64::from_le_bytes(self.data[self.pos..self.pos+8].try_into().unwrap());
        self.pos += 8;
        f64::from_bits(bits)
    }

    #[inline]
    pub fn u32(&mut self) -> u32 {
        let v = u32::from_le_bytes(self.data[self.pos..self.pos+4].try_into().unwrap());
        self.pos += 4;
        v
    }

    /// Read a 16-byte GUID and return it as a lowercase 32-char hex
    /// string with no separators (matches hud-go's `fmtGUID(raw)` +
    /// `NormalizeGUID` chain). Mirrors UE's `FGuid` layout: 4 little-
    /// endian u32 components rendered Big-Endian per component, then
    /// concatenated.
    pub fn guid_str(&mut self) -> String {
        let mut raw = [0u8; 16];
        raw.copy_from_slice(&self.data[self.pos..self.pos+16]);
        self.pos += 16;
        format_guid(&raw)
    }

    pub fn guid(&mut self) { self.skip(16); }

    /// Read an FName: `(NameIndex i32, Number i32)`. Returns `"?N"` for
    /// out-of-range indices so a corrupt stream is debuggable rather than
    /// silently zero-string'd.
    pub fn fname(&mut self) -> String {
        let idx = self.i32();
        let num = self.i32();
        if idx < 0 || (idx as usize) >= self.name_map.len() {
            return format!("?{idx}");
        }
        let s = &self.name_map[idx as usize];
        if num > 0 { format!("{s}_{}", num - 1) } else { s.clone() }
    }

    /// Read an FString: length-prefixed, ASCII for positive lengths,
    /// UTF-16LE for negative. Trailing null byte/wchar is consumed.
    /// Bogus lengths exceed the buffer empty out and consume nothing
    /// (rather than panic on a malformed asset).
    pub fn fstr(&mut self) -> String {
        let n = self.i32();
        if n == 0 { return String::new() }
        if n > 0 {
            let n = n as usize;
            if n > self.remaining() {
                self.pos = self.data.len();
                return String::new();
            }
            // Trailing NUL byte excluded from the returned string.
            let body = &self.data[self.pos..self.pos + n - 1];
            self.pos += n;
            return String::from_utf8_lossy(body).into_owned();
        }
        let n = (-n) as usize;
        if n * 2 > self.remaining() {
            self.pos = self.data.len();
            return String::new();
        }
        let bytes = &self.data[self.pos..self.pos + n * 2];
        self.pos += n * 2;
        let mut runes: Vec<u16> = Vec::with_capacity(n - 1);
        for i in 0..(n - 1) {
            let lo = bytes[i * 2] as u16;
            let hi = bytes[i * 2 + 1] as u16;
            runes.push(lo | (hi << 8));
        }
        String::from_utf16_lossy(&runes)
    }

    /// Read an FText blob bounded by `size` bytes from this cursor. The
    /// cursor always advances exactly `size` bytes, even on history
    /// variants we don't decode (returns "" in that case).
    pub fn ftext(&mut self, size: usize) -> String {
        let field_end = self.pos + size;
        if size < 5 {
            self.pos = field_end;
            return String::new();
        }
        self.skip(4); // flags
        let history = self.u8();
        let result = match history {
            0xFF => {
                // None: optional bool + FString
                if self.pos >= field_end { String::new() }
                else if self.u8() != 0 { self.fstr() }
                else { String::new() }
            }
            0 => {
                // Base: namespace, key, source
                let _ns  = self.fstr();
                let _key = self.fstr();
                self.fstr()
            }
            _ => String::new(),
        };
        self.pos = field_end;
        result
    }
}

// =================================================================== Tag stream
//
// UE serialises a property set as `FPropertyTag` records terminated by an
// `None` FName. Each tag declares the property's class, byte size, and a
// handful of variant-specific fields (struct type, array inner type, etc.)
// before the payload. Callers walk the stream by `read_tag`'ing in a loop,
// either decoding the payload they recognise or seeking past `size` bytes
// to skip.

#[derive(Debug, Clone, Default)]
pub struct TagInfo {
    pub name:           String,
    pub ptype:          String,
    pub struct_type:    String,
    pub inner_type:     String,
    /// MapProperty only: key + value element FNames.
    pub map_key_type:   String,
    pub map_value_type: String,
    pub size:           i32,
    /// BoolProperty packs its value into the header rather than the
    /// payload (size == 0). Other property types leave this at 0.
    pub bool_val:       u8,
}

impl<'a> Reader<'a> {
    /// Read one FPropertyTag header. Returns `None` when the `None`
    /// sentinel FName is hit — signalling the end of the property list.
    pub fn read_tag(&mut self) -> Option<TagInfo> {
        let name = self.fname();
        if name == "None" {
            return None;
        }
        let ptype = self.fname();
        let size  = self.i32();
        self.skip(4); // arr_idx

        let mut t = TagInfo {
            name, ptype: ptype.clone(),
            size, ..Default::default()
        };
        match ptype.as_str() {
            "StructProperty" => {
                t.struct_type = self.fname();
                self.guid();
            }
            "BoolProperty" => {
                t.bool_val = self.u8();
            }
            "ByteProperty" | "EnumProperty" => {
                t.inner_type = self.fname();
            }
            "ArrayProperty" => {
                t.inner_type = self.fname();
            }
            "MapProperty" => {
                t.map_key_type   = self.fname();
                t.map_value_type = self.fname();
            }
            "SetProperty" => {
                t.inner_type = self.fname();
            }
            _ => {}
        }
        if self.u8() != 0 {
            self.guid(); // has_guid flag → 16 B payload
        }
        Some(t)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Smoke-test the fstr ASCII path: length-prefixed null-terminated.
    #[test]
    fn fstr_ascii() {
        // "hi\0" with length 3 (i32 little-endian).
        let mut data = (3i32).to_le_bytes().to_vec();
        data.extend_from_slice(b"hi\0");
        let names: Vec<String> = Vec::new();
        let mut r = Reader::new(&data, &names);
        assert_eq!(r.fstr(), "hi");
    }

    /// Smoke-test fstr UTF-16: negative length, two-byte chars, null word.
    #[test]
    fn fstr_utf16() {
        // length = -3 → 3 wchars including trailing null. "hi" + \0.
        let mut data = (-3i32).to_le_bytes().to_vec();
        data.extend_from_slice(&[b'h', 0, b'i', 0, 0, 0]);
        let names: Vec<String> = Vec::new();
        let mut r = Reader::new(&data, &names);
        assert_eq!(r.fstr(), "hi");
    }

    /// Smoke-test fname against a tiny NameMap.
    #[test]
    fn fname_lookup() {
        // idx=1, num=0 → "Bravo"
        let names = vec!["Alpha".to_string(), "Bravo".to_string()];
        let mut data = 1i32.to_le_bytes().to_vec();
        data.extend_from_slice(&0i32.to_le_bytes());
        let mut r = Reader::new(&data, &names);
        assert_eq!(r.fname(), "Bravo");

        // idx=0, num=2 → "Alpha_1"
        let mut data = 0i32.to_le_bytes().to_vec();
        data.extend_from_slice(&2i32.to_le_bytes());
        let mut r = Reader::new(&data, &names);
        assert_eq!(r.fname(), "Alpha_1");

        // Out-of-range index → "?<idx>" sentinel, never panics.
        let mut data = 99i32.to_le_bytes().to_vec();
        data.extend_from_slice(&0i32.to_le_bytes());
        let mut r = Reader::new(&data, &names);
        assert_eq!(r.fname(), "?99");
    }

    /// Synthesize a tag header for an Int32 property: FName "Score" /
    /// "IntProperty" / size=4 / arr_idx=0 / has_guid=0.
    #[test]
    fn read_tag_int_property() {
        let names = vec![
            "None".to_string(),       // idx 0 — sentinel
            "Score".to_string(),      // idx 1
            "IntProperty".to_string(),// idx 2
        ];
        // name=Score(idx=1,num=0) ptype=IntProperty(idx=2,num=0) size=4 arr_idx=0 has_guid=0
        let mut data: Vec<u8> = Vec::new();
        data.extend_from_slice(&1i32.to_le_bytes()); data.extend_from_slice(&0i32.to_le_bytes()); // name
        data.extend_from_slice(&2i32.to_le_bytes()); data.extend_from_slice(&0i32.to_le_bytes()); // ptype
        data.extend_from_slice(&4i32.to_le_bytes()); // size
        data.extend_from_slice(&0i32.to_le_bytes()); // arr_idx
        data.push(0); // has_guid flag

        let mut r = Reader::new(&data, &names);
        let t = r.read_tag().expect("expected a tag, not None");
        assert_eq!(t.name,  "Score");
        assert_eq!(t.ptype, "IntProperty");
        assert_eq!(t.size,  4);
    }

    /// "None" sentinel terminates the tag stream.
    #[test]
    fn read_tag_none_sentinel() {
        let names = vec!["None".to_string()];
        // name=None(idx=0,num=0). read_tag returns immediately.
        let mut data: Vec<u8> = Vec::new();
        data.extend_from_slice(&0i32.to_le_bytes());
        data.extend_from_slice(&0i32.to_le_bytes());
        let mut r = Reader::new(&data, &names);
        assert!(r.read_tag().is_none());
    }

    #[test]
    fn resolve_fpackage_index_basic() {
        let names: Vec<String> = vec![];
        let u = Umap {
            names: names.clone(),
            exports: vec![ExportEntry {
                object_name: "ExpA".into(),
                class_index: 0, outer_index: 0,
                serial_offset: 0, serial_size: 0,
            }],
            imports: vec![ImportEntry {
                class_name: "Class".into(),
                object_name: "ImpA".into(),
            }],
            uexp: vec![],
            header_size: 0,
        };
        assert_eq!(u.resolve_fpackage_index(0), "");
        assert_eq!(u.resolve_fpackage_index(1), "ExpA");
        assert_eq!(u.resolve_fpackage_index(-1), "ImpA");
        assert_eq!(u.resolve_fpackage_index(42), "");
        assert_eq!(u.resolve_fpackage_index(-42), "");
    }
}
