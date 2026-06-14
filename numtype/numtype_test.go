package numtype

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rat(s string) *big.Rat {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		panic("bad rat: " + s)
	}
	return r
}

func TestParse_sixNames(t *testing.T) {
	cases := map[string]NumType{
		"rat":   {DRat, SAny},
		"rat0+": {DRat, SNonNeg},
		"rat0-": {DRat, SNonPos},
		"int":   {DInt, SAny},
		"int0+": {DInt, SNonNeg},
		"int0-": {DInt, SNonPos},
	}
	for name, want := range cases {
		got, ok := Parse(name)
		require.True(t, ok, "name %q", name)
		assert.Equal(t, want, got, "name %q", name)
		assert.Equal(t, name, got.String(), "round-trip %q", name)
	}
	_, ok := Parse("bool")
	assert.False(t, ok)
}

func TestZeroValueIsTopRat(t *testing.T) {
	// The zero value must be the widest type so an unset NumType reads as `rat`.
	assert.Equal(t, NumType{DRat, SAny}, NumType{})
}

func TestSub(t *testing.T) {
	r := NumType{DRat, SAny}
	rNN := NumType{DRat, SNonNeg}
	intNN := NumType{DInt, SNonNeg}
	zero := NumType{DInt, SZero}

	assert.True(t, Sub(intNN, r))     // int0+ <: rat (domain + sign widen)
	assert.True(t, Sub(intNN, rNN))   // int0+ <: rat0+
	assert.True(t, Sub(rNN, r))       // rat0+ <: rat
	assert.False(t, Sub(r, rNN))      // rat is NOT <: rat0+
	assert.False(t, Sub(rNN, intNN))  // rat0+ is NOT <: int0+ (domain)

	// The literal-0 type is below every numeric type, including non-positive ones.
	assert.True(t, Sub(zero, rNN))
	assert.True(t, Sub(zero, NumType{DRat, SNonPos}))
	assert.True(t, Sub(zero, NumType{DInt, SNonPos}))
}

func TestJoin(t *testing.T) {
	assert.Equal(t, NumType{DRat, SAny},
		Join(NumType{DRat, SNonNeg}, NumType{DRat, SNonPos})) // NonNeg ⊔ NonPos = Any
	assert.Equal(t, NumType{DRat, SNonNeg},
		Join(NumType{DInt, SZero}, NumType{DRat, SNonNeg})) // Zero ⊔ rat0+ = rat0+
	assert.Equal(t, NumType{DRat, SNonNeg},
		Join(NumType{DInt, SNonNeg}, NumType{DRat, SNonNeg})) // domain widens to rat
}

func TestAllows(t *testing.T) {
	assert.True(t, Allows(NumType{DRat, SNonNeg}, rat("0")))
	assert.True(t, Allows(NumType{DRat, SNonNeg}, rat("5/2")))
	assert.False(t, Allows(NumType{DRat, SNonNeg}, rat("-1")))
	assert.False(t, Allows(NumType{DInt, SAny}, rat("5/2"))) // non-integral
	assert.True(t, Allows(NumType{DInt, SAny}, rat("-3")))
	assert.False(t, Allows(NumType{DInt, SNonPos}, rat("1")))
	assert.True(t, Allows(NumType{DInt, SZero}, rat("0")))
	assert.False(t, Allows(NumType{DInt, SZero}, rat("1")))
}
