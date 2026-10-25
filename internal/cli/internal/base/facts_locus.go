package base

import (
	"regexp"
	"sort"
	"strings"

	"github.com/entireio/entire-brain/factmerge"
)

// LocusIdentifierPattern matches code-identifier-like tokens: backtick spans are
// handled separately; this catches dotted paths (settings.Load), file paths
// (internal/cli/facts.go), CamelCase, and snake_case identifiers.
//
// A path segment is `[A-Za-z0-9_]+(?:-[A-Za-z0-9_]+)*` — interior hyphens are
// part of the name. Without that, `website/scripts/pre-build.ts` was cut at the
// hyphen and emitted as TWO tokens, "website/scripts/pre" and "build.ts",
// neither of which names anything in the repo. That is worse than emitting
// nothing: a fragment looks like a path, so it anchors a fact to a file that
// does not exist, and locusTokenIsFilePath cannot even flag the drift (the
// first fragment has no extension, the second has no slash). Hyphenated names
// are not exotic here — .github/workflows/*, pre-build.ts, 20260320-no-raft.md.
//
// The hyphen must be INTERIOR (followed by more name), so a trailing dash is
// left out of the token, and a separator (. or /) is still required for the
// first alternative to fire at all — which is what keeps ordinary hyphenated
// English ("state-of-the-art") out of the locus.
//
// The leading `\.?` is the same defect for dot-directories: ".github/..." was
// emitted as "github/...", a path that resolves nowhere and would be reported
// as drifted on every recall.
var LocusIdentifierPattern = regexp.MustCompile(`\.?[A-Za-z_][A-Za-z0-9_]*(?:-[A-Za-z0-9_]+)*(?:[./][A-Za-z0-9_]+(?:-[A-Za-z0-9_]+)*)+|[A-Za-z_]*[a-z][A-Za-z0-9]*[A-Z][A-Za-z0-9_]*|[A-Za-z][A-Za-z0-9]*_[A-Za-z0-9_]+`)

// FactLocus extracts the set of code identifiers a fact is about — the symbols,
// packages, file paths, and refs named in its text (including backtick-quoted
// spans). Returned lowercased and deduped. This is the locus signal: it lets
// retrieval boost facts that name the same code the query names, and is the
// hook for tying facts to the semantic index in a later slice.
func FactLocus(text string) []string {
	set := map[string]struct{}{}
	add := func(tok string) {
		// Trimmed asymmetrically: a LEADING dot is part of the name
		// (".github/workflows/ci.yml", ".entire/settings.json"), a trailing one
		// is sentence punctuation. Trimming both ends, as this did, silently
		// renamed every dot-directory path in the corpus.
		tok = strings.ToLower(strings.TrimRight(strings.TrimLeft(tok, LocusLeadingTrimCutset), LocusTrailingTrimCutset))
		if len(tok) >= 3 {
			set[tok] = struct{}{}
		}
	}
	// Backtick-quoted spans are almost always code; take each whitespace token.
	for _, span := range BacktickSpanPattern.FindAllStringSubmatch(text, -1) {
		for _, tok := range strings.Fields(span[1]) {
			add(tok)
		}
	}
	for _, tok := range LocusIdentifierPattern.FindAllString(text, -1) {
		add(tok)
	}
	out := make([]string, 0, len(set))
	for tok := range set {
		out = append(out, tok)
	}
	sort.Strings(out)
	return out
}

const (
	// LocusLeadingTrimCutset omits "." on purpose; see add in FactLocus.
	LocusLeadingTrimCutset  = "`,;:([{'\""
	LocusTrailingTrimCutset = "`.,;:)]}'\""
)

var BacktickSpanPattern = regexp.MustCompile("`([^`]+)`")

// FactLocusOf returns a fact's stored locus, falling back to deriving it from
// the text for facts authored before the locus field existed (or never
// backfilled). Read surfaces use this so locus matching works regardless of
// whether `reclassify` has run.
func FactLocusOf(f factmerge.Record) []string {
	if len(f.Locus) > 0 {
		return f.Locus
	}
	return FactLocus(f.Text)
}
