package gateway

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test 10: header parsing. A malformed/NaN/Inf/out-of-range ratio is a fast 400
// (error) — never silently accepted into a quorum predicate that would later
// time out as a 503.
func TestParseLinearize(t *testing.T) {
	cases := []struct {
		name      string
		linHeader string
		ratio     string
		wantErr   bool
		wantOn    bool
		wantRatio float64
	}{
		{name: "off by default", wantOn: false, wantRatio: 0.5},
		{name: "on, default ratio", linHeader: "true", wantOn: true, wantRatio: 0.5},
		{name: "on, explicit 0", linHeader: "true", ratio: "0", wantOn: true, wantRatio: 0},
		{name: "on, explicit 0.5", linHeader: "true", ratio: "0.5", wantOn: true, wantRatio: 0.5},
		{name: "on, explicit 1", linHeader: "true", ratio: "1", wantOn: true, wantRatio: 1},
		{name: "NaN rejected", linHeader: "true", ratio: "NaN", wantErr: true},
		{name: "Inf rejected", linHeader: "true", ratio: "Inf", wantErr: true},
		{name: "negative rejected", linHeader: "true", ratio: "-1", wantErr: true},
		{name: "above one rejected", linHeader: "true", ratio: "2", wantErr: true},
		{name: "non-numeric rejected", linHeader: "true", ratio: "abc", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			if tc.linHeader != "" {
				r.Header.Set("X-Gospr-Linearize", tc.linHeader)
			}
			if tc.ratio != "" {
				r.Header.Set("X-Gospr-Sync-Ratio", tc.ratio)
			}
			on, ratio, err := parseLinearize(r)
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
