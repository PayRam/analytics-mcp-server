package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/payram/payram-analytics-mcp-server/internal/protocol"
)

// payramDocsTool provides search and section retrieval over local PayRam docs.
// Actions:
//   - search: query markdown corpus, optional category filter, limit results
//   - get_section: return a specific section (by path and optional heading) or whole file
type payramDocsTool struct {
	root           string
	sections       []docSection
	sectionsByPath map[string][]docSection // path -> sections
	files          map[string]string       // path -> full content
}

// docSection represents a single heading + content block within a markdown file.
type docSection struct {
	Path     string
	Heading  string
	Body     string
	Category string
	Tags     []string
}

// PayramDocs builds the docs tool, indexing markdown under docs/payram-docs by default.
func PayramDocs() *payramDocsTool {
	root := strings.TrimSpace(os.Getenv("PAYRAM_DOCS_ROOT"))
	if root == "" {
		root = filepath.Join("docs", "payram-docs")
	}

	sections, byPath, files := indexDocs(root)
	applyTopics(sections)

	return &payramDocsTool{
		root:           filepath.Clean(root),
		sections:       sections,
		sectionsByPath: byPath,
		files:          files,
	}
}

// Descriptor describes the tool.
func (t *payramDocsTool) Descriptor() protocol.ToolDescriptor {
	return protocol.ToolDescriptor{
		Name:        "payram_docs",
		Description: "Search PayRam docs, fetch sections, load full docs by id, or list available docs. Categories: faqs, features, onboarding-guide. Actions: search, get_section, list_index, get_doc_by_id, list_docs. Keywords are boosted when they match headings or curated topic tags (analytics, payouts, hot wallet, payment links, multi-brand, multi-currency, customer deposit wallets, API/webhooks, config/deployment, debug).",
		InputSchema: &protocol.JSONSchema{
			Type: "object",
			Properties: map[string]protocol.JSONSchema{
				"action": {
					Type:        "string",
					Enum:        []string{"search", "get_section", "list_index", "get_doc_by_id", "list_docs"},
					Description: "Action to perform",
				},
				"query": {
					Type:        "string",
					Description: "Search query (required for search)",
				},
				"category": {
					Type:        "string",
					Description: "Optional category filter (faqs, features, onboarding-guide)",
				},
				"limit": {
					Type:        "integer",
					Description: "Max results (default 3, max 10)",
				},
				"path": {
					Type:        "string",
					Description: "Doc path relative to docs/payram-docs (required for get_section)",
				},
				"heading": {
					Type:        "string",
					Description: "Heading within the file (optional; if omitted returns whole file)",
				},
				"id": {
					Type:        "string",
					Description: "Doc id like features/payouts (required for get_doc_by_id)",
				},
				"prefix": {
					Type:        "string",
					Description: "Optional prefix to scope list_docs (e.g. features or faqs/payment)",
				},
			},
			Required: []string{"action"},
		},
	}
}

type docsArgs struct {
	Action   string `json:"action"`
	Query    string `json:"query"`
	Category string `json:"category"`
	Limit    int    `json:"limit"`
	Path     string `json:"path"`
	Heading  string `json:"heading"`
	ID       string `json:"id"`
	Prefix   string `json:"prefix"`
}

