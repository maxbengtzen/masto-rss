package js_test

import (
	"testing"

	"github.com/maxbengtzen/masto-rss/internal/js"
	"github.com/maxbengtzen/masto-rss/internal/testutil"
)

// Date.parse must return the same time as in Node for every date in the golden file, parsed in
// Europe/Stockholm.
func TestParseDateGolden(t *testing.T) {
	testutil.Stockholm(t)
	var golden struct {
		Dates []struct {
			Text string
			Ms   *int64
		}
	}
	testutil.Golden(t, "text.json", &golden)
	for _, c := range golden.Dates {
		ms, ok := js.ParseDate(c.Text)
		switch {
		case c.Ms == nil && ok:
			t.Errorf("%q: got %d, want NaN", c.Text, ms)
		case c.Ms != nil && !ok:
			t.Errorf("%q: got NaN, want %d", c.Text, *c.Ms)
		case c.Ms != nil && ms != *c.Ms:
			t.Errorf("%q: got %d, want %d (%+d s)", c.Text, ms, *c.Ms, (ms-*c.Ms)/1000)
		}
	}
}

func TestFormatNumber(t *testing.T) {
	for in, want := range map[float64]string{0: "0", 2: "2", 1.5: "1.5", 1.5e21: "1.5e+21", 1e-7: "1e-7", 123456789012: "123456789012", -3: "-3"} {
		if got := js.FormatNumber(in); got != want {
			t.Errorf("FormatNumber(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestTrimAndSlice(t *testing.T) {
	if got := js.Trim("\uFEFF\u00A0 hi\u3000\n"); got != "hi" {
		t.Errorf("Trim: %q", got)
	}
	if got := js.Trim("\u0085hi"); got != "\u0085hi" {
		t.Errorf("Trim must not strip U+0085: %q", got)
	}
	if got := js.Slice16("a😀b", 0, 2); got != "a�" {
		t.Errorf("Slice16: %q", got)
	}
	if got := js.Slice16("a😀b", 0, 200); got != "a😀b" {
		t.Errorf("Slice16: %q", got)
	}
	if got := js.EncodeURIComponent("a b/ö!~*'()"); got != "a%20b%2F%C3%B6!~*'()" {
		t.Errorf("EncodeURIComponent: %q", got)
	}
}
