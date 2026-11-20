package abc

// Dialect selects the accidental rule.
type Dialect string

const (
	// DialectYuE2 carries an accidental to every octave of the same pitch class until the bar line (YuE2's native rule).
	DialectYuE2 Dialect = "yue2"
	// DialectStandard follows ABC 2.1: an accidental applies to the same pitch in the same octave until the bar line.
	DialectStandard Dialect = "standard"
)

// Options configures Parse.
type Options struct {
	Dialect    Dialect
	DefaultBPM float64 // quarter notes per minute when the tune has no usable Q:; 120 when zero
}

func (o Options) dialect() Dialect {
	if o.Dialect == "" {
		return DialectStandard
	}
	return o.Dialect
}

func (o Options) bpm() float64 {
	if o.DefaultBPM <= 0 {
		return 120
	}
	return o.DefaultBPM
}

// accKey identifies a written pitch for the bar's accidental memory: octave is always 0 in the yue2 dialect.
type accKey struct {
	letter byte // 'A'..'G'
	octave int
}

func (d Dialect) key(letter byte, octave int) accKey {
	if d == DialectYuE2 {
		return accKey{letter, 0}
	}
	return accKey{letter, octave}
}
