package main

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
)

var payload []byte
var nm []string

func fname(p *int) string {
	idx := int(int32(binary.LittleEndian.Uint32(payload[*p:])))
	num := int(int32(binary.LittleEndian.Uint32(payload[*p+4:])))
	*p += 8
	if idx < 0 || idx >= len(nm) {
		return fmt.Sprintf("?%d", idx)
	}
	s := nm[idx]
	if num > 0 {
		return fmt.Sprintf("%s_%d", s, num-1)
	}
	return s
}

func u8(p *int) byte     { v := payload[*p]; *p++; return v }
func i32(p *int) int32   { v := int32(binary.LittleEndian.Uint32(payload[*p:])); *p += 4; return v }
func i64(p *int) int64   { v := int64(binary.LittleEndian.Uint64(payload[*p:])); *p += 8; return v }
func f32(p *int) float32 { bits := binary.LittleEndian.Uint32(payload[*p:]); *p += 4; return math.Float32frombits(bits) }
func f64(p *int) float64 { bits := binary.LittleEndian.Uint64(payload[*p:]); *p += 8; return math.Float64frombits(bits) }
func skip(p *int, n int) { *p += n }

func fstr(p *int) string {
	n := int(i32(p))
	if n == 0 {
		return ""
	}
	if n > 0 {
		s := string(payload[*p : *p+n-1])
		*p += n
		return s
	}
	n = -n
	b := payload[*p : *p+n*2]
	*p += n * 2
	runes := make([]rune, 0, n-1)
	for i := 0; i < (n-1)*2; i += 2 {
		runes = append(runes, rune(binary.LittleEndian.Uint16(b[i:])))
	}
	return string(runes)
}

type tag struct {
	name       string
	ptype      string
	structType string
	innerType  string
	size       int
	boolVal    byte
	start      int
	dataStart  int
}

func readTag(p *int) (tag, bool) {
	start := *p
	name := fname(p)
	if name == "None" {
		return tag{}, false
	}
	ptype := fname(p)
	size := int(i32(p))
	i32(p) // arr_idx

	var t tag
	t.name = name
	t.ptype = ptype
	t.size = size
	t.start = start

	switch ptype {
	case "StructProperty":
		t.structType = fname(p)
		skip(p, 16) // guid
	case "BoolProperty":
		t.boolVal = u8(p)
	case "ByteProperty", "EnumProperty":
		t.innerType = fname(p)
	case "ArrayProperty":
		t.innerType = fname(p)
	case "MapProperty":
		fname(p)
		fname(p)
	case "SetProperty":
		fname(p)
	}
	if u8(p) != 0 { // has_guid
		skip(p, 16)
	}
	t.dataStart = *p
	return t, true
}

// dumpArrayStruct walks an ArrayProperty<StructProperty> payload and
// prints each element as a nested property tree.
func dumpArrayStruct(p *int, limit int, depth int, maxDepth int) {
	indent := ""
	for i := 0; i < depth; i++ {
		indent += "  "
	}
	if *p >= limit {
		return
	}
	count := int(i32(p))
	fmt.Printf("%s[array count=%d]\n", indent, count)
	if count == 0 {
		*p = limit
		return
	}
	innerTag, ok := readTag(p)
	if !ok {
		*p = limit
		return
	}
	fmt.Printf("%s[inner tag=%s size=%d]\n", indent, innerTag.name, innerTag.size)
	arrEnd := *p + innerTag.size
	for i := 0; i < count && *p < arrEnd; i++ {
		fmt.Printf("%s-- element %d @%d --\n", indent, i, *p)
		elStart := *p
		dumpProps(p, arrEnd, depth+1, maxDepth)
		// Advance past None sentinel if dumpProps didn't already
		*p = elStart
		for *p < arrEnd {
			t, ok := readTag(p)
			if !ok {
				break
			}
			*p += t.size
		}
		if i >= 2 {
			fmt.Printf("%s(stopping after 3 elements)\n", indent)
			break
		}
	}
	*p = limit
}

