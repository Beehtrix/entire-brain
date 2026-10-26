package patterns_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/entireio/entire-brain/internal/cli/internal/patterntest"
	patterncore "github.com/entireio/entire-brain/internal/cli/patterns"
)

func TestCandidatePromotionGate(t *testing.T) {
	db := patterntest.FreshCorpusDB(t)
	now := time.Now()

	// A specific, high-support, successful workflow — but with NO corroboration.
	// "mise deploy" is rare across the corpus, so its per-command idf is high.
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("episode:d%d", i)
		patterntest.InsertCorpusEpisode(t, db, id, "deploy:release", "success", 2, now)
		patterntest.InsertCorpusShape(t, db, id, "mise build", "mise deploy")
	}
	// Many unrelated episodes running only generic commands so "ls"/"cat" are
	// common (low specificity) and the deploy shape stands out.
	for i := 0; i < 200; i++ {
		id := fmt.Sprintf("episode:u%d", i)
		patterntest.InsertCorpusEpisode(t, db, id, "other:thing", "neutral", 2, now)
		patterntest.InsertCorpusShape(t, db, id, "ls", "cat")
	}

	if err := patterncore.BuildCandidates(db, "gh/acme/cli", now); err != nil {
		t.Fatal(err)
	}
	uncorr, ok := patterntest.TaskStrengthFor(t, db, "deploy:release")
	if !ok {
		t.Fatal("expected a deploy:release task candidate")
	}
	if uncorr >= patterncore.PromotionThreshold {
		t.Errorf("uncorroborated task must be capped below %.2f, got %.3f", patterncore.PromotionThreshold, uncorr)
	}

	// Add strong (locus-match, weight>=2) durable-fact corroboration to the same
	// episodes → now promotable. Weak (weight=1) text-overlap links do not count.
	for i := 0; i < 12; i++ {
		if _, err := db.Exec(`INSERT INTO episode_facts (episode_id, fact_id, weight) VALUES (?,?,2)`, fmt.Sprintf("episode:d%d", i), "fact:x"); err != nil {
			t.Fatal(err)
		}
	}
	if err := patterncore.BuildCandidates(db, "gh/acme/cli", now); err != nil {
		t.Fatal(err)
	}
	corr, _ := patterntest.TaskStrengthFor(t, db, "deploy:release")
	if corr <= uncorr {
		t.Errorf("corroboration should lift strength: %.3f -> %.3f", uncorr, corr)
	}
	if corr < patterncore.PromotionThreshold {
		t.Errorf("corroborated high-support specific task should be promotable, got %.3f", corr)
	}
}

func TestRiskCandidatesFromFailures(t *testing.T) {
	db := patterntest.FreshCorpusDB(t)
	now := time.Now()
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("episode:r%d", i)
		patterntest.InsertCorpusEpisode(t, db, id, "migrate:schema", "corrected", 2, now)
		patterntest.InsertCorpusShape(t, db, id, "goose up", "goose status")
	}
	if err := patterncore.BuildCandidates(db, "gh/acme/cli", now); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM patterns WHERE type='risk' AND intent_sig='migrate:schema'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Error("expected a risk candidate from repeated corrected episodes")
	}
	// Risk evidence cites the corrected episodes.
	var ev int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pattern_evidence pe JOIN patterns p ON p.id=pe.pattern_id WHERE p.type='risk'`).Scan(&ev); err != nil {
		t.Fatal(err)
	}
	if ev == 0 {
		t.Error("expected risk pattern_evidence anchors")
	}
}

func TestCandidatesRebuildNotDuplicated(t *testing.T) {
	db := patterntest.FreshCorpusDB(t)
	now := time.Now()
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("episode:p%d", i)
		patterntest.InsertCorpusEpisode(t, db, id, "build:thing", "neutral", 2, now)
		patterntest.InsertCorpusShape(t, db, id, "go build", "go test")
	}
	if err := patterncore.BuildCandidates(db, "gh/acme/cli", now); err != nil {
		t.Fatal(err)
	}
	first := patterntest.CorpusCount(t, db, "patterns")
	if first == 0 {
		t.Fatal("fixture produced no candidates, so rebuild idempotence would be vacuous")
	}
	if err := patterncore.BuildCandidates(db, "gh/acme/cli", now); err != nil {
		t.Fatal(err)
	}
	if again := patterntest.CorpusCount(t, db, "patterns"); again != first {
		t.Errorf("candidate rebuild duplicated patterns: %d -> %d", first, again)
	}
}
