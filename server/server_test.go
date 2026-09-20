package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupMockKB(t *testing.T) (kbRoot, portalsDir string, cleanup func()) {
	tmpDir, err := os.MkdirTemp("", "kb-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	kbRoot = filepath.Join(tmpDir, "kb")
	portalsDir = filepath.Join(kbRoot, "portals")

	_ = os.MkdirAll(filepath.Join(kbRoot, "notes"), 0755)
	_ = os.MkdirAll(filepath.Join(kbRoot, "indexes"), 0755)

	_ = os.WriteFile(filepath.Join(kbRoot, "INDEX.md"), []byte("# Master Index\n- [Secret Note](notes/secret.md)\n- [Public Note](notes/public.md)\n"), 0644)
	_ = os.WriteFile(filepath.Join(kbRoot, "notes", "public.md"), []byte("# Public Knowledge\nThis is public knowledge about AI agents.\n"), 0644)
	_ = os.WriteFile(filepath.Join(kbRoot, "notes", "secret.md"), []byte("# Secret Knowledge\nThis is confidential internal data.\n"), 0644)
	_ = os.WriteFile(filepath.Join(kbRoot, "notes", "dsh-arch.md"), []byte("# DSH Architecture\nDeepseek Harness code design.\n"), 0644)

	portalDir := filepath.Join(portalsDir, "agent-public")
	_ = os.MkdirAll(portalDir, 0755)

	filelistContent := `# Whitelist for agent-public
notes/public.md
notes/dsh-*.md
`
	_ = os.WriteFile(filepath.Join(portalDir, "filelist.txt"), []byte(filelistContent), 0644)
	_ = os.WriteFile(filepath.Join(portalDir, "AGENTS.md"), []byte("# Rules for Agent Public\n1. Search public notes only.\n2. Upload lessons learned to /agent-public/upload.\n"), 0644)
	_ = os.WriteFile(filepath.Join(portalDir, "INDEX.md"), []byte("# Scoped Index\n- [Public Note](file/notes/public.md)\n"), 0644)

	cleanup = func() {
		_ = os.RemoveAll(tmpDir)
	}

	return kbRoot, portalsDir, cleanup
}

func TestPortalLoadingAndBoundary(t *testing.T) {
	kbRoot, portalsDir, cleanup := setupMockKB(t)
	defer cleanup()

	mgr := NewPortalManager(kbRoot, portalsDir)
	if err := mgr.ScanAndLoad(); err != nil {
		t.Fatalf("ScanAndLoad failed: %v", err)
	}

	portal, ok := mgr.GetPortal("agent-public")
	if !ok {
		t.Fatalf("portal 'agent-public' not found")
	}

	if !portal.IsFileAllowed("notes/public.md") {
		t.Errorf("expected notes/public.md to be allowed")
	}
	if !portal.IsFileAllowed("notes/dsh-arch.md") {
		t.Errorf("expected notes/dsh-arch.md (via glob) to be allowed")
	}
	if portal.IsFileAllowed("notes/secret.md") {
		t.Errorf("expected notes/secret.md to be FORBIDDEN")
	}
	if portal.IsFileAllowed("../../../etc/passwd") {
		t.Errorf("expected directory traversal to be FORBIDDEN")
	}

	reqAllowed := httptest.NewRequest("GET", "/agent-public/file/notes/public.md", nil)
	wAllowed := httptest.NewRecorder()
	handlePortalRequest(portal, kbRoot, "file/notes/public.md", wAllowed, reqAllowed)
	if wAllowed.Code != http.StatusOK {
		t.Errorf("expected 200 OK for allowed file, got %d", wAllowed.Code)
	}
	if !strings.Contains(wAllowed.Body.String(), "Public Knowledge") {
		t.Errorf("expected content to contain 'Public Knowledge'")
	}

	reqForbidden := httptest.NewRequest("GET", "/agent-public/file/notes/secret.md", nil)
	wForbidden := httptest.NewRecorder()
	handlePortalRequest(portal, kbRoot, "file/notes/secret.md", wForbidden, reqForbidden)
	if wForbidden.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for unexposed file, got %d", wForbidden.Code)
	}

	reqTraversal := httptest.NewRequest("GET", "/agent-public/file/../server/main.go", nil)
	wTraversal := httptest.NewRecorder()
	handlePortalRequest(portal, kbRoot, "file/../server/main.go", wTraversal, reqTraversal)
	if wTraversal.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for traversal, got %d", wTraversal.Code)
	}
}

