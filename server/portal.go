package main

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Portal represents a single entrance / subset view of the knowledge base.
type Portal struct {
	Name         string          `json:"name"`
	Dir          string          `json:"dir"`
	FileListPath string          `json:"filelist_path"`
	IndexPath    string          `json:"index_path"`
	AgentsPath   string          `json:"agents_path"`
	UploadDir    string          `json:"upload_dir"`
	DomainPath   string          `json:"domain_path"`
	Domains      []string        `json:"domains"`
	AllowedFiles map[string]bool `json:"-"`
	AllowedGlobs []string        `json:"-"`
	mu           sync.RWMutex
}

// NewPortal initializes a portal structure for a given entrance directory.
func NewPortal(name, dir string) *Portal {
	return &Portal{
		Name:         name,
		Dir:          dir,
		FileListPath: filepath.Join(dir, "filelist.txt"),
		IndexPath:    filepath.Join(dir, "INDEX.md"),
		AgentsPath:   filepath.Join(dir, "AGENTS.md"),
		UploadDir:    filepath.Join(dir, "uploads"),
		DomainPath:   filepath.Join(dir, "domain.txt"),
		Domains:      nil,
		AllowedFiles: make(map[string]bool),
	}
}

// Load reads filelist.txt and populates the allowed files whitelist.
func (p *Portal) Load(kbRoot string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.AllowedFiles = make(map[string]bool)
	p.AllowedGlobs = nil

	// Ensure uploads directory exists
	if err := os.MkdirAll(p.UploadDir, 0755); err != nil {
		return fmt.Errorf("failed to create upload dir: %w", err)
	}

	if _, err := os.Stat(p.FileListPath); os.IsNotExist(err) {
		// If filelist.txt doesn't exist, create an empty one
		_ = os.WriteFile(p.FileListPath, []byte("# Whitelisted files for portal: "+p.Name+"\n# One relative path per line\n"), 0644)
		return nil
	}

	file, err := os.Open(p.FileListPath)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Normalize path to use forward slashes and clean relative path
		cleaned := filepath.Clean(filepath.ToSlash(line))
		cleaned = strings.TrimPrefix(cleaned, "/")
		cleaned = strings.TrimPrefix(cleaned, "./")

		if strings.Contains(cleaned, "*") || strings.Contains(cleaned, "?") {
			p.AllowedGlobs = append(p.AllowedGlobs, cleaned)
		} else {
			p.AllowedFiles[cleaned] = true
		}
	}

	// Load optional domain mappings (domain.txt or domains.txt)
	p.Domains = nil
	domainFile := p.DomainPath
	if _, err := os.Stat(domainFile); os.IsNotExist(err) {
		altDomain := filepath.Join(p.Dir, "domains.txt")
		if _, errAlt := os.Stat(altDomain); errAlt == nil {
			domainFile = altDomain
		}
	}
	if dFile, err := os.Open(domainFile); err == nil {
		dScanner := bufio.NewScanner(dFile)
		for dScanner.Scan() {
			d := strings.ToLower(strings.TrimSpace(dScanner.Text()))
			if d != "" && !strings.HasPrefix(d, "#") {
				p.Domains = append(p.Domains, d)
			}
		}
		dFile.Close()
	}

	return scanner.Err()
}

// IsFileAllowed checks if a requested file path (relative to KB root) is allowed by this portal.
func (p *Portal) IsFileAllowed(relPath string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	cleaned := filepath.Clean(filepath.ToSlash(relPath))
	cleaned = strings.TrimPrefix(cleaned, "/")
	cleaned = strings.TrimPrefix(cleaned, "./")

	// Prevent directory traversal
	if strings.HasPrefix(cleaned, "..") || strings.Contains(cleaned, "/../") {
		return false
	}

	// 1. Direct whitelist match (flexible for with or without .md extension)
	if p.AllowedFiles[cleaned] || p.AllowedFiles[cleaned+".md"] || p.AllowedFiles[strings.TrimSuffix(cleaned, ".md")] {
		return true
	}

	// 2. Glob pattern match
	for _, pattern := range p.AllowedGlobs {
		if matched, _ := filepath.Match(pattern, cleaned); matched {
			return true
		}
		if matched, _ := filepath.Match(pattern, cleaned+".md"); matched {
			return true
		}
		if !strings.Contains(pattern, "/") {
			if matched, _ := filepath.Match(pattern, filepath.Base(cleaned)); matched {
				return true
			}
			if matched, _ := filepath.Match(pattern, filepath.Base(cleaned)+".md"); matched {
				return true
			}
		}
	}

	return false
}

