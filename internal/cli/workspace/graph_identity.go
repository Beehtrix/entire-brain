package workspace

import (
	"fmt"
	"hash/fnv"
	"strings"
)

func GraphCrossEdgeID(edge CrossEdge) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(edge.RelationKind))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(edge.Type))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(edge.Endpoint))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(edge.FromRepo))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(edge.FromSymbol.ID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(edge.ToRepo))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(edge.ToSymbol.ID))
	return fmt.Sprintf("graph:%016x", h.Sum64())
}

func GraphCrossEdgePath(edge CrossEdge) string {
	from := strings.TrimSpace(edge.FromSymbol.FilePath)
	to := strings.TrimSpace(edge.ToSymbol.FilePath)
	switch {
	case from != "" && to != "":
		return edge.FromRepo + ":" + from + " -> " + edge.ToRepo + ":" + to
	case from != "":
		return edge.FromRepo + ":" + from
	case to != "":
		return edge.ToRepo + ":" + to
	default:
		return edge.FromRepo + " -> " + edge.ToRepo
	}
}

func GraphSymbolDisplay(symbol SymbolRef) string {
	for _, value := range []string{symbol.QualifiedName, symbol.Name, symbol.ID} {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return "(unknown)"
}

func GraphSearchScore(text, query string) float64 {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return 0
	}
	text = strings.ToLower(text)
	terms := strings.Fields(query)
	if len(terms) == 0 {
		return 0
	}
	var matched int
	for _, term := range terms {
		if strings.Contains(text, term) {
			matched++
		}
	}
	if matched == 0 {
		return 0
	}
	return float64(matched) / float64(len(terms))
}