// Invoke routes search and section fetch.
func (t *payramDocsTool) Invoke(ctx context.Context, raw json.RawMessage) (protocol.CallResult, *protocol.ResponseError) {
	_ = ctx
	var args docsArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return protocol.CallResult{}, &protocol.ResponseError{Code: -32602, Message: "invalid arguments"}
		}
	}

	switch args.Action {
	case "search":
		if strings.TrimSpace(args.Query) == "" {
			return protocol.CallResult{}, &protocol.ResponseError{Code: -32602, Message: "query is required for search"}
		}
		limit := args.Limit
		if limit <= 0 {
			limit = 3
		}
		if limit > 10 {
			limit = 10
		}
		return t.search(args.Query, args.Category, limit)
	case "get_section":
		if strings.TrimSpace(args.Path) == "" {
			return protocol.CallResult{}, &protocol.ResponseError{Code: -32602, Message: "path is required for get_section"}
		}
		return t.getSection(args.Path, args.Heading)
	case "get_doc_by_id":
		if strings.TrimSpace(args.ID) == "" {
			return protocol.CallResult{}, &protocol.ResponseError{Code: -32602, Message: "id is required for get_doc_by_id"}
		}
		return t.getDocByID(args.ID)
	case "list_docs":
		return t.listDocs(args.Prefix)
	case "list_index":
		return t.listIndex(), nil
	default:
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32602, Message: "action must be search, get_section, list_index, get_doc_by_id, or list_docs"}
	}
}

// search performs a simple keyword match over headings and bodies.
func (t *payramDocsTool) search(query, category string, limit int) (protocol.CallResult, *protocol.ResponseError) {
	q := strings.ToLower(strings.TrimSpace(query))
	words := strings.Fields(q)
	if len(words) == 0 {
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32602, Message: "empty query"}
	}
	cat := strings.TrimSpace(strings.ToLower(category))

	type hit struct {
		sec   docSection
		score int
	}

	hits := make([]hit, 0)
	for _, sec := range t.sections {
		if cat != "" && strings.ToLower(sec.Category) != cat {
			continue
		}
		hScore := scoreSection(sec, words)
		if hScore > 0 {
			hits = append(hits, hit{sec: sec, score: hScore})
		}
	}

	if len(hits) == 0 {
		return protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: "No results."}}}, nil
	}

	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score == hits[j].score {
			return hits[i].sec.Path < hits[j].sec.Path
		}
		return hits[i].score > hits[j].score
	})

	if len(hits) > limit {
		hits = hits[:limit]
	}

	var b strings.Builder
	b.WriteString("Results:\n")
	for i, h := range hits {
		excerpt := trimExcerpt(h.sec.Body, 320)
		fmtPath := h.sec.Path
		if h.sec.Heading != "" {
			fmtPath += "#" + h.sec.Heading
		}
		b.WriteString(fmt.Sprintf("%d) [%s] (%s)\n", i+1, fmtPath, h.sec.Category))
		b.WriteString(excerpt)
		b.WriteString("\n\n")
	}

	return protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: strings.TrimSpace(b.String())}}}, nil
}

// getSection returns a specific section or whole file.
func (t *payramDocsTool) getSection(path, heading string) (protocol.CallResult, *protocol.ResponseError) {
	norm := filepath.ToSlash(strings.TrimSpace(path))
	norm = strings.TrimPrefix(norm, "./")

	sections, ok := t.sectionsByPath[norm]
	if !ok {
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32004, Message: "path not found"}
	}

	if strings.TrimSpace(heading) == "" {
		full := strings.TrimSpace(t.files[norm])
		if full == "" {
			return protocol.CallResult{}, &protocol.ResponseError{Code: -32004, Message: "content not found"}
		}
		return protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: full}}}, nil
	}

	target := strings.ToLower(strings.TrimSpace(heading))
	for _, sec := range sections {
		if strings.ToLower(sec.Heading) == target {
			text := fmt.Sprintf("%s\n\n%s", sec.Heading, sec.Body)
			return protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: strings.TrimSpace(text)}}}, nil
		}
	}

	return protocol.CallResult{}, &protocol.ResponseError{Code: -32004, Message: "heading not found in file"}
}

