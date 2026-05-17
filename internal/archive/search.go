package archive

import (
	"strings"
	"sync"
	"unicode"
)

// SearchResult represents a single ranked result from an archive search.
type SearchResult struct {
	ChunkID   string
	MessageID string
	Sequence  int64
	Role      string
	Score     int
	Preview   string // first ~120 chars of visible content
}

// SearchService maintains a lazy in-memory index over an archive.
type SearchService struct {
	mu      sync.Mutex
	archive *SessionArchive
	index   []indexedEntry
	built   bool
	dirty   bool
}

type indexedEntry struct {
	chunkID    string
	messageID  string
	sequence   int64
	role       string
	rawContent string
	content    string // lowercased
	toolMeta   string // lowercased tool call names + result summaries
	roleLower  string // lowercased role
}

// NewSearchService constructs a search service backed by the provided archive.
func NewSearchService(archive *SessionArchive) *SearchService {
	return &SearchService{archive: archive}
}

// MarkDirty signals that the underlying archive changed; index will rebuild on next search.
func (s *SearchService) MarkDirty() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dirty = true
}

// UpdateArchive replaces the archive reference and marks the index dirty.
func (s *SearchService) UpdateArchive(archive *SessionArchive) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.archive = archive
	s.dirty = true
	s.built = false
}

// Search performs a keyword search over the archive.
// maxResults <= 0 means unbounded.
func (s *SearchService) Search(query string, maxResults int, caseSensitive bool) []SearchResult {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.archive == nil || query == "" {
		return nil
	}

	if !s.built || s.dirty {
		s.buildIndex()
		s.built = true
		s.dirty = false
	}

	tokens := tokenize(query, caseSensitive)
	queryNorm := query
	if !caseSensitive {
		queryNorm = strings.ToLower(query)
	}

	var results []SearchResult
	for _, entry := range s.index {
		score := scoreEntry(entry, queryNorm, tokens, caseSensitive)
		if score == 0 {
			continue
		}
		preview := entry.rawContent
		if len(preview) > 120 {
			preview = preview[:120] + "…"
		}
		results = append(results, SearchResult{
			ChunkID:   entry.chunkID,
			MessageID: entry.messageID,
			Sequence:  entry.sequence,
			Role:      entry.role,
			Score:     score,
			Preview:   preview,
		})
	}

	sortSearchResults(results)

	if maxResults > 0 && len(results) > maxResults {
		results = results[:maxResults]
	}
	return results
}

// buildIndex rebuilds the in-memory index. Must be called with mu held.
func (s *SearchService) buildIndex() {
	s.index = nil
	if s.archive == nil {
		return
	}
	for _, chunk := range s.archive.Chunks {
		for _, am := range chunk.Messages {
			entry := indexedEntry{
				chunkID:    chunk.ChunkID,
				messageID:  am.MessageID,
				sequence:   am.Sequence,
				role:       am.Role,
				rawContent: am.Message.Content,
				content:    strings.ToLower(am.Message.Content),
				roleLower:  strings.ToLower(am.Role),
			}
			var toolParts []string
			for _, tc := range am.Message.ToolCalls {
				toolParts = append(toolParts, tc.Function.Name)
			}
			if am.Role == "tool" && am.Message.Content != "" {
				toolParts = append(toolParts, am.Message.Content)
			}
			entry.toolMeta = strings.ToLower(strings.Join(toolParts, " "))
			s.index = append(s.index, entry)
		}
	}
}

// Scoring weights (per spec):
// +10 exact substring match in visible content
// +3  per token match in visible content
// +2  per token match in tool metadata/summaries
// +1  per token match in role/name fields
func scoreEntry(e indexedEntry, queryNorm string, tokens []string, caseSensitive bool) int {
	content := e.content
	toolMeta := e.toolMeta
	role := e.roleLower
	if caseSensitive {
		content = e.rawContent
		toolMeta = e.toolMeta // toolMeta is always lowercase; case-sensitive won't match uppercase
		role = e.role
	}

	score := 0
	if strings.Contains(content, queryNorm) {
		score += 10
	}
	for _, tok := range tokens {
		if strings.Contains(content, tok) {
			score += 3
		}
		if strings.Contains(toolMeta, tok) {
			score += 2
		}
		if strings.Contains(role, tok) {
			score += 1
		}
	}
	return score
}

// tokenize splits query into normalised non-empty tokens.
func tokenize(query string, caseSensitive bool) []string {
	fields := strings.FieldsFunc(query, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r)
	})
	var out []string
	for _, f := range fields {
		if f == "" {
			continue
		}
		if !caseSensitive {
			f = strings.ToLower(f)
		}
		out = append(out, f)
	}
	return out
}

// sortSearchResults sorts descending by score, then ascending by sequence (deterministic).
func sortSearchResults(results []SearchResult) {
	for i := 1; i < len(results); i++ {
		for j := i; j > 0; j-- {
			a, b := results[j-1], results[j]
			if a.Score < b.Score || (a.Score == b.Score && a.Sequence > b.Sequence) {
				results[j-1], results[j] = results[j], results[j-1]
			} else {
				break
			}
		}
	}
}