func TestFixedUploadEntrance(t *testing.T) {
	kbRoot, portalsDir, cleanup := setupMockKB(t)
	defer cleanup()

	mgr := NewPortalManager(kbRoot, portalsDir)
	_ = mgr.ScanAndLoad()
	portal, _ := mgr.GetPortal("agent-public")

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "agent-experience-1.md")
	if err != nil {
		t.Fatalf("CreateFormFile error: %v", err)
	}
	_, _ = part.Write([]byte("# Lessons Learned\nWe discovered that caching reduced latency by 40%.\n"))
	_ = writer.Close()

	reqUpload := httptest.NewRequest("POST", "/agent-public/upload", body)
	reqUpload.Header.Set("Content-Type", writer.FormDataContentType())
	wUpload := httptest.NewRecorder()

	HandlePortalUpload(portal, wUpload, reqUpload)
	if wUpload.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created for upload, got %d: %s", wUpload.Code, wUpload.Body.String())
	}

	var resp UploadResponse
	if err := json.NewDecoder(wUpload.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode upload JSON: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("expected status 'ok', got %s", resp.Status)
	}
	if resp.Portal != "agent-public" {
		t.Errorf("expected portal 'agent-public', got %s", resp.Portal)
	}

	savedPath := filepath.Join(portal.UploadDir, resp.Filename)
	content, err := os.ReadFile(savedPath)
	if err != nil {
		t.Fatalf("saved upload file not found: %v", err)
	}
	if !strings.Contains(string(content), "Lessons Learned") {
		t.Errorf("expected saved file to contain 'Lessons Learned'")
	}

	jsonPayload := `{"title":"Refinement Record","content":"Never pass raw tokens without sanitization."}`
	reqJSON := httptest.NewRequest("POST", "/agent-public/upload", strings.NewReader(jsonPayload))
	reqJSON.Header.Set("Content-Type", "application/json")
	wJSON := httptest.NewRecorder()

	HandlePortalUpload(portal, wJSON, reqJSON)
	if wJSON.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created for JSON upload, got %d: %s", wJSON.Code, wJSON.Body.String())
	}
}

func TestSearchGlobalAndScoped(t *testing.T) {
	kbRoot, portalsDir, cleanup := setupMockKB(t)
	defer cleanup()

	mgr := NewPortalManager(kbRoot, portalsDir)
	_ = mgr.ScanAndLoad()
	portal, _ := mgr.GetPortal("agent-public")

	globalResp, err := SearchKB(kbRoot, nil, "confidential", 10)
	if err != nil {
		t.Fatalf("SearchKB global error: %v", err)
	}
	if globalResp.TotalLines == 0 {
		t.Errorf("expected global search to find 'confidential' in secret.md")
	}

	scopedResp, err := SearchKB(kbRoot, portal, "confidential", 10)
	if err != nil {
		t.Fatalf("SearchKB scoped error: %v", err)
	}
	if scopedResp.TotalLines != 0 {
		t.Errorf("expected portal search NOT to reveal matches from secret.md, got %d", scopedResp.TotalLines)
	}

	publicResp, err := SearchKB(kbRoot, portal, "Public", 10)
	if err != nil {
		t.Fatalf("SearchKB public error: %v", err)
	}
	if publicResp.TotalLines == 0 {
		t.Errorf("expected portal search to find 'Public' in notes/public.md")
	}
}

func TestFixedPathEntryMarkdown(t *testing.T) {
	kbRoot, portalsDir, cleanup := setupMockKB(t)
	defer cleanup()

	mgr := NewPortalManager(kbRoot, portalsDir)
	_ = mgr.ScanAndLoad()
	portal, _ := mgr.GetPortal("agent-public")

	reqEntry := httptest.NewRequest("GET", "/agent-public/", nil)
	wEntry := httptest.NewRecorder()
	handlePortalRequest(portal, kbRoot, "", wEntry, reqEntry)

	if wEntry.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for fixed path entry, got %d", wEntry.Code)
	}

	body := wEntry.Body.String()
	if !strings.Contains(body, "Portal: `agent-public`") {
		t.Errorf("expected markdown to contain Portal header")
	}
	if !strings.Contains(body, "AGENTS.md") {
		t.Errorf("expected markdown to contain AGENTS.md section")
	}
	if !strings.Contains(body, "notes/public.md") {
		t.Errorf("expected markdown to list allowed notes/public.md")
	}
}

