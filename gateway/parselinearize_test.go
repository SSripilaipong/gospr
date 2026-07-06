package gateway

import (
	"net/http/httptest"
	"testing"

	"gospr/node"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test 10: presence-based header parsing. An absent X-Gospr-Sync-Ratio is async
// (off); any present value turns sync on. A present-but-empty value, a numeric
// value >= 1 (use "all"), and malformed/NaN/Inf/out-of-range ratios are all a
// fast 400 — never silently accepted into a quorum predicate that would later
// time out as a 503, and never a silent fall-back to async.
func TestParseSync(t *testing.T) {
	strp := func(s string) *string { return &s }
	cases := []struct {
		name      string
		ratio     *string // nil = header absent; non-nil = header set to this value (incl. "")
		wantErr   bool
		wantOn    bool
		wantRatio float64
	}{
		{name: "absent ⇒ async off", ratio: nil, wantOn: false},
		{name: "explicit 0", ratio: strp("0"), wantOn: true, wantRatio: 0},
		{name: "explicit 0.5 (majority)", ratio: strp("0.5"), wantOn: true, wantRatio: 0.5},
		{name: "explicit 0.9", ratio: strp("0.9"), wantOn: true, wantRatio: 0.9},
		{name: "all keyword", ratio: strp("all"), wantOn: true, wantRatio: node.AllRatio},
		{name: "ALL case-insensitive", ratio: strp("ALL"), wantOn: true, wantRatio: node.AllRatio},
		{name: "padded value trimmed", ratio: strp("  0.5  "), wantOn: true, wantRatio: 0.5},
		{name: "present but empty rejected", ratio: strp(""), wantErr: true},
		{name: "whitespace-only rejected", ratio: strp("   "), wantErr: true},
		{name: "numeric 1 rejected (use all)", ratio: strp("1"), wantErr: true},
		{name: "numeric 1.0 rejected (use all)", ratio: strp("1.0"), wantErr: true},
		{name: "NaN rejected", ratio: strp("NaN"), wantErr: true},
		{name: "Inf rejected", ratio: strp("Inf"), wantErr: true},
		{name: "negative rejected", ratio: strp("-1"), wantErr: true},
		{name: "above one rejected", ratio: strp("2"), wantErr: true},
		{name: "non-numeric rejected", ratio: strp("abc"), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			if tc.ratio != nil {
				r.Header.Set("X-Gospr-Sync-Ratio", *tc.ratio)
			}
			on, ratio, err := parseSync(r)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantOn, on)
			assert.Equal(t, tc.wantRatio, ratio)
		})
	}
}