// dumpProps walks tagged properties and prints the tree.
// maxDepth: how many levels of nested structs to recurse into.
func dumpProps(p *int, limit int, depth int, maxDepth int) {
	indent := ""
	for i := 0; i < depth; i++ {
		indent += "  "
	}
	for *p < limit {
		t, ok := readTag(p)
		if !ok {
			break
		}
		dp := *p

		extra := ""
		switch t.ptype {
		case "StructProperty":
			extra = fmt.Sprintf(" struct=%s", t.structType)
		case "BoolProperty":
			extra = fmt.Sprintf(" val=%d", t.boolVal)
		case "ByteProperty", "EnumProperty":
			extra = fmt.Sprintf(" inner=%s", t.innerType)
		case "ArrayProperty":
			extra = fmt.Sprintf(" inner=%s", t.innerType)
		}

		// Show first bytes of data
		dataHex := ""
		end := dp + t.size
		if end > len(payload) {
			end = len(payload)
		}
		show := end - dp
		if show > 32 {
			show = 32
		}
		for i := 0; i < show; i++ {
			dataHex += fmt.Sprintf("%02x ", payload[dp+i])
		}
		if end-dp > 32 {
			dataHex += "..."
		}

		fmt.Printf("%s[pos=%d] %s (%s) size=%d%s | %s\n",
			indent, t.start, t.name, t.ptype, t.size, extra, dataHex)

		if t.ptype == "StructProperty" && depth < maxDepth && t.size > 0 {
			nested := dp
			dumpProps(&nested, dp+t.size, depth+1, maxDepth)
		} else if t.ptype == "NameProperty" && t.size >= 8 {
			nested := dp
			v := fname(&nested)
			fmt.Printf("%s  => %q\n", indent, v)
		} else if t.ptype == "EnumProperty" && t.size >= 8 {
			nested := dp
			v := fname(&nested)
			fmt.Printf("%s  => %q\n", indent, v)
		} else if t.ptype == "FloatProperty" && t.size == 4 {
			nested := dp
			fmt.Printf("%s  => %f\n", indent, f32(&nested))
		} else if t.ptype == "DoubleProperty" && t.size == 8 {
			nested := dp
			fmt.Printf("%s  => %f\n", indent, f64(&nested))
		}

		*p = dp + t.size
	}
}

func main() {
	// Find the JSON file in the temp dir
	jsonPath := findJSON()
	if jsonPath == "" {
		fmt.Fprintln(os.Stderr, "Could not find IOW_Timetable.uasset.json in temp dirs")
		os.Exit(1)
	}
	fmt.Println("Using:", jsonPath)

	raw, _ := os.ReadFile(jsonPath)
	var doc struct {
		NameMap []string `json:"NameMap"`
		Exports []struct {
			Data any `json:"Data"`
		} `json:"Exports"`
	}
	json.Unmarshal(raw, &doc)
	nm = doc.NameMap

	b64, _ := doc.Exports[0].Data.(string)
	payload, _ = base64.StdEncoding.DecodeString(b64)
	fmt.Printf("Payload size: %d bytes\n", len(payload))

	// Walk to find Formations and dump first few formation entries
	p := 0
	for p < len(payload)-8 {
		t, ok := readTag(&p)
		if !ok {
			break
		}
		if t.name == "Formations" && t.ptype == "ArrayProperty" {
			parseFormations(&p, t.size)
			return
		}
		p = t.dataStart + t.size
	}
	fmt.Println("Formations not found")
}

func parseFormations(p *int, outerSize int) {
	end := *p + outerSize
	count := int(i32(p))
	fmt.Printf("Formations count: %d\n", count)

	innerTag, ok := readTag(p)
	if !ok {
		return
	}
	fmt.Printf("Inner tag: %s size=%d\n", innerTag.name, innerTag.size)
	arrEnd := *p + innerTag.size

	for i := 0; i < count && *p < arrEnd && i < 3; i++ {
		fmt.Printf("\n=== Formation %d (pos=%d) ===\n", i, *p)
		fStart := *p
		dumpProps(p, arrEnd, 1, 6)
		// Re-scan to None sentinel
		*p = fStart
		for *p < arrEnd {
			t, ok := readTag(p)
			if !ok {
				break
			}
			*p += t.size
		}
	}
	*p = end
}

