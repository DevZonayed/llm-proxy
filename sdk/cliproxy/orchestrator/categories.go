package orchestrator

import (
	"regexp"
	"strings"
	"sync"
)

// catalogIndex is a lookup-friendly snapshot of a []CatalogEntry slice.
// We build it once per rules-policy construction (Configs are
// effectively immutable per server run; on hot-reload a new index is
// built alongside a new policy).
type catalogIndex struct {
	byID map[string]CatalogEntry
}

// newCatalogIndex builds a catalogIndex from the slice. The slice may
// be nil. The effective id (EffectiveID()) is what callers query.
func newCatalogIndex(catalog []CatalogEntry) *catalogIndex {
	if len(catalog) == 0 {
		return &catalogIndex{byID: map[string]CatalogEntry{}}
	}
	idx := &catalogIndex{byID: make(map[string]CatalogEntry, len(catalog))}
	for _, e := range catalog {
		id := e.EffectiveID()
		if id == "" {
			continue
		}
		idx.byID[id] = e
	}
	return idx
}

// Get returns the catalog entry registered under id, or zero-value +
// false when the id is unknown. ids are compared case-sensitively
// (model names tend to be case-sensitive already).
func (i *catalogIndex) Get(id string) (CatalogEntry, bool) {
	if i == nil {
		return CatalogEntry{}, false
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return CatalogEntry{}, false
	}
	e, ok := i.byID[id]
	return e, ok
}

// pickPreferred walks ids in order and returns the first catalog entry
// whose Provider appears in the available set. Returns zero-value +
// false when nothing matches.
func (i *catalogIndex) pickPreferred(ids []string, available []string) (CatalogEntry, bool) {
	if i == nil || len(ids) == 0 || len(available) == 0 {
		return CatalogEntry{}, false
	}
	set := make(map[string]struct{}, len(available))
	for _, a := range available {
		set[a] = struct{}{}
	}
	for _, id := range ids {
		entry, ok := i.Get(id)
		if !ok {
			continue
		}
		if _, providerOK := set[entry.Provider]; providerOK {
			return entry, true
		}
	}
	return CatalogEntry{}, false
}

// pickForRole resolves a (category, role) pair to a catalog entry by
// consulting RolePins first, then Prefer. available is the request's
// candidate provider set; the picked entry's provider must be in it.
func (i *catalogIndex) pickForRole(cat Category, role string, available []string) (CatalogEntry, bool) {
	if pinID := cat.PinFor(role); pinID != "" {
		if entry, ok := i.Get(pinID); ok {
			// Pin must be reachable from the available set; otherwise
			// fall through to Prefer.
			set := make(map[string]struct{}, len(available))
			for _, a := range available {
				set[a] = struct{}{}
			}
			if _, providerOK := set[entry.Provider]; providerOK {
				return entry, true
			}
		}
	}
	return i.pickPreferred(cat.Prefer, available)
}

// categoryClassifier wraps a slice of Categories with compiled regexes
// and the heuristic matcher.
type categoryClassifier struct {
	categories []Category
	compiled   [][]*regexp.Regexp // parallel to categories
	cfg        ClassifierConfig
	mu         sync.Mutex
}

// newCategoryClassifier compiles regexes from the supplied categories
// once. Bad patterns are logged-into-config-validation upstream; here
// we silently drop them (the rest of the predicates still apply).
func newCategoryClassifier(categories []Category, cfg ClassifierConfig) *categoryClassifier {
	cc := &categoryClassifier{
		categories: append([]Category(nil), categories...),
		cfg:        cfg,
	}
	cc.compiled = make([][]*regexp.Regexp, len(categories))
	for i, cat := range categories {
		for _, raw := range cat.Match.Regex {
			r, err := regexp.Compile(raw)
			if err != nil {
				continue
			}
			cc.compiled[i] = append(cc.compiled[i], r)
		}
	}
	return cc
}

