package main

// Local semantic RAG backend: shells out to the local code-rag tool
// (~/code-rag/rag-search.sh, bge-m3 embedding + bge-reranker-v2-m3 rerank)
// and parses its text output. Portal-scoped results are then re-checked
// against filelist.txt so the privacy gate (whitelist) is never bypassed:
// semantic retrieval is real, but the boundary is still the whitelist.
//
// Environment variables:
//
//	KB_RAG_SCRIPT  path to rag-search.sh (default ~/code-rag/rag-search.sh)
//	KB_RAG_GROUP   group to search (default "kb"; use "kb-index" for indexes only)
//	KB_RAG_TOP     number of hits per query (default 8)
//	KB_RAG_TIMEOUT subprocess timeout seconds (default 45)
//
// If the script is missing or fails, /rag, /search and /api/search report the
// failure explicitly instead of silently degrading (same philosophy as
// code-rag: no fallback to BM25 / substring scan when embedding is
// unavailable). The old per-line substring walker (SearchKB) was removed —
// it is a banned approach: it re-reads every file on every request, is
// O(KB size) per query, and cannot do semantic matching.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// SemanticHit is one parsed result from rag-search.sh.
type SemanticHit struct {
	Repo      string  `json:"repo"`        // e.g. "kb-notes" or "kb-indexes"
	Group     string  `json:"group"`       // e.g. "kb"
	Label     string  `json:"label"`       // e.g. "section path-kb-publish"
	KbRelPath string  `json:"kb_rel_path"` // path relative to KB root, e.g. "notes/path-kb-publish.md"
	Line      int     `json:"line"`
	Score     float64 `json:"score"`
	Reranked  bool    `json:"reranked"`
	Snippet   string  `json:"snippet"`
	Allowed   bool    `json:"allowed"` // true if passes portal.IsFileAllowed
}

// LocalRAGResult is the aggregate returned to the HTTP layer.
type LocalRAGResult struct {
	Query        string        `json:"query"`
	Engine       string        `json:"engine"` // "code-rag (bge-m3 + rerank)"
	Hits         []SemanticHit `json:"hits"`
	TotalRecall  int           `json:"total_recall"` // chunks scanned by the local index
	Error        string        `json:"error,omitempty"`
	ContextText  string        `json:"context_text,omitempty"`
	MatchedFiles []string      `json:"matched_files,omitempty"`
}

func codeRagScript() string {
	if v := os.Getenv("KB_RAG_SCRIPT"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "~/code-rag/rag-search.sh"
	}
	return filepath.Join(home, "code-rag", "rag-search.sh")
}

func codeRagGroup() string {
	if v := os.Getenv("KB_RAG_GROUP"); v != "" {
		return v
	}
	return "kb"
}