func TestDirectPathAndRAGStrictBoundary(t *testing.T) {
	kbRoot, portalsDir, cleanup := setupMockKB(t)
	defer cleanup()

	mgr := NewPortalManager(kbRoot, portalsDir)
	_ = mgr.ScanAndLoad()
	portal, _ := mgr.GetPortal("agent-public")

	// 1. Direct path hitting filelist.txt: /notes/public.md -> 200 OK with full text
	reqDirectAllowed := httptest.NewRequest("GET", "/agent-public/notes/public.md", nil)
	wDirectAllowed := httptest.NewRecorder()
	handlePortalRequest(portal, kbRoot, "notes/public.md", wDirectAllowed, reqDirectAllowed)
	if wDirectAllowed.Code != http.StatusOK {
		t.Errorf("expected 200 OK for direct allowed path, got %d", wDirectAllowed.Code)
	}
	if !strings.Contains(wDirectAllowed.Body.String(), "public knowledge about AI agents") {
		t.Errorf("expected full text to be served for direct path, got: %s", wDirectAllowed.Body.String())
	}

	// 2. Direct path NOT hitting filelist.txt: /notes/secret.md -> 403 Forbidden!
	reqDirectForbidden := httptest.NewRequest("GET", "/agent-public/notes/secret.md", nil)
	wDirectForbidden := httptest.NewRecorder()
	handlePortalRequest(portal, kbRoot, "notes/secret.md", wDirectForbidden, reqDirectForbidden)
	if wDirectForbidden.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for path not in filelist.txt, got %d", wDirectForbidden.Code)
	}

	// 3. RAG endpoint: query matching unexposed file MUST return 0 results
	reqRAGSecret := httptest.NewRequest("GET", "/agent-public/rag?q=confidential", nil)
	wRAGSecret := httptest.NewRecorder()
	handlePortalRequest(portal, kbRoot, "rag", wRAGSecret, reqRAGSecret)
	if wRAGSecret.Code != http.StatusOK {
		t.Errorf("expected 200 OK for RAG search, got %d", wRAGSecret.Code)
	}
	if strings.Contains(wRAGSecret.Body.String(), "secret.md") {
		t.Errorf("RAG MUST NOT expose content from secret.md")
	}

	// 4. RAG endpoint: query matching exposed file returns context text
	reqRAGPublic := httptest.NewRequest("GET", "/agent-public/rag?q=agents", nil)
	wRAGPublic := httptest.NewRecorder()
	handlePortalRequest(portal, kbRoot, "rag", wRAGPublic, reqRAGPublic)
	if wRAGPublic.Code != http.StatusOK {
		t.Errorf("expected 200 OK for RAG search, got %d", wRAGPublic.Code)
	}
	if !strings.Contains(wRAGPublic.Body.String(), "notes/public.md") {
		t.Errorf("RAG expected to include notes/public.md, got: %s", wRAGPublic.Body.String())
	}
}

func TestS3DualBucketingDomainAndPath(t *testing.T) {
	kbRoot, portalsDir, cleanup := setupMockKB(t)
	defer cleanup()

	mgr := NewPortalManager(kbRoot, portalsDir)
	_ = mgr.ScanAndLoad()

	// 1. Path-style resolution: http://127.0.0.1:8080/agent-public/notes/public.md
	reqPath := httptest.NewRequest("GET", "/agent-public/notes/public.md", nil)
	reqPath.Host = "127.0.0.1:8080"
	portalP, subPathP, okP := mgr.ResolvePortalAndSubpath(reqPath)
	if !okP || portalP.Name != "agent-public" || subPathP != "notes/public.md" {
		t.Errorf("Path-style resolution failed: ok=%v, portal=%v, subpath=%s", okP, portalP, subPathP)
	}

	// 2. Domain-style resolution (Subdomain): http://agent-public.kb.example.com:8080/notes/public.md
	reqDomain := httptest.NewRequest("GET", "/notes/public.md", nil)
	reqDomain.Host = "agent-public.kb.example.com:8080"
	portalD, subPathD, okD := mgr.ResolvePortalAndSubpath(reqDomain)
	if !okD || portalD.Name != "agent-public" || subPathD != "notes/public.md" {
		t.Errorf("Domain-style subdomain resolution failed: ok=%v, portal=%v, subpath=%s", okD, portalD, subPathD)
	}

	// 3. Domain-style resolution (Explicit domain in domain.txt)
	portalObj, _ := mgr.GetPortal("agent-public")
	_ = os.WriteFile(portalObj.DomainPath, []byte("kb-agent.internal\ncustom.domain.net\n"), 0644)
	_ = portalObj.Load(kbRoot)

	reqCustomDomain := httptest.NewRequest("GET", "/rag?q=agents", nil)
	reqCustomDomain.Host = "kb-agent.internal"
	portalC, subPathC, okC := mgr.ResolvePortalAndSubpath(reqCustomDomain)
	if !okC || portalC.Name != "agent-public" || subPathC != "rag?q=agents" && !strings.HasPrefix(subPathC, "rag") {
		t.Errorf("Domain-style custom domain resolution failed: ok=%v, portal=%v, subpath=%s", okC, portalC, subPathC)
	}
}
