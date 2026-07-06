package smartcrusher

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/iancoleman/orderedmap"
)

// f64p is a small helper for building *float64 fields in FieldStats.
func f64p(v float64) *float64 { return &v }

// jsonNum builds a json.Number, matching what decodeJSON emits for numeric
// literals (UseNumber). Field detection must coerce these via Float64().
func jsonNum(s string) json.Number { return json.Number(s) }

// objOf builds a single-key *orderedmap.OrderedMap {name: v}, matching the
// object shape decodeJSON produces for array elements.
func objOf(name string, v any) *orderedmap.OrderedMap {
	om := orderedmap.New()
	om.Set(name, v)
	return om
}

// closeEnough compares two confidences within the parity tolerance (1e-9). The
// confidences are fixed literal sums, so any real divergence exceeds 1e-9.
func closeEnough(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestDetectIDFieldStatistically(t *testing.T) {
	// uuidVals builds n distinct canonical UUIDs (differing only in the last
	// hex digit) so UniqueRatio can be set to 1.0 alongside a real UUID sample.
	uuidVals := func(n int) []any {
		out := make([]any, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, "550e8400-e29b-41d4-a716-4466554400"+string("0123456789abcdef"[i%16])+string("0123456789abcdef"[(i/16)%16]))
		}
		return out
	}

	cases := []struct {
		name     string
		stats    FieldStats
		values   []any
		wantIsID bool
		wantConf float64
	}{
		{
			// STEP1 hard gate: UniqueRatio < 0.9 -> (false, 0).
			name:     "hard_gate_low_unique_ratio",
			stats:    FieldStats{Name: "id", FieldType: "string", UniqueRatio: 0.5},
			values:   uuidVals(10),
			wantIsID: false,
			wantConf: 0.0,
		},
		{
			// STEP2 string UUID fraction > 0.8 -> (true, 0.95).
			name:     "string_uuid_fraction",
			stats:    FieldStats{Name: "id", FieldType: "string", UniqueRatio: 1.0},
			values:   uuidVals(10),
			wantIsID: true,
			wantConf: 0.95,
		},
		{
			// STEP2 string entropy: avg entropy > 0.7 AND uniq > 0.95 -> (true, 0.8).
			// High-entropy non-UUID random-ish tokens, all distinct.
			name:     "string_entropy",
			stats:    FieldStats{Name: "id", FieldType: "string", UniqueRatio: 1.0},
			values:   []any{"a1b2c3d4", "e5f6g7h8", "i9j0k1l2", "m3n4o5p6", "q7r8s9t0"},
			wantIsID: true,
			wantConf: 0.8,
		},
		{
			// STEP3 numeric sequential AND uniq > 0.95 -> (true, 0.9).
			name:     "numeric_sequential",
			stats:    FieldStats{Name: "id", FieldType: "numeric", UniqueRatio: 1.0, MinVal: f64p(1), MaxVal: f64p(5)},
			values:   []any{jsonNum("1"), jsonNum("2"), jsonNum("3"), jsonNum("4"), jsonNum("5")},
			wantIsID: true,
			wantConf: 0.9,
		},
		{
			// STEP3 numeric range > 0 AND uniq > 0.95 -> (true, 0.85). Range set,
			// but not sequential (avg diff far outside [0.5,2.0]).
			name:     "numeric_range",
			stats:    FieldStats{Name: "id", FieldType: "numeric", UniqueRatio: 1.0, MinVal: f64p(10), MaxVal: f64p(9000)},
			values:   []any{jsonNum("10"), jsonNum("500"), jsonNum("2000"), jsonNum("6000"), jsonNum("9000")},
			wantIsID: true,
			wantConf: 0.85,
		},
		{
			// STEP4 catch-all: UniqueRatio > 0.98 -> (true, 0.7). Not string/numeric
			// signal-bearing (field_type unrecognized), so it falls to the catch-all.
			name:     "catch_all",
			stats:    FieldStats{Name: "id", FieldType: "object", UniqueRatio: 0.99},
			values:   []any{},
			wantIsID: true,
			wantConf: 0.7,
		},
		{
			// STEP5 else: not UUID, not high-entropy, uniq in (0.9, 0.98] -> (false, 0).
			// Low-entropy short repeated-char strings; UniqueRatio just above gate but
			// at or below catch-all threshold.
			name:     "no_signal_false",
			stats:    FieldStats{Name: "id", FieldType: "string", UniqueRatio: 0.95},
			values:   []any{"aa", "bb", "cc", "dd", "ee"},
			wantIsID: false,
			wantConf: 0.0,
		},
		{
			// Empty string sample -> skip UUID/entropy, fall through to catch-all if
			// uniq > 0.98, else (false, 0). Here uniq is exactly 0.95 -> false.
			name:     "empty_sample_no_catch_all",
			stats:    FieldStats{Name: "id", FieldType: "string", UniqueRatio: 0.95},
			values:   []any{},
			wantIsID: false,
			wantConf: 0.0,
		},
		{
			// take(20) BEFORE is-string filter: a non-string in the first 20 consumes
			// a slot. Here 9 UUIDs + 1 non-string => 9 strings, all UUID -> 9/9 > 0.8.
			name:     "sample_take20_before_filter",
			stats:    FieldStats{Name: "id", FieldType: "string", UniqueRatio: 1.0},
			values:   append(append([]any{}, uuidVals(9)...), jsonNum("1")),
			wantIsID: true,
			wantConf: 0.95,
		},
		{
			// range branch requires range > 0: min==max (zero width) -> not range;
			// not sequential; uniq 0.96 (<=0.98) -> (false, 0).
			name:     "numeric_zero_width_range",
			stats:    FieldStats{Name: "id", FieldType: "numeric", UniqueRatio: 0.96, MinVal: f64p(7), MaxVal: f64p(7)},
			values:   []any{jsonNum("7"), jsonNum("7"), jsonNum("7"), jsonNum("7"), jsonNum("7")},
			wantIsID: false,
			wantConf: 0.0,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotIsID, gotConf := DetectIDFieldStatistically(c.stats, c.values)
			if gotIsID != c.wantIsID {
				t.Errorf("isID = %v, want %v", gotIsID, c.wantIsID)
			}
			if !closeEnough(gotConf, c.wantConf) {
				t.Errorf("confidence = %v, want %v", gotConf, c.wantConf)
			}
		})
	}
}

