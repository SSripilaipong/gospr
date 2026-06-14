package numtype

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParse_sixNames(t *testing.T) {
	cases := map[string]NumType{
		"real":   {DReal, SAny},
		"real0+": {DReal, SNonNeg},
		"real0-": {DReal, SNonPos},
		"int":    {DInt, SAny},
		"int0+":  {DInt, SNonNeg},
		"int0-":  {DInt, SNonPos},
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

func TestZeroValueIsTopReal(t *testing.T) {
	// The zero value must be the widest type so an unset NumType reads as `real`.
	assert.Equal(t, NumType{DReal, SAny}, NumType{})
}

func TestSub(t *testing.T) {
	real := NumType{DReal, SAny}
	realNN := NumType{DReal, SNonNeg}
	intNN := NumType{DInt, SNonNeg}
	zero := NumType{DInt, SZero}

	assert.True(t, Sub(intNN, real))    // int0+ <: real (domain + sign widen)
	assert.True(t, Sub(intNN, realNN))  // int0+ <: real0+
	assert.True(t, Sub(realNN, real))   // real0+ <: real
	assert.False(t, Sub(real, realNN))  // real is NOT <: real0+
	assert.False(t, Sub(realNN, intNN)) // real0+ is NOT <: int0+ (domain)

	// The literal-0 type is below every numeric type, including non-positive ones.
	assert.True(t, Sub(zero, realNN))
	assert.True(t, Sub(zero, NumType{DReal, SNonPos}))
	assert.True(t, Sub(zero, NumType{DInt, SNonPos}))
}

func TestJoin(t *testing.T) {
	assert.Equal(t, NumType{DReal, SAny},
		Join(NumType{DReal, SNonNeg}, NumType{DReal, SNonPos})) // NonNeg ⊔ NonPos = Any
	assert.Equal(t, NumType{DReal, SNonNeg},
		Join(NumType{DInt, SZero}, NumType{DReal, SNonNeg})) // Zero ⊔ real0+ = real0+
	assert.Equal(t, NumType{DReal, SNonNeg},
		Join(NumType{DInt, SNonNeg}, NumType{DReal, SNonNeg})) // domain widens to real
}

func TestAllows(t *testing.T) {
	assert.True(t, Allows(NumType{DReal, SNonNeg}, 0))
	assert.True(t, Allows(NumType{DReal, SNonNeg}, 2.5))
	assert.False(t, Allows(NumType{DReal, SNonNeg}, -1))
	assert.False(t, Allows(NumType{DInt, SAny}, 2.5)) // non-integral
	assert.True(t, Allows(NumType{DInt, SAny}, -3))
	assert.False(t, Allows(NumType{DInt, SNonPos}, 1))
	assert.True(t, Allows(NumType{DInt, SZero}, 0))
	assert.False(t, Allows(NumType{DInt, SZero}, 1))
}
