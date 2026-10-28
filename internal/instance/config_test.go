package instance

import (
	"slices"
	"testing"
)

func TestQuoteArgRoundTrip(t *testing.T) {
	for _, s := range []string{
		"plain",
		"cd sub && ./run.sh -x",
		`it's "quoted"`,
		`back\slash $HOME *.jar`,
		"line1\nline2",
		"'",
		"",
	} {
		cmd := "bash -c " + QuoteArg(s)
		got, err := splitCommand(cmd)
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		if want := []string{"bash", "-c", s}; !slices.Equal(got, want) {
			t.Errorf("QuoteArg(%q) → %s → %q, want %q", s, cmd, got, want)
		}
	}
}