func TestDetectScoreFieldStatistically(t *testing.T) {
	// scoreItems builds a []any of objects {name: v} from the given values.
	scoreItems := func(name string, vals ...any) []any {
		out := make([]any, 0, len(vals))
		for _, v := range vals {
			out = append(out, objOf(name, v))
		}
		return out
	}
	num := func(vals ...string) []any {
		out := make([]any, 0, len(vals))
		for _, s := range vals {
			out = append(out, jsonNum(s))
		}
		return out
	}

	cases := []struct {
		name        string
		stats       FieldStats
		items       []any
		wantIsScore bool
		wantConf    float64
	}{
		{
			// STEP1 non-numeric field_type -> (false, 0).
			name:        "non_numeric",
			stats:       FieldStats{Name: "score", FieldType: "string", MinVal: f64p(0), MaxVal: f64p(1)},
			items:       scoreItems("score", "a", "b"),
			wantIsScore: false,
			wantConf:    0.0,
		},
		{
			// STEP2 min unset -> (false, 0).
			name:        "min_unset",
			stats:       FieldStats{Name: "score", FieldType: "numeric", MaxVal: f64p(1)},
			items:       scoreItems("score", num("0.5")[0]),
			wantIsScore: false,
			wantConf:    0.0,
		},
		{
			// [0,1] range +0.4 -> conf 0.4 >= 0.4 -> isScore true. Integer 0/1
			// values isolate the range bonus: no float bonus (all integers), no
			// descending bonus (0,1,0,1,0 is 50% non-ascending), not sequential.
			name:        "range_0_1",
			stats:       FieldStats{Name: "score", FieldType: "numeric", MinVal: f64p(0.0), MaxVal: f64p(1.0)},
			items:       scoreItems("score", num("0", "1", "0", "1", "0")...),
			wantIsScore: true,
			wantConf:    0.4,
		},
		{
			// [0,10] range +0.3 -> conf 0.3 < 0.4 -> isScore false, conf 0.3 returned.
			// (No descending, no float bonus: ascending ints.)
			name:        "range_0_10",
			stats:       FieldStats{Name: "score", FieldType: "numeric", MinVal: f64p(0.0), MaxVal: f64p(10.0)},
			items:       scoreItems("score", num("0", "2", "5", "8", "10")...),
			wantIsScore: false,
			wantConf:    0.3,
		},
		{
			// [0,100] range +0.25 -> conf 0.25.
			name:        "range_0_100",
			stats:       FieldStats{Name: "score", FieldType: "numeric", MinVal: f64p(0.0), MaxVal: f64p(100.0)},
			items:       scoreItems("score", num("5", "40", "60", "80", "100")...),
			wantIsScore: false,
			wantConf:    0.25,
		},
		{
			// Asymmetric [-1,1]: min>=-1 AND max<=1 -> +0.35. Not [0,1] because
			// min<0. Integer -1/1 values isolate the range bonus (no float, no
			// descending, not sequential). 0.35 < 0.4 -> isScore false.
			name:        "range_asymmetric_neg1_1",
			stats:       FieldStats{Name: "score", FieldType: "numeric", MinVal: f64p(-1.0), MaxVal: f64p(1.0)},
			items:       scoreItems("score", num("-1", "1", "-1", "1", "-1")...),
			wantIsScore: false,
			wantConf:    0.35,
		},
		{
			// Not bounded (min < -1) -> (false, 0).
			name:        "not_bounded",
			stats:       FieldStats{Name: "score", FieldType: "numeric", MinVal: f64p(-5.0), MaxVal: f64p(5.0)},
			items:       scoreItems("score", num("-5", "0", "5", "-2", "2")...),
			wantIsScore: false,
			wantConf:    0.0,
		},
		{
			// Sequential rejection: bounded [0,10] but values form a sequence -> (false, 0).
			name:        "sequential_rejection",
			stats:       FieldStats{Name: "score", FieldType: "numeric", MinVal: f64p(0.0), MaxVal: f64p(10.0)},
			items:       scoreItems("score", num("1", "2", "3", "4", "5")...),
			wantIsScore: false,
			wantConf:    0.0,
		},
		{
			// Descending bonus: [0,1] +0.4, non-ascending fraction >0.7 +0.3 -> 0.7.
			// Integer 1,0,0,0,0 isolates the descending bonus (no float bonus; not
			// sequential: sorted diffs avg 0.25 outside [0.5,2.0]).
			name:        "descending_bonus",
			stats:       FieldStats{Name: "score", FieldType: "numeric", MinVal: f64p(0.0), MaxVal: f64p(1.0)},
			items:       scoreItems("score", num("1", "0", "0", "0", "0")...),
			wantIsScore: true,
			wantConf:    0.7,
		},
		{
			// Float-fraction bonus: [0,1] +0.4, all floats (>30%) +0.1 -> 0.5.
			// Non-monotonic so no descending bonus; not sequential.
			name:        "float_fraction_bonus",
			stats:       FieldStats{Name: "score", FieldType: "numeric", MinVal: f64p(0.0), MaxVal: f64p(1.0)},
			items:       scoreItems("score", num("0.5", "0.9", "0.1", "0.7", "0.3")...),
			wantIsScore: true,
			wantConf:    0.5,
		},
		{
			// Cap at 0.95: [0,1] +0.4, descending +0.3, floats +0.1 -> 0.8 (< cap;
			// verifies cap does not clamp legitimate sums under 0.95).
			name:        "under_cap",
			stats:       FieldStats{Name: "score", FieldType: "numeric", MinVal: f64p(0.0), MaxVal: f64p(1.0)},
			items:       scoreItems("score", num("0.91", "0.72", "0.53", "0.34", "0.15")...),
			wantIsScore: true,
			wantConf:    0.8,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotIsScore, gotConf := DetectScoreFieldStatistically(c.stats, c.items)
			if gotIsScore != c.wantIsScore {
				t.Errorf("isScore = %v, want %v", gotIsScore, c.wantIsScore)
			}
			if !closeEnough(gotConf, c.wantConf) {
				t.Errorf("confidence = %v, want %v", gotConf, c.wantConf)
			}
		})
	}
}