// getDocByID returns the full markdown for a doc id like "features/payouts".
func (t *payramDocsTool) getDocByID(id string) (protocol.CallResult, *protocol.ResponseError) {
	meta, err := buildDocMetadata(id)
	if err != nil {
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32602, Message: err.Error()}
	}

	fullPath := filepath.Join(t.root, meta.path)
	data, readErr := os.ReadFile(fullPath)
	if readErr != nil {
		if errors.Is(readErr, fs.ErrNotExist) {
			return protocol.CallResult{}, &protocol.ResponseError{Code: -32004, Message: describeMissingDoc(t.root, meta.path)}
		}
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32004, Message: readErr.Error()}
	}

	text := strings.TrimSpace(string(data))
	if text == "" {
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32004, Message: "content not found"}
	}

	body := fmt.Sprintf("Loaded doc %s (%s)\n\n%s", meta.id, meta.path, text)
	return protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: body}}}, nil
}

// listDocs enumerates doc ids under docs/payram-docs, optionally scoped by prefix.
func (t *payramDocsTool) listDocs(prefix string) (protocol.CallResult, *protocol.ResponseError) {
	sanitized, err := sanitizePrefix(prefix)
	if err != nil {
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32602, Message: err.Error()}
	}

	docs, listErr := collectDocIDs(t.root, sanitized)
	if listErr != nil {
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32004, Message: listErr.Error()}
	}

	var b strings.Builder
	section := sanitized
	if section == "" {
		section = "root"
	}
	plural := "s"
	if len(docs) == 1 {
		plural = ""
	}
	b.WriteString(fmt.Sprintf("Found %d doc%s under %s.\n", len(docs), plural, section))
	for _, d := range docs {
		b.WriteString("- ")
		b.WriteString(d)
		b.WriteString("\n")
	}

	return protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: strings.TrimSpace(b.String())}}}, nil
}

// listIndex returns available categories, topics, and per-file headings (truncated).
func (t *payramDocsTool) listIndex() protocol.CallResult {
	cats := make(map[string]struct{})
	fileHeadings := make(map[string][]string)
	for _, sec := range t.sections {
		cats[sec.Category] = struct{}{}
		hs := fileHeadings[sec.Path]
		if len(hs) < 8 { // cap to avoid huge payloads
			fileHeadings[sec.Path] = append(hs, sec.Heading)
		}
	}

	// sort categories
	catList := make([]string, 0, len(cats))
	for c := range cats {
		catList = append(catList, c)
	}
	sort.Strings(catList)

	// topics map
	topics := topicMap()

	var b strings.Builder
	b.WriteString("Categories: ")
	b.WriteString(strings.Join(catList, ", "))
	b.WriteString("\n\nTopics (keywords -> paths):\n")
	// invert topics for display
	keys := make([]string, 0, len(topics))
	for p := range topics {
		keys = append(keys, p)
	}
	sort.Strings(keys)
	for _, p := range keys {
		b.WriteString(fmt.Sprintf("- %s : %s\n", p, strings.Join(topics[p], ", ")))
	}

	b.WriteString("\nHeadings (truncated per file):\n")
	paths := make([]string, 0, len(fileHeadings))
	for p := range fileHeadings {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		hs := fileHeadings[p]
		b.WriteString(fmt.Sprintf("- %s: %s\n", p, strings.Join(hs, "; ")))
	}

	return protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: strings.TrimSpace(b.String())}}}
}

// buildDocMetadata normalizes a doc id into a path and url.
func buildDocMetadata(id string) (docMetadata, error) {
	if strings.Contains(id, "..") {
		return docMetadata{}, fmt.Errorf("doc id must not include path traversal characters")
	}

	normalized := strings.Trim(id, "/")
	if normalized == "" {
		return docMetadata{}, fmt.Errorf("doc id must not be empty")
	}

	pathWithExt := normalized
	if !strings.HasSuffix(strings.ToLower(pathWithExt), ".md") {
		pathWithExt += ".md"
	}

	cleanedPath := filepath.ToSlash(pathWithExt)
	return docMetadata{
		id:   normalized,
		path: cleanedPath,
		url:  fmt.Sprintf("https://docs.payram.com/%s", normalized),
	}, nil
}