func codeRagTop() int {
	if v := os.Getenv("KB_RAG_TOP"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 8
}

func codeRagTimeout() time.Duration {
	if v := os.Getenv("KB_RAG_TIMEOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 45 * time.Second
}

// repoToKBPrefix maps code-rag repo names back to KB-relative path prefixes.
func repoToKBPrefix(repo string) string {
	switch repo {
	case "kb-notes":
		return "notes/"
	case "kb-indexes":
		return "indexes/"
	default:
		return ""
	}
}

// RunLocalRAG runs the local semantic search and returns parsed hits.
// portal may be nil (global search); when non-nil, each hit is annotated
// with Allowed based on portal.IsFileAllowed(relPath).
func RunLocalRAG(query string, portal *Portal) (*LocalRAGResult, error) {
	script := codeRagScript()
	if _, err := os.Stat(script); err != nil {
		return nil, fmt.Errorf("code-rag script not found at %s (set KB_RAG_SCRIPT)", script)
	}

	ctx, cancel := context.WithTimeout(context.Background(), codeRagTimeout())
	defer cancel()

	// rag-search.sh "query" --group <g> --top <n>
	args := []string{script, query, "--group", codeRagGroup(), "--top", strconv.Itoa(codeRagTop())}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		stderr := strings.TrimSpace(errBuf.String())
		if ctx.Err() != nil {
			return nil, fmt.Errorf("code-rag timed out after %s (query: %q)", codeRagTimeout(), query)
		}
		// code-rag exits non-zero when embedding/rerank API is unavailable.
		if stderr == "" {
			stderr = err.Error()
		}
		return nil, fmt.Errorf("code-rag failed (query: %q): %s", query, stderr)
	}

	hits := parseRagOutput(outBuf.String())
	totalRecall := extractRecall(outBuf.String())
	for i := range hits {
		h := &hits[i]
		prefix := repoToKBPrefix(h.Repo)
		if prefix != "" {
			h.KbRelPath = prefix + strings.TrimPrefix(h.KbRelPath, prefix)
		}
		if portal != nil {
			h.Allowed = portal.IsFileAllowed(h.KbRelPath)
		} else {
			h.Allowed = true
		}
	}

	return &LocalRAGResult{
		Query:       query,
		Engine:      "code-rag (bge-m3 + rerank)",
		Hits:        hits,
		TotalRecall: totalRecall,
	}, nil
}

// parseRagOutput parses the human-readable output of rag-search.sh.
// Format (from printHit in rag.mjs):
//
//	\n1. [kb-notes·kb] section path-kb-publish
//	   path-kb-publish.md:84  score=0.9487 (rerank) –92
//	   ─ 文档 ─
//	     id: path-kb-publish
//	   <snippet lines indented by 5 spaces>
func parseRagOutput(out string) []SemanticHit {
	var hits []SemanticHit
	var cur *SemanticHit
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		// "1. [kb-notes·kb] section path-kb-publish"
		if idx := strings.Index(line, ". ["); idx >= 0 {
			rest := line[idx+2:]
			// rest = "[kb-notes·kb] section path-kb-publish"
			closeIdx := strings.Index(rest, "]")
			if closeIdx <= 0 {
				continue
			}
			repoGroup := rest[1:closeIdx]
			label := strings.TrimSpace(rest[closeIdx+1:])
			var repo, group string
			if parts := strings.SplitN(repoGroup, "·", 2); len(parts) == 2 {
				repo = strings.TrimSpace(parts[0])
				group = strings.TrimSpace(parts[1])
			} else {
				repo = repoGroup
				group = repoGroup
			}
			hits = append(hits, SemanticHit{Repo: repo, Group: group, Label: label})
			cur = &hits[len(hits)-1]
			continue
		}
		if cur == nil {
			continue
		}
		// "   path-kb-publish.md:84  score=0.9487 (rerank) –92"
		if strings.HasPrefix(line, "   ") && !strings.HasPrefix(line, "     ") {
			trimmed := strings.TrimSpace(line)
			if m := strings.Index(trimmed, ":"); m > 0 {
				// path:line  score=...
				rest := trimmed[m+1:]
				lineEnd := strings.IndexAny(rest, " \t")
				if lineEnd < 0 {
					lineEnd = len(rest)
				}
				if ln, err := strconv.Atoi(rest[:lineEnd]); err == nil {
					cur.Line = ln
				}
				cur.KbRelPath = trimmed[:m]
				if s := strings.Index(rest[lineEnd:], "score="); s >= 0 {
					scoreStr := rest[lineEnd:][s+6:]
					scoreEnd := strings.IndexAny(scoreStr, " \t")
					if scoreEnd < 0 {
						scoreEnd = len(scoreStr)
					}
					if f, err := strconv.ParseFloat(scoreStr[:scoreEnd], 64); err == nil {
						cur.Score = f
					}
				}
				cur.Reranked = strings.Contains(rest[lineEnd:], "(rerank)")
			}
			continue
		}
		// snippet lines: 5-space indent
		if strings.HasPrefix(line, "     ") {
			s := strings.TrimSpace(line)
			if s != "" {
				if cur.Snippet != "" {
					cur.Snippet += "\n"
				}
				cur.Snippet += s
			}
			continue
		}
	}
	return hits
}

// extractRecall pulls "召回 N" from the header line like
// （召回 103219 → top 3）.
func extractRecall(out string) int {
	idx := strings.Index(out, "召回 ")
	if idx < 0 {
		return 0
	}
	rest := out[idx+len("召回 "):]
	end := strings.IndexAny(rest, " →\t\n")
	if end < 0 {
		end = len(rest)
	}
	n, _ := strconv.Atoi(rest[:end])
	return n
}

// FilterAllowed returns only hits that pass the portal whitelist.
func (r *LocalRAGResult) FilterAllowed() *LocalRAGResult {
	out := &LocalRAGResult{Query: r.Query, Engine: r.Engine, TotalRecall: r.TotalRecall}
	seen := map[string]bool{}
	for _, h := range r.Hits {
		if !h.Allowed {
			continue
		}
		if !seen[h.KbRelPath] {
			seen[h.KbRelPath] = true
			out.MatchedFiles = append(out.MatchedFiles, h.KbRelPath)
		}
		out.Hits = append(out.Hits, h)
	}
	return out
}

// BuildContextText renders allowed hits into a markdown context block,
// shaped for injection into an LLM prompt (mirrors the old RAGResponse.ContextText).
func (r *LocalRAGResult) BuildContextText() string {
	var sb strings.Builder
	sb.WriteString("# RAG Context (code-rag: bge-m3 + rerank)\n\n")
	sb.WriteString(fmt.Sprintf("> Query: %s\n\n", r.Query))
	if r.TotalRecall > 0 {
		sb.WriteString(fmt.Sprintf("> 索引召回：%d 个分块；白名单过滤后：%d 个文件、%d 个命中。\n\n",
			r.TotalRecall, len(r.MatchedFiles), len(r.Hits)))
	}
	for _, h := range r.Hits {
		sb.WriteString(fmt.Sprintf("### Source: `%s` (line %d, score=%.4f%s)\n", h.KbRelPath, h.Line, h.Score, rerankTag(h)))
		if h.Snippet != "" {
			sb.WriteString(h.Snippet + "\n\n")
		}
	}
	return sb.String()
}

func rerankTag(h SemanticHit) string {
	if h.Reranked {
		return ", rerank"
	}
	return ""
}

// marshalHelper keeps JSON responses consistent.
func (r *LocalRAGResult) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Query        string        `json:"query"`
		Engine       string        `json:"engine"`
		Hits         []SemanticHit `json:"hits"`
		TotalRecall  int           `json:"total_recall"`
		MatchedFiles []string      `json:"matched_files,omitempty"`
		Error        string        `json:"error,omitempty"`
	}{r.Query, r.Engine, r.Hits, r.TotalRecall, r.MatchedFiles, r.Error})
}
