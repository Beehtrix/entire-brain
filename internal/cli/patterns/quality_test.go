package patterns_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/entireio/entire-brain/internal/cli/internal/base"
	"github.com/entireio/entire-brain/internal/cli/internal/patterntest"
	patterncore "github.com/entireio/entire-brain/internal/cli/patterns"
)

// Gate: top (promotable) candidates must include at least two independent source
// anchors unless explicitly fact-backed.
func TestQualityPromotableHaveAnchorsOrFactBacked(t *testing.T) {
	db := patterntest.PromotableCorpus(t, time.Now())
	if err := patterncore.BuildCandidates(db, "gh/acme/cli", time.Now()); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT json_redacted FROM dossiers`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var blob string
		if err := rows.Scan(&blob); err != nil {
			t.Fatal(err)
		}
		var rec patterncore.DossierRecord
		if err := json.Unmarshal([]byte(blob), &rec); err != nil {
			t.Fatal(err)
		}
		n++
		if len(rec.SourceAnchors) < 2 && len(rec.Facts) == 0 {
			t.Errorf("promotable dossier %q has %d anchors and no facts — gate requires >=2 anchors or fact-backing",
				rec.Title, len(rec.SourceAnchors))
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("expected at least one promotable dossier to check")
	}
}

// Gate: redaction must hold over the consolidation record (the JSON used both at
// rest and as agent verifier input).
func TestQualityConsolidationRedactionBoundary(t *testing.T) {
	now := time.Now()
	db := patterntest.FreshCorpusDB(t)
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("episode:s%d", i)
		patterntest.InsertCorpusEpisode(t, db, id, "deploy:release", "success", 2, now)
		patterntest.InsertCorpusShape(t, db, id, "mise build", "mise deploy")
		// A fact whose paths carry a home-dir username AND a secret token.
		if _, err := db.Exec(`INSERT INTO episode_facts (episode_id, fact_id, kind, paths, weight) VALUES (?,?,?,?,2)`,
			id, "fact:deploy", "architecture", "/Users/example-user/secret ghp_SECRETTOKEN0123456789abcdef"); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 200; i++ {
		id := fmt.Sprintf("episode:n%d", i)
		patterntest.InsertCorpusEpisode(t, db, id, "other:thing", "neutral", 2, now)
		patterntest.InsertCorpusShape(t, db, id, "ls", "cat")
	}
	if err := patterncore.BuildCandidates(db, "gh/acme/cli", now); err != nil {
		t.Fatal(err)
	}
	var blob string
	if err := db.QueryRow(`SELECT group_concat(json_redacted, '||') FROM dossiers`).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"example-user", "ghp_SECRETTOKEN0123456789abcdef"} {
		if strings.Contains(blob, secret) {
			t.Errorf("dossier JSON leaked %q:\n%s", secret, blob)
		}
	}
	// A second redaction pass over stored JSON must keep both sensitive values
	// absent. This does not exercise the verifier transport.
	if got := base.RedactText(blob); strings.Contains(got, "example-user") || strings.Contains(got, "ghp_SECRET") {
		t.Errorf("second redaction pass leaked a secret: %s", got)
	}
}