// CategoryInput is the matcher's input — the same payload information
// the difficulty classifier already extracts, plus a cached lowercase
// text buffer to avoid re-walking the JSON for every category.
type CategoryInput struct {
	// UserText is the full text extracted from the user-visible
	// messages, in source order, lowercased.
	UserText string
	// HasTools is true when the request payload carries a non-empty
	// tools/functions array.
	HasTools bool
	// ApproxTokens is the difficulty classifier's token estimate
	// (4 chars/token).
	ApproxTokens int
}

// classify walks the configured categories and returns the first
// matching one. Returns "" when nothing matches.
func (cc *categoryClassifier) classify(in CategoryInput) string {
	if cc == nil || len(cc.categories) == 0 {
		return ""
	}
	for i, cat := range cc.categories {
		if cc.matches(cat, cc.compiled[i], in) {
			return cat.Name
		}
	}
	return ""
}

// matches evaluates a single category's predicates against the input.
// Predicates are combined with logical AND; only non-zero predicates
// contribute.
func (cc *categoryClassifier) matches(cat Category, compiled []*regexp.Regexp, in CategoryInput) bool {
	m := cat.Match
	text := in.UserText
	if m.MinTokens > 0 && in.ApproxTokens < m.MinTokens {
		return false
	}
	if m.MaxTokens > 0 && in.ApproxTokens > m.MaxTokens {
		return false
	}
	if m.RequireTools && !in.HasTools {
		return false
	}
	if m.RequireCodeBlock && !containsCodeBlock(text) {
		return false
	}
	if len(m.NoneOf) > 0 {
		for _, s := range m.NoneOf {
			if s == "" {
				continue
			}
			if strings.Contains(text, strings.ToLower(s)) {
				return false
			}
		}
	}
	// Positive matchers (Keywords / AnyOf / Regex) are OR-combined and
	// the category is rejected if all of them are non-empty yet none
	// hit. Empty matchers do not contribute, so a category with no
	// positive matchers still passes (it acts as a catch-all).
	posDefined, posMatched := false, false
	if len(m.Keywords) > 0 || len(m.AnyOf) > 0 {
		posDefined = true
		if containsAny(text, m.Keywords) || containsAny(text, m.AnyOf) {
			posMatched = true
		}
	}
	if !posMatched && len(compiled) > 0 {
		posDefined = true
		for _, r := range compiled {
			if r.MatchString(in.UserText) {
				posMatched = true
				break
			}
		}
	}
	if posDefined && !posMatched {
		return false
	}
	return true
}

// containsAny returns true if any case-insensitive substring in needles
// appears in haystack. haystack is expected to be already lowercased.
func containsAny(haystack string, needles []string) bool {
	for _, n := range needles {
		if n == "" {
			continue
		}
		if strings.Contains(haystack, strings.ToLower(n)) {
			return true
		}
	}
	return false
}

// containsCodeBlock returns true when the input contains a Markdown
// code fence or inline code span. text is expected to be lowercased.
func containsCodeBlock(text string) bool {
	if strings.Contains(text, "```") {
		return true
	}
	// Inline code uses backticks; lower-casing leaves them intact.
	return strings.Contains(text, "`")
}

// findCategory returns the Category struct registered under the
// supplied name, or zero-value + false when none matches. Lookup is
// case-insensitive.
func (cc *categoryClassifier) findCategory(name string) (Category, bool) {
	if cc == nil || strings.TrimSpace(name) == "" {
		return Category{}, false
	}
	want := strings.ToLower(strings.TrimSpace(name))
	for _, cat := range cc.categories {
		if strings.EqualFold(strings.TrimSpace(cat.Name), want) {
			return cat, true
		}
	}
	return Category{}, false
}

// categoriesSummary returns a short multi-line listing of categories
// suitable for embedding in an LLM classifier prompt. Each entry is
// "name: instructions" — empty instructions are tolerated.
func (cc *categoryClassifier) categoriesSummary() string {
	if cc == nil || len(cc.categories) == 0 {
		return ""
	}
	var b strings.Builder
	for _, cat := range cc.categories {
		b.WriteString("- ")
		b.WriteString(strings.TrimSpace(cat.Name))
		if instr := strings.TrimSpace(cat.Instructions); instr != "" {
			b.WriteString(": ")
			b.WriteString(instr)
		}
		b.WriteByte('\n')
	}
	return b.String()
}
