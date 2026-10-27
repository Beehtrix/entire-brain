package cli

import "github.com/entireio/entire-brain/internal/cli/internal/retrieval"

// Type aliases for the unified retrieval engine extracted to retrieval:
// struct identity and JSON wire shapes are unchanged, the cli call sites keep
// their old lowercase names.
type (
	unifiedResult         = retrieval.Result
	retrievalMode         = retrieval.Mode
	retrievalOptions      = retrieval.Options
	retrievalCaveat       = retrieval.Caveat
	factReviewGroup       = retrieval.FactReviewGroup
	factReviewNotice      = retrieval.FactReviewNotice
	scoredHistoryRecord   = retrieval.ScoredHistoryRecord
	conversationTurn      = retrieval.ConversationTurn
	sessionAbstract       = retrieval.SessionAbstract
	abstractStatement     = retrieval.AbstractStatement
	abstractCoverageRange = retrieval.AbstractCoverageRange
	conceptMatch          = retrieval.ConceptMatch
)
