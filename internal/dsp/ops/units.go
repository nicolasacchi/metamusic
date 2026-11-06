package ops

import "encoding/json"

// selfFinishing are the kinds that run safety.Finish themselves, with their own target and ceiling, and carry the "safety"
// object in their report (master, widen: H1; binaural: H2). The dispatcher does not finish them again, and their registered
// units already include SafetyUnits.
var selfFinishing = map[string]bool{KindMaster: true, KindWiden: true, "binaural": true}

// ownFades are the kinds whose render brings its own edges: safety skips its 10 ms fades for them (D-T1-14.3). Glacier fades; a
// shepard is a seamless loop, and a fade at both ends would notch every wrap.
var ownFades = map[string]bool{KindGlacier: true, KindShepard: true}

// SelfFinishing reports whether the kind runs safety.Finish itself.
func SelfFinishing(kind string) bool { return selfFinishing[kind] }

// Units is the work-unit model of a whole render of the kind: the registered model of the operator plus, for the kinds that do not
// finish themselves, SafetyUnits of the output (about 700 per frame, measured by H1). The work meter and the cost model share
// it. ok is false for an unregistered kind.
func Units(kind string, inFrames, outFrames int64, rate int, params json.RawMessage) (units int64, ok bool) {
	_, fn, found := Lookup(kind)
	if !found {
		return 0, false
	}
	u := fn(inFrames, outFrames, rate, params)
	if !selfFinishing[kind] {
		u += SafetyUnits(outFrames)
	}
	return u, true
}