// sanitizePrefix removes unsafe characters and normalizes separators.
func sanitizePrefix(prefix string) (string, error) {
	p := strings.TrimSpace(prefix)
	if p == "" {
		return "", nil
	}
	if strings.Contains(p, "..") {
		return "", fmt.Errorf("prefix must not include path traversal characters")
	}
	p = strings.Trim(p, "/")
	return filepath.ToSlash(p), nil
}

// joinRelativePath joins two segments preserving forward slashes.
func joinRelativePath(base, segment string) string {
	if base == "" {
		return filepath.ToSlash(segment)
	}
	return filepath.ToSlash(filepath.Join(base, segment))
}

// collectDocIDs walks the docs tree under a prefix and returns doc ids without .md.
func collectDocIDs(root, relativeDir string) ([]string, error) {
	rootClean := filepath.Clean(root)
	absoluteDir := filepath.Clean(filepath.Join(rootClean, relativeDir))
	if !strings.HasPrefix(absoluteDir, rootClean) {
		return nil, fmt.Errorf("unknown docs prefix \"%s\"", relativeDir)
	}

	entries, err := os.ReadDir(absoluteDir)
	if err != nil {
		return nil, err
	}

	docs := make([]string, 0)
	subdirectories := make([]string, 0)

	for _, entry := range entries {
		if entry.IsDir() {
			subdirectories = append(subdirectories, entry.Name())
			continue
		}
		if entry.Type().IsRegular() && strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			base := strings.TrimSuffix(entry.Name(), ".md")
			rel := joinRelativePath(relativeDir, base)
			docs = append(docs, rel)
		}
	}

	for _, dir := range subdirectories {
		relDir := filepath.Join(relativeDir, dir)
		nestedDocs, nestedErr := collectDocIDs(rootClean, relDir)
		if nestedErr != nil {
			return nil, nestedErr
		}
		docs = append(docs, nestedDocs...)
	}

	sort.Strings(docs)
	return docs, nil
}

// describeMissingDoc formats a helpful error message when a doc is missing.
func describeMissingDoc(root, relativePath string) string {
	fullPath := filepath.Join(root, relativePath)
	parentDir := filepath.Dir(fullPath)
	rootClean := filepath.Clean(root)
	relativeSection := strings.TrimPrefix(filepath.ToSlash(strings.TrimPrefix(parentDir, rootClean)), "/")
	sectionLabel := "the root docs folder"
	if relativeSection != "" && relativeSection != "." {
		sectionLabel = "docs/" + relativeSection
	}

	if entries, err := os.ReadDir(parentDir); err == nil {
		siblings := make([]string, 0)
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			if entry.Type().IsRegular() && strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
				base := strings.TrimSuffix(entry.Name(), ".md")
				rel := base
				if relativeSection != "" && relativeSection != "." {
					rel = joinRelativePath(relativeSection, base)
				}
				siblings = append(siblings, rel)
			}
		}
		if len(siblings) > 0 {
			return fmt.Sprintf("No doc found for \"%s\". Available entries in %s: %s.", relativePath, sectionLabel, strings.Join(siblings, ", "))
		}
	}

	return fmt.Sprintf("No doc found for \"%s\". Expected file at %s. Ensure your local docs mirror the PayRam docs tree or request a different id.", relativePath, fullPath)
}

type docMetadata struct {
	id   string
	path string
	url  string
}

// scoreSection gives weight to heading matches and body matches.
func scoreSection(sec docSection, words []string) int {
	head := strings.ToLower(sec.Heading)
	body := strings.ToLower(sec.Body)
	tags := strings.ToLower(strings.Join(sec.Tags, " "))
	score := 0
	for _, w := range words {
		if w == "" {
			continue
		}
		if strings.Contains(tags, w) {
			score += 5
		}
		if strings.Contains(head, w) {
			score += 3
		}
		if strings.Contains(body, w) {
			score += 1
		}
	}
	return score
}