func parseServices(p *int, outerSize int) {
	end := *p + outerSize
	count := int(i32(p))
	fmt.Printf("Services count: %d\n", count)

	innerTag, ok := readTag(p)
	if !ok {
		return
	}
	fmt.Printf("Inner tag: %s size=%d\n", innerTag.name, innerTag.size)
	arrEnd := *p + innerTag.size

	// Parse only first 3 services, dump their first Destination
	for i := 0; i < count && *p < arrEnd && i < 3; i++ {
		parseOneService(p, arrEnd, i)
	}
	*p = end
}

func parseOneService(p *int, limit int, idx int) {
	fmt.Printf("\n=== Service %d (pos=%d) ===\n", idx, *p)
	start := *p
	serviceName := ""
	for *p < limit {
		t, ok := readTag(p)
		if !ok {
			break
		}
		dp := *p
		switch {
		case t.name == "Name" && t.ptype == "NameProperty":
			serviceName = fname(p)
			fmt.Printf("  Name: %q\n", serviceName)
		case t.name == "Instructions" && t.ptype == "ArrayProperty":
			fmt.Printf("  [Instructions array at pos=%d size=%d]\n", dp-t.size, t.size)
			parseInstructions(p, dp, t.size, serviceName)
		}
		*p = dp + t.size
	}
	// Re-scan to None sentinel
	*p = start
	for *p < limit {
		t, ok := readTag(p)
		if !ok {
			break
		}
		*p += t.size
	}
}

func parseInstructions(p *int, dp, outerSize int, svcName string) {
	end := dp + outerSize
	count := int(i32(p))
	fmt.Printf("    Instruction count: %d\n", count)
	if count == 0 {
		*p = end
		return
	}
	innerTag, ok := readTag(p)
	if !ok {
		*p = end
		return
	}
	arrEnd := *p + innerTag.size

	// Dump first 5 instructions' Destination structs
	for i := 0; i < count && *p < arrEnd; i++ {
		fmt.Printf("\n    --- Instruction %d (pos=%d) ---\n", i, *p)
		instrStart := *p
		for *p < arrEnd {
			t, ok := readTag(p)
			if !ok {
				break
			}
			idp := *p
			if t.name == "InstructionType" && t.ptype == "EnumProperty" {
				v := fname(p)
				fmt.Printf("      InstructionType: %q\n", v)
			} else if t.name == "Destination" && t.ptype == "StructProperty" {
				fmt.Printf("      Destination (struct=%s size=%d):\n", t.structType, t.size)
				nested := idp
				dumpProps(&nested, idp+t.size, 4, 8)
			} else if t.name == "LoadingCriteria" && t.ptype == "ArrayProperty" {
				fmt.Printf("      LoadingCriteria (ArrayProperty inner=%s size=%d):\n", t.innerType, t.size)
				nested := idp
				dumpArrayStruct(&nested, idp+t.size, 4, 8)
			}
			*p = idp + t.size
		}
		// Re-scan to None
		*p = instrStart
		for *p < arrEnd {
			t, ok := readTag(p)
			if !ok {
				break
			}
			*p += t.size
		}
		if i >= 4 {
			fmt.Println("    (stopping after 5 instructions)")
			break
		}
	}
	*p = end
}

func findJSON() string {
	// Check temp dirs
	tmp := os.TempDir()
	entries, err := os.ReadDir(tmp)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if len(name) < 15 {
			continue
		}
		candidate := tmp + "/" + name + "/IsleOfWight/TS2Prototype/Plugins/DLC/IsleOfWight_Route_Gameplay/Content/Timetable/IOW_Timetable.uasset.json"
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}
