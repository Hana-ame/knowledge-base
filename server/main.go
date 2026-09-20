package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	portFlag := flag.Int("port", 8080, "Port to listen on")
	listenFlag := flag.String("listen", "0.0.0.0", "IP address to listen on")
	kbRootFlag := flag.String("kb", "", "Path to knowledge base root directory (default: auto-detected)")
	portalsFlag := flag.String("portals", "", "Path to portals directory (default: {kb}/portals)")

	flag.Parse()

	kbRoot := *kbRootFlag
	if kbRoot == "" {
		if env := os.Getenv("KB_ROOT"); env != "" {
			kbRoot = env
		} else {
			if _, err := os.Stat("INDEX.md"); err == nil {
				kbRoot = "."
			} else if _, err := os.Stat("../INDEX.md"); err == nil {
				kbRoot = ".."
			} else {
				kbRoot = "/home/gekkasayu/knowledge-base"
			}
		}
	}
	absKBRoot, err := filepath.Abs(kbRoot)
	if err != nil {
		log.Fatalf("Invalid KB root path: %v", err)
	}

	portalsDir := *portalsFlag
	if portalsDir == "" {
		if env := os.Getenv("PORTALS_DIR"); env != "" {
			portalsDir = env
		} else {
			portalsDir = filepath.Join(absKBRoot, "portals")
		}
	}
	absPortalsDir, err := filepath.Abs(portalsDir)
	if err != nil {
		log.Fatalf("Invalid portals path: %v", err)
	}

	listenAddr := fmt.Sprintf("%s:%d", *listenFlag, *portFlag)

	log.Printf("==================================================")
	log.Printf("   Knowledge Base Multi-Portal Server (kb-server) ")
	log.Printf("==================================================")
	log.Printf("📖 Master KB Root : %s", absKBRoot)
	log.Printf("🚪 Portals Directory: %s", absPortalsDir)
	log.Printf("🌐 Listen Address   : http://%s", listenAddr)

	mgr := NewPortalManager(absKBRoot, absPortalsDir)
	if err := mgr.ScanAndLoad(); err != nil {
		log.Fatalf("Failed to scan portals: %v", err)
	}

	mux := http.NewServeMux()

	// 1. Global Search API: searches the entire master KB once
	mux.HandleFunc("/api/search", func(rw http.ResponseWriter, req *http.Request) {
		q := req.URL.Query().Get("q")
		resp, err := SearchKB(absKBRoot, nil, q, 100)
		if err != nil {
			http.Error(rw, fmt.Sprintf(`{"error":"Search failed: %v"}`, err), http.StatusInternalServerError)
			return
		}
		rw.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(rw).Encode(resp)
	})

	// 2. Portals listing API
	mux.HandleFunc("/api/portals", func(rw http.ResponseWriter, req *http.Request) {
		portals := mgr.ListPortals()
		type PortalInfo struct {
			Name        string   `json:"name"`
			FilesCount  int      `json:"files_count"`
			EntryURL    string   `json:"entry_url"`
			UploadURL   string   `json:"upload_url"`
			SearchURL   string   `json:"search_url"`
			SampleFiles []string `json:"sample_files"`
		}
		var list []PortalInfo
		for _, p := range portals {
			files := p.GetFileList()
			samples := files
			if len(samples) > 5 {
				samples = samples[:5]
			}
			list = append(list, PortalInfo{
				Name:        p.Name,
				FilesCount:  len(files),
				EntryURL:    fmt.Sprintf("/%s/", p.Name),
				UploadURL:   fmt.Sprintf("/%s/upload", p.Name),
				SearchURL:   fmt.Sprintf("/%s/search", p.Name),
				SampleFiles: samples,
			})
		}
		rw.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(rw).Encode(map[string]interface{}{
			"total_portals": len(list),
			"portals":       list,
		})
	})

	// 3. Portals reload API
	mux.HandleFunc("/api/portals/reload", func(rw http.ResponseWriter, req *http.Request) {
		if err := mgr.ScanAndLoad(); err != nil {
			http.Error(rw, fmt.Sprintf(`{"error":"Reload failed: %v"}`, err), http.StatusInternalServerError)
			return
		}
		rw.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(rw).Encode(map[string]interface{}{
			"status":  "ok",
			"message": "All portals reloaded successfully from disk.",
		})
	})

	// 4. Create Portal API (Dynamic portal creation)
	mux.HandleFunc("/api/portals/create", func(rw http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(rw, `{"error":"Method not allowed. Use POST."}`, http.StatusMethodNotAllowed)
			return
		}
		var payload struct {
			Name     string   `json:"name"`
			FileList []string `json:"filelist"`
			Index    string   `json:"index"`
			Agents   string   `json:"agents"`
			Domain   string   `json:"domain"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			http.Error(rw, fmt.Sprintf(`{"error":"Invalid JSON: %v"}`, err), http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(payload.Name)
		if name == "" || strings.Contains(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, "..") {
			http.Error(rw, `{"error":"Invalid portal name"}`, http.StatusBadRequest)
			return
		}
		targetDir := filepath.Join(absPortalsDir, name)
		if err := os.MkdirAll(filepath.Join(targetDir, "uploads"), 0755); err != nil {
			http.Error(rw, fmt.Sprintf(`{"error":"Failed to create portal directory: %v"}`, err), http.StatusInternalServerError)
			return
		}
		if len(payload.FileList) > 0 {
			_ = os.WriteFile(filepath.Join(targetDir, "filelist.txt"), []byte(strings.Join(payload.FileList, "\n")+"\n"), 0644)
		} else {
			_ = os.WriteFile(filepath.Join(targetDir, "filelist.txt"), []byte("# Whitelist for "+name+"\n"), 0644)
		}
		if payload.Index != "" {
			_ = os.WriteFile(filepath.Join(targetDir, "INDEX.md"), []byte(payload.Index), 0644)
		}
		if payload.Agents != "" {
			_ = os.WriteFile(filepath.Join(targetDir, "AGENTS.md"), []byte(payload.Agents), 0644)
		}
		if payload.Domain != "" {
			_ = os.WriteFile(filepath.Join(targetDir, "domain.txt"), []byte(payload.Domain+"\n"), 0644)
		}
		if err := mgr.ScanAndLoad(); err != nil {
			log.Printf("⚠️ ScanAndLoad warning after portal create: %v", err)
		}

		rw.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(rw).Encode(map[string]interface{}{
			"status":     "ok",
			"portal":     name,
			"message":    fmt.Sprintf("Portal '%s' created and loaded successfully.", name),
			"entry_url":  fmt.Sprintf("/%s/", name),
			"rag_url":    fmt.Sprintf("/%s/rag", name),
			"upload_url": fmt.Sprintf("/%s/upload", name),
		})
	})

	// 5. S3-Style Dual Routing (Domain-style & Path-style bucketing) + Root Dashboard
	mux.HandleFunc("/", func(rw http.ResponseWriter, req *http.Request) {
		// S3-style bucketing resolution
		if portal, subPath, ok := mgr.ResolvePortalAndSubpath(req); ok {
			handlePortalRequest(portal, absKBRoot, subPath, rw, req)
			return
		}

		path := strings.TrimPrefix(req.URL.Path, "/")
		if path == "" {
			// Global dashboard
			portals := mgr.ListPortals()
			rw.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(rw, `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>KB Multi-Portal Server</title>
<style>
body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; max-width: 900px; margin: 40px auto; padding: 0 20px; line-height: 1.6; background: #fafafa; color: #333; }
h1, h2 { color: #111; }
.card { background: #fff; border: 1px solid #e1e4e8; border-radius: 8px; padding: 20px; margin-bottom: 20px; box-shadow: 0 2px 4px rgba(0,0,0,0.02); }
a { color: #0366d6; text-decoration: none; font-weight: 500; }
a:hover { text-decoration: underline; }
code { background: #f6f8fa; padding: 2px 6px; border-radius: 4px; font-size: 0.9em; font-family: monospace; }
.tag { display: inline-block; background: #e1f5fe; color: #0277bd; padding: 3px 8px; border-radius: 12px; font-size: 0.8em; margin-left: 8px; }
.search-box { display: flex; gap: 10px; margin: 20px 0; }
.search-box input { flex: 1; padding: 10px 14px; font-size: 16px; border: 1px solid #ccc; border-radius: 6px; }
.search-box button { padding: 10px 20px; font-size: 16px; background: #2ea44f; color: white; border: none; border-radius: 6px; cursor: pointer; }
</style>
</head>
<body>
<h1>📚 知识库多入口服务 (KB Multi-Portal)</h1>
<p>S3 风格双模式分桶：既支持 <code>/&lt;portal&gt;/...</code> Path 分桶，也支持 <code>&lt;portal&gt;.domain/...</code> 域名分桶。</p>

<div class="card">
  <h2>🔍 全局知识库搜索 (Unified Search)</h2>
  <form action="/api/search" method="GET" class="search-box">
    <input type="text" name="q" placeholder="在全库所有 Markdown 笔记中搜索关键字..." required />
    <button type="submit">搜索全库</button>
  </form>
</div>

<div class="card">
  <h2>🚪 已激活的入口 (Portals / Buckets)</h2>
  <ul>`)
			for _, p := range portals {
				files := p.GetFileList()
				fmt.Fprintf(rw, `<li>
				<a href="/%s/"><strong>%s</strong></a> <span class="tag">%d 个受限文件</span>
				<ul>
					<li>🤖 Agent 上下文/入口指南: <code><a href="/%s/">GET /%s/</a></code> (自动组合守则、索引与授权清单)</li>
					<li>🔍 RAG 检索入口: <code><a href="/%s/rag?q=test">GET /%s/rag?q=...</a></code> (仅限当前入口白名单切片)</li>
					<li>📑 规范文档: <a href="/%s/AGENTS.md">AGENTS.md (守则)</a> | <a href="/%s/INDEX.md">INDEX.md (索引)</a> | <a href="/%s/filelist.txt">filelist.txt</a></li>
					<li>📤 经验上传入口: <code>POST /%s/upload</code> (沉淀至 uploads/)</li>
					<li>🌐 Domain 分桶访问: <code>http://%s.&lt;host&gt;/</code></li>
				</ul>
				</li><br>`, p.Name, p.Name, len(files), p.Name, p.Name, p.Name, p.Name, p.Name, p.Name, p.Name, p.Name, p.Name)
			}
			fmt.Fprintf(rw, `</ul>
</div>

<div class="card">
  <h2>🤖 Agent 专用入口操作速查 (Agent Quick Reference)</h2>
  <p>外部 AI Agent 或自动化脚本接入某入口时的固定端点：</p>
  <table style="width: 100%%; border-collapse: collapse; margin-top: 10px;">
    <thead>
      <tr style="background: #f6f8fa; text-align: left;">
        <th style="padding: 8px; border: 1px solid #e1e4e8;">功能</th>
        <th style="padding: 8px; border: 1px solid #e1e4e8;">Path 分桶模式</th>
        <th style="padding: 8px; border: 1px solid #e1e4e8;">Domain 分桶模式</th>
        <th style="padding: 8px; border: 1px solid #e1e4e8;">返回格式</th>
      </tr>
    </thead>
    <tbody>
      <tr>
        <td style="padding: 8px; border: 1px solid #e1e4e8;"><strong>1. 注入 Agent 上下文</strong></td>
        <td style="padding: 8px; border: 1px solid #e1e4e8;"><code>GET /{portal}/</code></td>
        <td style="padding: 8px; border: 1px solid #e1e4e8;"><code>GET /</code></td>
        <td style="padding: 8px; border: 1px solid #e1e4e8;">Markdown / JSON</td>
      </tr>
      <tr>
        <td style="padding: 8px; border: 1px solid #e1e4e8;"><strong>2. RAG 知识检索</strong></td>
        <td style="padding: 8px; border: 1px solid #e1e4e8;"><code>GET /{portal}/rag?q=...</code></td>
        <td style="padding: 8px; border: 1px solid #e1e4e8;"><code>GET /rag?q=...</code></td>
        <td style="padding: 8px; border: 1px solid #e1e4e8;">Markdown 上下文片段</td>
      </tr>
      <tr>
        <td style="padding: 8px; border: 1px solid #e1e4e8;"><strong>3. 读取授权文件正文</strong></td>
        <td style="padding: 8px; border: 1px solid #e1e4e8;"><code>GET /{portal}/{path}</code></td>
        <td style="padding: 8px; border: 1px solid #e1e4e8;"><code>GET /{path}</code></td>
        <td style="padding: 8px; border: 1px solid #e1e4e8;">Markdown 原文 (未授权返回403)</td>
      </tr>
      <tr>
        <td style="padding: 8px; border: 1px solid #e1e4e8;"><strong>4. 回传采坑经验优化库</strong></td>
        <td style="padding: 8px; border: 1px solid #e1e4e8;"><code>POST /{portal}/upload</code></td>
        <td style="padding: 8px; border: 1px solid #e1e4e8;"><code>POST /upload</code></td>
        <td style="padding: 8px; border: 1px solid #e1e4e8;">JSON 确认 (落盘至 uploads/)</td>
      </tr>
    </tbody>
  </table>
</div>
</body>
</html>`)
			return
		}

		http.Error(rw, fmt.Sprintf(`{"error":"Not Found: '%s' is neither an API endpoint nor a valid portal bucket."}`, req.URL.Path), http.StatusNotFound)
	})

	server := &http.Server{
		Addr:         listenAddr,
		Handler:      mux,
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Server failed: %v", err)
	}
}

func handlePortalRequest(portal *Portal, kbRoot, subPath string, rw http.ResponseWriter, req *http.Request) {
	// 1. Fixed Path Entrance: GET /{portal}/ or GET /{portal}/entry
	if subPath == "" || subPath == "entry" {
		if req.URL.Query().Get("format") == "json" || strings.Contains(req.Header.Get("Accept"), "application/json") {
			rw.Header().Set("Content-Type", "application/json; charset=utf-8")
			agentsContent, _ := os.ReadFile(portal.AgentsPath)
			indexContent, _ := os.ReadFile(portal.IndexPath)
			_ = json.NewEncoder(rw).Encode(map[string]interface{}{
				"portal":       portal.Name,
				"agents_rules": string(agentsContent),
				"index":        string(indexContent),
				"files":        portal.GetFileList(),
				"upload_url":   fmt.Sprintf("/%s/upload", portal.Name),
			})
			return
		}

		rw.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		md := FormatPortalEntryMarkdown(portal, kbRoot)
		_, _ = rw.Write([]byte(md))
		return
	}

	// 2. Direct guide files: AGENTS.md, INDEX.md, filelist.txt
	if subPath == "AGENTS.md" {
		http.ServeFile(rw, req, portal.AgentsPath)
		return
	}
	if subPath == "INDEX.md" {
		http.ServeFile(rw, req, portal.IndexPath)
		return
	}
	if subPath == "filelist.txt" {
		http.ServeFile(rw, req, portal.FileListPath)
		return
	}

	// 3. Fixed Upload Entrance: POST /{portal}/upload
	if subPath == "upload" {
		HandlePortalUpload(portal, rw, req)
		return
	}

	// 4. Portal Scoped Search & RAG: GET /{portal}/search?q=... or GET /{portal}/rag?q=...
	if subPath == "search" {
		q := req.URL.Query().Get("q")
		resp, err := SearchKB(kbRoot, portal, q, 50)
		if err != nil {
			http.Error(rw, fmt.Sprintf(`{"error":"Portal search failed: %v"}`, err), http.StatusInternalServerError)
			return
		}
		rw.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(rw).Encode(resp)
		return
	}

	if subPath == "rag" {
		q := req.URL.Query().Get("q")
		if q == "" {
			// Without query: return portal RAG entry guide
			rw.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			_, _ = rw.Write([]byte(FormatPortalEntryMarkdown(portal, kbRoot)))
			return
		}

		// Strictly retrieves snippets ONLY from files listed in filelist.txt
		ragResp, err := RAGSearch(kbRoot, portal, q, 30)
		if err != nil {
			http.Error(rw, fmt.Sprintf(`{"error":"RAG search failed: %v"}`, err), http.StatusInternalServerError)
			return
		}

		if req.URL.Query().Get("format") == "json" || strings.Contains(req.Header.Get("Accept"), "application/json") {
			rw.Header().Set("Content-Type", "application/json; charset=utf-8")
			_ = json.NewEncoder(rw).Encode(ragResp)
			return
		}

		rw.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = rw.Write([]byte(ragResp.ContextText))
		return
	}

	// 5. Uploads directory access: GET /{portal}/uploads or GET /{portal}/uploads/...
	if strings.HasPrefix(subPath, "uploads") {
		uploadFile := strings.TrimPrefix(subPath, "uploads")
		uploadFile = strings.TrimPrefix(uploadFile, "/")
		if uploadFile == "" {
			entries, _ := os.ReadDir(portal.UploadDir)
			var list []map[string]interface{}
			for _, e := range entries {
				info, _ := e.Info()
				list = append(list, map[string]interface{}{
					"name":       e.Name(),
					"size_bytes": info.Size(),
					"mod_time":   info.ModTime().Format(time.RFC3339),
					"url":        fmt.Sprintf("/%s/uploads/%s", portal.Name, e.Name()),
				})
			}
			rw.Header().Set("Content-Type", "application/json; charset=utf-8")
			_ = json.NewEncoder(rw).Encode(map[string]interface{}{
				"portal":  portal.Name,
				"total":   len(list),
				"uploads": list,
			})
			return
		}

		cleanTarget := filepath.Join(portal.UploadDir, filepath.Base(uploadFile))
		http.ServeFile(rw, req, cleanTarget)
		return
	}

	// 6. Path-based full-text serving: Path MUST match filelist.txt to serve full text!
	// Supports direct path (e.g. /{portal}/notes/xxx.md) or /{portal}/file/notes/xxx.md
	targetRel := subPath
	if strings.HasPrefix(targetRel, "file/") {
		targetRel = strings.TrimPrefix(targetRel, "file/")
	} else if strings.HasPrefix(targetRel, "raw/") {
		targetRel = strings.TrimPrefix(targetRel, "raw/")
	}
	targetRel = strings.TrimPrefix(targetRel, "/")

	// Security check: ONLY serve full text if path matches filelist.txt!
	if !portal.IsFileAllowed(targetRel) {
		rw.Header().Set("Content-Type", "application/json; charset=utf-8")
		rw.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(rw).Encode(map[string]interface{}{
			"error":     "Forbidden",
			"message":   fmt.Sprintf("Access denied: path '%s' does not match '%s/filelist.txt'. Full text cannot be served.", targetRel, portal.Name),
			"portal":    portal.Name,
			"requested": targetRel,
		})
		return
	}

	// Resolve target file in knowledge base root
	targetAbs := filepath.Join(kbRoot, filepath.FromSlash(targetRel))
	if !strings.HasPrefix(filepath.Clean(targetAbs), filepath.Clean(kbRoot)) {
		http.Error(rw, `{"error":"Directory traversal forbidden"}`, http.StatusForbidden)
		return
	}

	// Check if file exists; if not, try appending .md
	if _, err := os.Stat(targetAbs); os.IsNotExist(err) {
		if _, errMD := os.Stat(targetAbs + ".md"); errMD == nil {
			targetAbs = targetAbs + ".md"
		} else {
			http.Error(rw, fmt.Sprintf(`{"error":"File '%s' not found on server"}`, targetRel), http.StatusNotFound)
			return
		}
	}

	rw.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	http.ServeFile(rw, req, targetAbs)
}
