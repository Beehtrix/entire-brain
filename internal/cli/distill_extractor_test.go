package cli

import "testing"

func TestStampFactExtractor(t *testing.T) {
	records := []factRecord{{ID: "a"}, {ID: "b"}}
	stampFactExtractor(records, " claude ", " claude-sonnet-5 ", " high ")
	for _, r := range records {
		if r.Agent != "claude" || r.Model != "claude-sonnet-5" || r.Effort != "high" {
			t.Fatalf("extractor identity not stamped (trimmed): %+v", r)
		}
	}
}
