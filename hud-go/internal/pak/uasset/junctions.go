// junctions.go — extract NetworkTurnoutJunction exports from TT tiles to
// produce an authoritative junction lookup table.
//
// Why: at a junction node, multiple ribbons share a node guid. The walker
// can't tell from node-membership alone which ribbons are legitimate chain
// partners (engine's "this switch goes A→B or A→C") and which are
// unrelated parallel tracks that just happen to share the node.
//
// NetworkTurnoutJunction has the answer in three Connection structs:
//
//   IngoingRibbonConnection  : the ribbon entering the switch
//   OutgoingRibbonConnection : the ribbon exiting on the main route
//   TurnoutRibbonConnection  : the ribbon exiting on the diverging route
//
// The walker can then enforce: at a node carrying a junction, valid chain
// partners are EXACTLY these three (or a subset, depending on travel
// direction). Any other ribbon attached to the same node is a different
// physical track and must NOT be chained.
package uasset

import "strings"

// JunctionConnections is the three-ribbon set defining one switch.
//
// All GUIDs are normalised lowercase 32-char hex.
type JunctionConnections struct {
	NodeGUID    string
	Ingoing     string
	Outgoing    string
	Turnout     string
	// "Start" booleans tell us which END of each connected ribbon meets
	// the switch — so we can tell if the ribbon is *leaving* the junction
	// (Start=true: the junction sits at ribbon's start endpoint) or
	// *entering* (Start=false: junction at ribbon's end endpoint).
	IngoingStart bool
	OutgoingStart bool
	TurnoutStart  bool
}

// ParseTurnoutJunctionsFromUmap walks every export of a TT tile and returns
// every NetworkTurnoutJunction's connection triplet, indexed by NodeGUID.
//
// One node can host at most one switch (a switch is the pairing of three
// ribbons at a single node), so NodeGUID is a unique key.
func ParseTurnoutJunctionsFromUmap(uassetPath string) ([]JunctionConnections, error) {
	u, err := ReadUmap(uassetPath)
	if err != nil {
		return nil, err
	}
	var out []JunctionConnections
	for _, e := range u.Exports {
		if !isTurnoutJunction(e.ObjectName) {
			continue
		}
		pr := u.PropertyReader(e)
		if pr == nil {
			continue
		}
		j, ok := walkTurnoutJunction(pr)
		if !ok {
			continue
		}
		out = append(out, j)
	}
	return out, nil
}

func isTurnoutJunction(name string) bool {
	if strings.HasPrefix(name, "Default__") {
		return false
	}
	return strings.HasPrefix(name, "NetworkTurnoutJunction")
}

// walkTurnoutJunction parses one NetworkTurnoutJunction export and pulls
// the three Connection refs + NodeGuid. Returns ok=false if any of the
// required ribbon connections is missing — better to skip than to produce
// a junction record with empty fields.
func walkTurnoutJunction(r *reader) (JunctionConnections, bool) {
	var j JunctionConnections
	for r.remaining() > 8 {
		t, ok := r.readTag()
		if !ok {
			break
		}
		dp := r.p
		switch {
		case t.name == "IngoingRibbonConnection" && t.ptype == "StructProperty" &&
			t.structType == "Connection":
			j.Ingoing, j.IngoingStart = readConnection(r, dp, t.size)
		case t.name == "OutgoingRibbonConnection" && t.ptype == "StructProperty" &&
			t.structType == "Connection":
			j.Outgoing, j.OutgoingStart = readConnection(r, dp, t.size)
		case t.name == "TurnoutRibbonConnection" && t.ptype == "StructProperty" &&
			t.structType == "Connection":
			j.Turnout, j.TurnoutStart = readConnection(r, dp, t.size)
		case t.name == "NodeGuid" && t.ptype == "StructProperty" &&
			t.structType == "Guid":
			var raw [16]byte
			copy(raw[:], r.d[dp:dp+16])
			j.NodeGUID = NormalizeGUID(fmtGUID(raw))
		}
		r.seek(dp + t.size)
	}
	if j.Ingoing == "" || j.Outgoing == "" || j.Turnout == "" || j.NodeGUID == "" {
		return j, false
	}
	return j, true
}

// readConnection parses one Connection struct body — { RibbonGuid, Start }.
// Returns the ribbon's normalised guid and the Start bool. If either is
// missing, returns the zero value (caller decides validity).
func readConnection(r *reader, dp, size int) (string, bool) {
	end := dp + size
	var guid string
	var start bool
	for r.p < end {
		t, ok := r.readTag()
		if !ok {
			break
		}
		tdp := r.p
		switch {
		case t.name == "RibbonGuid" && t.ptype == "StructProperty" &&
			t.structType == "Guid":
			var raw [16]byte
			copy(raw[:], r.d[tdp:tdp+16])
			guid = NormalizeGUID(fmtGUID(raw))
		case t.name == "Start" && t.ptype == "BoolProperty":
			start = t.boolVal != 0
		}
		r.seek(tdp + t.size)
	}
	r.seek(end)
	return guid, start
}
