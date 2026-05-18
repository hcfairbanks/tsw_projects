// texture.go — extract Texture2D pixel data from a cooked UE 4.27 uasset.
//
// Why this exists: TSW6 ships pre-rendered route map textures
// (e.g. TrainingCentre/Content/RouteDefinition/TrainingCentre_Small_1676x468)
// that are exactly the 2D top-down image the game's UI uses. Decoding them
// gives us a ground-truth basemap directly — no NetworkRibbon walker, no
// chaining, no clothoid integration required.
//
// Supports PF_DXT1 (BC1), PF_DXT5 (BC3), and PF_B8G8R8A8 (uncompressed
// BGRA). The low-res route preview tile uses DXT1; the high-res map uses
// BGRA8. Train class thumbnails on newer DLCs (BR 111 DB, etc.) ship as
// DXT5 — BC1 color + BC4 alpha — which is why DXT5 support is required
// alongside the route-map decoders.
package uasset

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"strings"
)

// Texture2DInfo summarises a decoded Texture2D — returned alongside the PNG
// path so the caller can report the source format / dimensions / mip layout.
type Texture2DInfo struct {
	SizeX, SizeY int
	PixelFormat  string
	NumMips      int
	MipBytes     []int // bytes-on-disk of each mip in serialization order
}

// ExtractTexture2DPNG reads a cooked Texture2D uasset and writes its top mip
// as a PNG to outPath. Returns the format/size info for logging.
func ExtractTexture2DPNG(uassetPath, outPath string) (*Texture2DInfo, error) {
	u, err := ReadUmap(uassetPath)
	if err != nil {
		return nil, fmt.Errorf("read uasset: %w", err)
	}
	if len(u.Exports) == 0 {
		return nil, fmt.Errorf("no exports")
	}
	// Pick the first non-default export — Texture2D uassets typically have
	// exactly one (the texture instance).
	var exp ExportEntry
	found := false
	for _, e := range u.Exports {
		if e.ObjectName != "" && !strings.HasPrefix(e.ObjectName, "Default__") {
			exp = e
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("no non-default export")
	}
	pr := u.PropertyReader(exp)
	if pr == nil {
		return nil, fmt.Errorf("export %s has no .uexp data", exp.ObjectName)
	}
	// Walk the standard FPropertyTag stream to its "None" terminator. We
	// don't care about any of the values — we just need to be positioned at
	// the start of the post-properties cooked Texture2D blob.
	for {
		_, ok := pr.readTag()
		if !ok {
			break
		}
		// readTag has already advanced past the tag header; we need to skip
		// the body (size bytes are part of the tag). But readTag doesn't
		// advance past the value — we must do it. Look at how dumpOneProperty
		// handles this: t.size is the value-size after the header. Skip it.
		// Actually, we can't get t.size without re-reading, and readTag does
		// advance past its header but not the body. The dumper uses
		// t.size + post-header advance. For our case we don't need the data
		// at all so we read tag.size and skip:
		//
		// — but we already lost the tag. Solution: re-implement the loop
		// inline. (Below.)
		break
	}
	// Re-seek to the start of the property stream and walk it correctly.
	pr.seek(0)
	for {
		t, ok := pr.readTag()
		if !ok {
			break
		}
		pr.skip(t.size)
	}
	// Now positioned just past the "None" terminator. UE 4.27 cooked
	// Texture2D layout from here:
	//   FStripDataFlags GlobalStripFlags (1 byte) (or 2 if also class flags)
	//   FStripDataFlags ClassStripFlags  (1 byte)
	//   bCooked            (uint32 = 1)
	//   PixelFormatName    (FName, 8 bytes)
	//   while PixelFormatName != "None":
	//     SkipOffset       (int64, 8 bytes — absolute file offset to skip past this PlatformData)
	//     FTexturePlatformData:
	//       SizeX, SizeY, NumSlices (int32 each)
	//       PixelFormatString FString
	//       FirstMipToSerialize (int32)
	//       NumMips (int32)
	//       for each mip:
	//         bCooked (uint32 = 1)
	//         FByteBulkData (4-byte flags, 4-byte ElementCount, 4-byte SizeOnDisk, 8-byte OffsetInFile, payload bytes)
	//         SizeX, SizeY, SizeZ (int32 each)
	//       bIsVirtual (uint32)
	//     PixelFormatName = next FName
	//
	// Eight bytes of class-serialization overhead before bCooked. UE's
	// UTexture::Serialize and UTexture2D::Serialize each emit a
	// 2-byte FStripDataFlags (Global+Class). Verified empirically against
	// TC's TrainingCentre_Small_1676x468.uasset where the post-properties
	// stream is `00 00 00 00 01 00 01 00 01 00 00 00 [bCooked]…`. The
	// non-zero bytes don't disambiguate any branches we care about — we just
	// need to land on bCooked.
	pr.skip(8)
	bCooked := pr.i32()
	if bCooked != 1 {
		return nil, fmt.Errorf("bCooked=%d, expected 1 (post-properties layout drift?)", bCooked)
	}
	pixFmtName := pr.fname()
	if pixFmtName == "None" {
		return nil, fmt.Errorf("no platform data (first PixelFormatName is None)")
	}
	if pixFmtName != "PF_DXT1" && pixFmtName != "PF_DXT5" && pixFmtName != "PF_B8G8R8A8" {
		return nil, fmt.Errorf("unsupported pixel format %q (only PF_DXT1, PF_DXT5, PF_B8G8R8A8 implemented)", pixFmtName)
	}
	_ = pr.i64() // SkipOffset — we always read this PlatformData, never skip
	sizeX := int(pr.i32())
	sizeY := int(pr.i32())
	numSlices := pr.i32()
	if numSlices != 1 {
		return nil, fmt.Errorf("NumSlices=%d (expected 1 for Texture2D)", numSlices)
	}
	pixFmtStr := pr.fstr()
	if pixFmtStr != pixFmtName {
		return nil, fmt.Errorf("PixelFormatString=%q != FName %q (disagreement)", pixFmtStr, pixFmtName)
	}
	firstMip := pr.i32()
	_ = firstMip
	numMips := int(pr.i32())
	if numMips < 1 || numMips > 32 {
		return nil, fmt.Errorf("NumMips=%d, sanity check failed", numMips)
	}

	info := &Texture2DInfo{
		SizeX: sizeX, SizeY: sizeY, PixelFormat: pixFmtName, NumMips: numMips,
	}

	// Decode mip 0 — that's the highest-resolution version. UE serializes
	// mips largest-first, so the first mip is the one we want.
	var topMip []byte
	for m := 0; m < numMips; m++ {
		mipCooked := pr.i32()
		if mipCooked != 1 {
			return nil, fmt.Errorf("mip %d bCooked=%d", m, mipCooked)
		}
		// FByteBulkData header (cooked, inline payload)
		_ = pr.i32() // BulkDataFlags — for inline cooked, lower bits include BULKDATA_PayloadAtEndOfFile etc; we don't gate on them
		elemCount := int(pr.i32())
		sizeOnDisk := int(pr.i32())
		_ = pr.i64() // OffsetInFile — irrelevant when payload is inline immediately after the header
		payload := make([]byte, elemCount)
		for i := 0; i < elemCount && pr.remaining() > 0; i++ {
			payload[i] = pr.u8()
		}
		_ = sizeOnDisk
		mipSizeX := int(pr.i32())
		mipSizeY := int(pr.i32())
		_ = pr.i32() // SizeZ — always 1 for Texture2D
		info.MipBytes = append(info.MipBytes, elemCount)
		if m == 0 {
			topMip = payload
			if mipSizeX != sizeX || mipSizeY != sizeY {
				return nil, fmt.Errorf("mip 0 size %dx%d != header %dx%d",
					mipSizeX, mipSizeY, sizeX, sizeY)
			}
		}
	}
	// (We don't read bIsVirtual or any subsequent PlatformData — we have what
	// we need from the first one.)

	if len(topMip) == 0 {
		return nil, fmt.Errorf("mip 0 has no pixel data")
	}

	var rgba []byte
	switch pixFmtName {
	case "PF_B8G8R8A8":
		expected := sizeX * sizeY * 4
		if len(topMip) != expected {
			return nil, fmt.Errorf("mip 0 byte count %d != expected BGRA8 size %d (%dx%d)",
				len(topMip), expected, sizeX, sizeY)
		}
		rgba = make([]byte, expected)
		for i := 0; i < expected; i += 4 {
			rgba[i+0] = topMip[i+2] // R ← B-channel position
			rgba[i+1] = topMip[i+1] // G
			rgba[i+2] = topMip[i+0] // B ← R-channel position
			rgba[i+3] = topMip[i+3] // A
		}
	case "PF_DXT1":
		// DXT1 expects 8 bytes per 4×4 block. Padded width/height to next mul of 4.
		wBlocks := (sizeX + 3) / 4
		hBlocks := (sizeY + 3) / 4
		expected := wBlocks * hBlocks * 8
		if len(topMip) != expected {
			return nil, fmt.Errorf("mip 0 byte count %d != expected DXT1 size %d (%dx%d → %dx%d blocks)",
				len(topMip), expected, sizeX, sizeY, wBlocks, hBlocks)
		}
		rgba = decodeDXT1(topMip, sizeX, sizeY)
	case "PF_DXT5":
		// DXT5 (BC3) is 16 bytes per 4×4 block: 8 bytes BC4-style alpha block
		// followed by 8 bytes BC1 color block (always 4-color mode).
		wBlocks := (sizeX + 3) / 4
		hBlocks := (sizeY + 3) / 4
		expected := wBlocks * hBlocks * 16
		if len(topMip) != expected {
			return nil, fmt.Errorf("mip 0 byte count %d != expected DXT5 size %d (%dx%d → %dx%d blocks)",
				len(topMip), expected, sizeX, sizeY, wBlocks, hBlocks)
		}
		rgba = decodeDXT5(topMip, sizeX, sizeY)
	default:
		return nil, fmt.Errorf("unreachable: format %q passed gate but no decoder", pixFmtName)
	}
	img := &image.NRGBA{
		Pix:    rgba,
		Stride: sizeX * 4,
		Rect:   image.Rect(0, 0, sizeX, sizeY),
	}
	f, err := os.Create(outPath)
	if err != nil {
		return nil, fmt.Errorf("create png: %w", err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		return nil, fmt.Errorf("encode png: %w", err)
	}
	return info, nil
}

// decodeDXT1 expands BC1-compressed bytes into RGBA8 (alpha=255). Each
// 4×4 block: two RGB565 colours + four 2-bit interpolation indices per pixel.
// We follow the standard interpolation rules; texels outside the actual
// SizeX/SizeY (because the texture isn't block-aligned) are dropped.
func decodeDXT1(src []byte, w, h int) []byte {
	dst := make([]byte, w*h*4)
	wBlocks := (w + 3) / 4
	hBlocks := (h + 3) / 4
	for by := 0; by < hBlocks; by++ {
		for bx := 0; bx < wBlocks; bx++ {
			off := (by*wBlocks + bx) * 8
			c0 := binary.LittleEndian.Uint16(src[off:])
			c1 := binary.LittleEndian.Uint16(src[off+2:])
			bits := binary.LittleEndian.Uint32(src[off+4:])
			r0, g0, b0 := unpack565(c0)
			r1, g1, b1 := unpack565(c1)
			var palR, palG, palB [4]uint8
			palR[0], palG[0], palB[0] = r0, g0, b0
			palR[1], palG[1], palB[1] = r1, g1, b1
			if c0 > c1 {
				// Four-colour block: interpolated 1/3 and 2/3 mixes.
				palR[2] = uint8((2*int(r0) + int(r1)) / 3)
				palG[2] = uint8((2*int(g0) + int(g1)) / 3)
				palB[2] = uint8((2*int(b0) + int(b1)) / 3)
				palR[3] = uint8((int(r0) + 2*int(r1)) / 3)
				palG[3] = uint8((int(g0) + 2*int(g1)) / 3)
				palB[3] = uint8((int(b0) + 2*int(b1)) / 3)
			} else {
				// Three-colour block + transparent index 3. We force opaque
				// black for index 3 since the route map texture has no alpha
				// channel and treating it as transparent would create holes.
				palR[2] = uint8((int(r0) + int(r1)) / 2)
				palG[2] = uint8((int(g0) + int(g1)) / 2)
				palB[2] = uint8((int(b0) + int(b1)) / 2)
				palR[3], palG[3], palB[3] = 0, 0, 0
			}
			for py := 0; py < 4; py++ {
				yy := by*4 + py
				if yy >= h {
					break
				}
				for px := 0; px < 4; px++ {
					xx := bx*4 + px
					if xx >= w {
						continue
					}
					idx := (bits >> uint((py*4+px)*2)) & 0x3
					di := (yy*w + xx) * 4
					dst[di+0] = palR[idx]
					dst[di+1] = palG[idx]
					dst[di+2] = palB[idx]
					dst[di+3] = 255
				}
			}
		}
	}
	return dst
}

// decodeDXT5 expands BC3-compressed bytes into RGBA8. Each 16-byte block
// holds an 8-byte alpha sub-block (BC4-style: two 8-bit endpoints + 16
// × 3-bit indices into an 8-entry alpha palette) followed by an 8-byte
// colour sub-block (BC1, always in the 4-colour interpolation mode).
// Texels outside the actual SizeX/SizeY are dropped.
func decodeDXT5(src []byte, w, h int) []byte {
	dst := make([]byte, w*h*4)
	wBlocks := (w + 3) / 4
	hBlocks := (h + 3) / 4
	for by := 0; by < hBlocks; by++ {
		for bx := 0; bx < wBlocks; bx++ {
			off := (by*wBlocks + bx) * 16
			// Alpha endpoints + 48-bit index stream.
			a0 := src[off+0]
			a1 := src[off+1]
			aBits := uint64(src[off+2]) |
				uint64(src[off+3])<<8 |
				uint64(src[off+4])<<16 |
				uint64(src[off+5])<<24 |
				uint64(src[off+6])<<32 |
				uint64(src[off+7])<<40
			var aPal [8]uint8
			aPal[0] = a0
			aPal[1] = a1
			if a0 > a1 {
				// 6 interpolated alpha values.
				for i := 1; i <= 6; i++ {
					aPal[i+1] = uint8(((7-i)*int(a0) + i*int(a1)) / 7)
				}
			} else {
				// 4 interpolated + transparent + opaque.
				for i := 1; i <= 4; i++ {
					aPal[i+1] = uint8(((5-i)*int(a0) + i*int(a1)) / 5)
				}
				aPal[6] = 0
				aPal[7] = 255
			}
			// Colour sub-block: BC1 in 4-colour mode (interpolated 1/3 and
			// 2/3 mixes regardless of c0/c1 ordering).
			c0 := binary.LittleEndian.Uint16(src[off+8:])
			c1 := binary.LittleEndian.Uint16(src[off+10:])
			bits := binary.LittleEndian.Uint32(src[off+12:])
			r0, g0, b0 := unpack565(c0)
			r1, g1, b1 := unpack565(c1)
			var palR, palG, palB [4]uint8
			palR[0], palG[0], palB[0] = r0, g0, b0
			palR[1], palG[1], palB[1] = r1, g1, b1
			palR[2] = uint8((2*int(r0) + int(r1)) / 3)
			palG[2] = uint8((2*int(g0) + int(g1)) / 3)
			palB[2] = uint8((2*int(b0) + int(b1)) / 3)
			palR[3] = uint8((int(r0) + 2*int(r1)) / 3)
			palG[3] = uint8((int(g0) + 2*int(g1)) / 3)
			palB[3] = uint8((int(b0) + 2*int(b1)) / 3)
			for py := 0; py < 4; py++ {
				yy := by*4 + py
				if yy >= h {
					break
				}
				for px := 0; px < 4; px++ {
					xx := bx*4 + px
					if xx >= w {
						continue
					}
					cIdx := (bits >> uint((py*4+px)*2)) & 0x3
					aIdx := (aBits >> uint((py*4+px)*3)) & 0x7
					di := (yy*w + xx) * 4
					dst[di+0] = palR[cIdx]
					dst[di+1] = palG[cIdx]
					dst[di+2] = palB[cIdx]
					dst[di+3] = aPal[aIdx]
				}
			}
		}
	}
	return dst
}

// unpack565 expands an RGB565 colour to 8-bit per channel using the standard
// (val << 3 | val >> 2) and (val << 2 | val >> 4) bit-replication that DXT
// hardware uses, so our output matches what the GPU would render.
func unpack565(c uint16) (r, g, b uint8) {
	r5 := uint8((c >> 11) & 0x1f)
	g6 := uint8((c >> 5) & 0x3f)
	b5 := uint8(c & 0x1f)
	r = (r5 << 3) | (r5 >> 2)
	g = (g6 << 2) | (g6 >> 4)
	b = (b5 << 3) | (b5 >> 2)
	return
}

// Compile-time guard against unused-import drift if the file evolves.
var _ = bytes.NewBuffer
var _ = io.Discard