// GetFileList returns all allowed file entries as a slice.
func (p *Portal) GetFileList() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	res := make([]string, 0, len(p.AllowedFiles)+len(p.AllowedGlobs))
	for f := range p.AllowedFiles {
		res = append(res, f)
	}
	for _, g := range p.AllowedGlobs {
		res = append(res, g+" (glob)")
	}
	return res
}

// PortalManager manages all active portals and dynamically detects entrance folders.
type PortalManager struct {
	KBRoot     string
	PortalsDir string
	portals    map[string]*Portal
	mu         sync.RWMutex
}

// NewPortalManager creates a manager for portals in the given directory.
func NewPortalManager(kbRoot, portalsDir string) *PortalManager {
	return &PortalManager{
		KBRoot:     kbRoot,
		PortalsDir: portalsDir,
		portals:    make(map[string]*Portal),
	}
}

// ScanAndLoad scans the portals directory and loads each entrance.
func (m *PortalManager) ScanAndLoad() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := os.MkdirAll(m.PortalsDir, 0755); err != nil {
		return fmt.Errorf("failed to create portals dir: %w", err)
	}

	entries, err := os.ReadDir(m.PortalsDir)
	if err != nil {
		return err
	}

	currentPortals := make(map[string]*Portal)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		dir := filepath.Join(m.PortalsDir, name)

		portal := NewPortal(name, dir)
		if err := portal.Load(m.KBRoot); err != nil {
			log.Printf("⚠️  [Portal %s] warning loading filelist: %v", name, err)
		}
		currentPortals[name] = portal
		log.Printf("📚 [Portal: %s] loaded (%d explicit files, %d globs)",
			name, len(portal.AllowedFiles), len(portal.AllowedGlobs))
	}

	m.portals = currentPortals
	return nil
}

// GetPortal returns the portal instance by name.
func (m *PortalManager) GetPortal(name string) (*Portal, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.portals[name]
	return p, ok
}

// ListPortals returns a list of all active portals.
func (m *PortalManager) ListPortals() []*Portal {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := make([]*Portal, 0, len(m.portals))
	for _, p := range m.portals {
		list = append(list, p)
	}
	return list
}

// ResolvePortalAndSubpath determines the portal and subpath from an HTTP request.
// Implements S3-style dual bucketing:
// 1. Domain-style bucketing (Virtual-Host style):
//    e.g. Host: <portal>.domain.com or Host: <portal>.localhost -> bucket=<portal>, path=req.URL.Path
//    or explicit domain matching domain.txt
// 2. Path-style bucketing fallback:
//    e.g. Host: domain.com, Path: /<portal>/<subpath> -> bucket=<portal>, path=<subpath>
func (m *PortalManager) ResolvePortalAndSubpath(req *http.Request) (*Portal, string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	rawHost := req.Host
	hostOnly, _, err := net.SplitHostPort(rawHost)
	if err != nil {
		hostOnly = rawHost
	}
	hostOnly = strings.ToLower(strings.TrimSpace(hostOnly))

	reqPath := strings.TrimPrefix(req.URL.Path, "/")

	// 1. Domain-style (Virtual-Host) resolution:
	// A. Exact domain match configured in portal's domain.txt
	for _, p := range m.portals {
		for _, d := range p.Domains {
			if strings.EqualFold(d, hostOnly) {
				return p, reqPath, true
			}
		}
	}

	// B. Subdomain match (e.g. {portal}.domain.com or {portal}.localhost)
	// If host is not a raw IP address and contains at least one dot
	if net.ParseIP(hostOnly) == nil && strings.Contains(hostOnly, ".") {
		subdomain := strings.Split(hostOnly, ".")[0]
		if p, ok := m.portals[subdomain]; ok {
			return p, reqPath, true
		}
	}

	// 2. Path-style resolution fallback (e.g. /{portal}/{subPath...})
	parts := strings.SplitN(reqPath, "/", 2)
	potentialPortal := parts[0]
	if p, ok := m.portals[potentialPortal]; ok {
		subPath := ""
		if len(parts) > 1 {
			subPath = parts[1]
		}
		return p, subPath, true
	}

	return nil, reqPath, false
}
