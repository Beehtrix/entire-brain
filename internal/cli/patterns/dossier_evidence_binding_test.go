package patterns_test

import (
	"testing"

	patterncore "github.com/entireio/entire-brain/internal/cli/patterns"
)

func TestDossierFingerprintBindsShallowVerifierEvidence(t *testing.T) {
	base := patterncore.DossierRecord{Title: "deploy", Variants: []string{"staging"}, SourceAnchors: []patterncore.DossierAnchor{{SessionID: "s", Transcript: "sessions/s.jsonl", StartLine: 1, EndLine: 3, CheckpointID: "checkpoint"}}}
	fingerprint := patterncore.DossierFingerprint(base, 0.9)
	for _, field := range []string{"title", "variants", "path", "end", "checkpoint"} {
		t.Run(field, func(t *testing.T) {
			changed := base
			changed.Variants = append([]string(nil), base.Variants...)
			changed.SourceAnchors = append([]patterncore.DossierAnchor(nil), base.SourceAnchors...)
			switch field {
			case "title":
				changed.Title = "ship"
			case "variants":
				changed.Variants[0] = "production"
			case "path":
				changed.SourceAnchors[0].Transcript = "sessions/other.jsonl"
			case "end":
				changed.SourceAnchors[0].EndLine = 6
			case "checkpoint":
				changed.SourceAnchors[0].CheckpointID = "other"
			}
			if patterncore.DossierFingerprint(changed, 0.9) == fingerprint {
				t.Fatalf("%s omitted from evidence identity", field)
			}
		})
	}
	base.Fingerprint = "stored-value"
	if patterncore.DossierFingerprint(base, 0.9) != fingerprint {
		t.Fatal("self-referential fingerprint changes evidence identity")
	}
}