// indexDocs walks root, parsing markdown files into sections.
func indexDocs(root string) ([]docSection, map[string][]docSection, map[string]string) {
	sections := make([]docSection, 0)
	byPath := make(map[string][]docSection)
	files := make(map[string]string)

	root = filepath.Clean(root)

	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		content := string(data)

		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)

		cats := strings.Split(rel, "/")
		category := "docs"
		if len(cats) > 1 {
			category = cats[0]
		}

		secs := parseSections(rel, category, content)
		sections = append(sections, secs...)
		byPath[rel] = secs
		files[rel] = strings.TrimSpace(content)
		return nil
	})

	return sections, byPath, files
}

// applyTopics attaches curated topic tags to sections based on their path.
func applyTopics(sections []docSection) {
	pathTopics := topicMap()
	for i := range sections {
		p := strings.ToLower(sections[i].Path)
		if tags, ok := pathTopics[p]; ok {
			sections[i].Tags = append(sections[i].Tags, tags...)
		}
	}
}

// topicMap defines curated topic tags for known docs.
func topicMap() map[string][]string {
	return map[string][]string{
		"onboarding-guide/hot-wallet-setup.md":               {"hot wallet", "sweep", "custody"},
		"onboarding-guide/funds-sweeping.md":                 {"funds sweep", "automation", "consolidation"},
		"onboarding-guide/testing-payment-links.md":          {"payment links", "testing", "checkout"},
		"features/payment-links.md":                          {"payment links", "checkout", "invoice"},
		"features/payouts.md":                                {"payouts", "withdraw", "settlement"},
		"features/analytics-and-reporting.md":                {"analytics", "reports", "dashboard"},
		"features/user-management.md":                        {"user roles", "rbac", "permissions"},
		"features/multi-brand-setup.md":                      {"multi-brand", "white label"},
		"features/multi-currency-and-multi-chain-support.md": {"multi-currency", "multi-chain", "tokens", "networks"},
		"features/customer-deposit-wallets.md":               {"deposit wallet", "customer wallet"},
		"features/fiat-onramp.md":                            {"on-ramp", "fiat", "buy crypto"},
		"faqs/api-integration-faqs.md":                       {"api", "webhook", "integration"},
		"faqs/configuration-faqs.md":                         {"config", "yaml", "env"},
		"faqs/deployment-faqs.md":                            {"deployment", "server requirements", "install"},
		"faqs/debug-faqs.md":                                 {"debug", "errors", "failures"},
		"faqs/fund-management-faqs.md":                       {"fund management", "settlement", "withdraw"},
		"faqs/referral-faqs.md":                              {"referral", "affiliate"},
		"faqs/general-faqs.md":                               {"overview", "general"},
	}
}

// parseSections breaks a markdown file into heading-based sections.
func parseSections(path, category, content string) []docSection {
	lines := strings.Split(content, "\n")
	currentHeading := "Introduction"
	var buf []string
	sections := make([]docSection, 0)

	flush := func() {
		text := strings.TrimSpace(strings.Join(buf, "\n"))
		if text == "" {
			buf = buf[:0]
			return
		}
		sections = append(sections, docSection{
			Path:     path,
			Heading:  currentHeading,
			Body:     text,
			Category: category,
		})
		buf = buf[:0]
	}

	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if h := parseHeading(trim); h != "" {
			flush()
			currentHeading = h
			continue
		}
		buf = append(buf, line)
	}
	flush()
	return sections
}

// parseHeading returns heading text if the line is a markdown heading.
func parseHeading(line string) string {
	if !strings.HasPrefix(line, "#") {
		return ""
	}
	// Count leading #'s then trim
	idx := 0
	for idx < len(line) && line[idx] == '#' {
		idx++
	}
	if idx == 0 || idx == len(line) {
		return ""
	}
	return strings.TrimSpace(line[idx:])
}

// trimExcerpt shortens body text to the requested length (runes).
func trimExcerpt(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 {
		return s
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "..."
}
