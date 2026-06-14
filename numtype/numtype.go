// Package numtype is the shared numeric subtype lattice for the DSL. It is a
// leaf package (it imports nothing from the project) so both builder (semantic
// type-checking) and crdt (runtime value validation) can depend on it without a
// cycle — builder imports crdt, so the lattice can live in neither of them.
//
// A numeric type has two independent axes:
//
//   - Domain: Int <: Real.
//   - Sign:   Zero <: NonNeg <: Any and Zero <: NonPos <: Any
//     (NonNeg and NonPos are incomparable).
//
// The six user-spellable types pair {Real,Int} with {Any,NonNeg,NonPos}:
// real, real0+, real0-, int, int0+, int0-. The Zero sign is internal (not
// user-spellable): it is the type of the literal 0, so that 0 is assignable to
// both non-negative and non-positive targets.
//
// The zero value of NumType is {Real, Any} == the top type `real`, so an unset
// NumType reads as the most permissive numeric type.
package numtype

import "math"

type Domain int

const (
	DReal Domain = iota // zero value: the widest domain
	DInt
)

type Sign int

const (
	SAny    Sign = iota // zero value: the widest sign
	SNonNeg             // >= 0
	SNonPos             // <= 0
	SZero               // exactly 0 (internal; the type of the literal 0)
)

// NumType is a numeric type: a domain paired with a sign.
type NumType struct {
	Domain Domain
	Sign   Sign
}

// Parse maps one of the six user-spellable type names to a NumType. It never
// yields the internal Zero sign.
func Parse(name string) (NumType, bool) {
	switch name {
	case "real":
		return NumType{DReal, SAny}, true
	case "real0+":
		return NumType{DReal, SNonNeg}, true
	case "real0-":
		return NumType{DReal, SNonPos}, true
	case "int":
		return NumType{DInt, SAny}, true
	case "int0+":
		return NumType{DInt, SNonNeg}, true
	case "int0-":
		return NumType{DInt, SNonPos}, true
	default:
		return NumType{}, false
	}
}

func (t NumType) String() string {
	d := "real"
	if t.Domain == DInt {
		d = "int"
	}
	switch t.Sign {
	case SNonNeg:
		return d + "0+"
	case SNonPos:
		return d + "0-"
	case SZero:
		return d + "0" // internal Zero; only appears in diagnostics
	default:
		return d
	}
}

func subDomain(a, b Domain) bool { return a == b || (a == DInt && b == DReal) }

func subSign(a, b Sign) bool {
	if a == b {
		return true
	}
	switch b {
	case SAny:
		return true // everything <: Any
	case SNonNeg, SNonPos:
		return a == SZero // only Zero is below the bounded signs
	default: // SZero is minimal
		return false
	}
}

// Sub reports whether a is a subtype of b (a is assignable where b is wanted).
func Sub(a, b NumType) bool { return subDomain(a.Domain, b.Domain) && subSign(a.Sign, b.Sign) }

func joinDomain(a, b Domain) Domain {
	if a == DReal || b == DReal {
		return DReal
	}
	return DInt
}

func joinSign(a, b Sign) Sign {
	switch {
	case a == b:
		return a
	case a == SZero:
		return b
	case b == SZero:
		return a
	default: // two distinct non-zero signs among {NonNeg, NonPos, Any}
		return SAny
	}
}

// Join is the least upper bound of a and b on both axes.
func Join(a, b NumType) NumType {
	return NumType{joinDomain(a.Domain, b.Domain), joinSign(a.Sign, b.Sign)}
}

// Allows reports whether the runtime value v is a member of type t: integral
// when the domain is Int, and within the sign's range.
func Allows(t NumType, v float64) bool {
	if t.Domain == DInt && v != math.Trunc(v) {
		return false
	}
	switch t.Sign {
	case SNonNeg:
		return v >= 0
	case SNonPos:
		return v <= 0
	case SZero:
		return v == 0
	default: // SAny
		return true
	}
}
