package main

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// SearchMatch represents a single matching line in a file.
type SearchMatch struct {
	File       string `json:"file"`
	LineNumber int    `json:"line"`
	Snippet    string `json:"snippet"`
}

// SearchResponse holds search query results.
type SearchResponse struct {
	Query      string        `json:"query"`
	Scope      string        `json:"scope"` // "global" or portal name
	TotalFiles int           `json:"total_files_matched"`
	TotalLines int           `json:"total_matches"`
	Matches    []SearchMatch `json:"matches"`
}

// SearchKB performs a case-insensitive search across files.
// If portal is nil, searches the entire knowledge base (global search).
// If portal is non-nil, searches ONLY files allowed by portal.IsFileAllowed.
func SearchKB(kbRoot string, portal *Portal, query string, maxMatches int) (*SearchResponse, error) {
	if maxMatches <= 0 {
		maxMatches = 50
	}

	scope := "global"
	if portal != nil {
		scope = portal.Name
	}

	resp := &SearchResponse{
		Query:   query,
		Scope:   scope,
		Matches: make([]SearchMatch, 0),
	}

	queryLower := strings.ToLower(strings.TrimSpace(query))
	if queryLower == "" {
		return resp, nil
	}

	matchedFiles := make(map[string]bool)

	err := filepath.WalkDir(kbRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "tools" || name == "server" || name == "portals" {
				return filepath.SkipDir
			}
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".md" && ext != ".txt" {
			return nil
		}

		relPath, err := filepath.Rel(kbRoot, path)
		if err != nil {
			return nil
		}
		relPath = filepath.ToSlash(relPath)

		if portal != nil {
			if !portal.IsFileAllowed(relPath) {
				return nil
			}
		}

		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			lineText := scanner.Text()
			if strings.Contains(strings.ToLower(lineText), queryLower) {
				matchedFiles[relPath] = true
				trimmed := strings.TrimSpace(lineText)
				if len(trimmed) > 300 {
					trimmed = trimmed[:300] + "..."
				}
				resp.Matches = append(resp.Matches, SearchMatch{
					File:       relPath,
					LineNumber: lineNum,
					Snippet:    trimmed,
				})

				if len(resp.Matches) >= maxMatches {
					return nil
				}
			}
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	resp.TotalFiles = len(matchedFiles)
	resp.TotalLines = len(resp.Matches)
	return resp, nil
}

// RAGResponse formats retrieved knowledge strictly scoped to filelist.txt for LLM/RAG context injection.
type RAGResponse struct {
	Portal       string        `json:"portal"`
	Query        string        `json:"query"`
	AllowedFiles []string      `json:"allowed_files"`
	MatchedFiles []string      `json:"matched_files"`
	TotalMatches int           `json:"total_matches"`
	ContextText  string        `json:"context_text"`
	Matches      []SearchMatch `json:"matches"`
}

// RAGSearch retrieves snippets strictly from files matching the portal's filelist.txt.
func RAGSearch(kbRoot string, portal *Portal, query string, maxMatches int) (*RAGResponse, error) {
	searchResp, err := SearchKB(kbRoot, portal, query, maxMatches)
	if err != nil {
		return nil, err
	}

	var matchedFiles []string
	seenFiles := make(map[string]bool)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# RAG Context from Portal [%s] (Scope: filelist.txt)\n\n", portal.Name))
	sb.WriteString(fmt.Sprintf("> Query: %s\n\n", query))

	fileSnippets := make(map[string][]string)
	for _, m := range searchResp.Matches {
		if !seenFiles[m.File] {
			seenFiles[m.File] = true
			matchedFiles = append(matchedFiles, m.File)
		}
		fileSnippets[m.File] = append(fileSnippets[m.File], fmt.Sprintf("- [Line %d] %s", m.LineNumber, m.Snippet))
	}

	for _, file := range matchedFiles {
		sb.WriteString(fmt.Sprintf("### Source: `%s`\n", file))
		for _, s := range fileSnippets[file] {
			sb.WriteString(s + "\n")
		}
		sb.WriteString("\n")
	}

	return &RAGResponse{
		Portal:       portal.Name,
		Query:        query,
		AllowedFiles: portal.GetFileList(),
		MatchedFiles: matchedFiles,
		TotalMatches: searchResp.TotalLines,
		ContextText:  sb.String(),
		Matches:      searchResp.Matches,
	}, nil
}
