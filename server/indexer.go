package main

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
)

var markdownLinkRegex = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
var wikilinkRegex = regexp.MustCompile(`\[\[([^\]]+)\]\]`)

// GenerateScopedIndex filters a master INDEX.md to only keep lines/links matching the portal's allowed files.
func GenerateScopedIndex(masterIndexPath string, portal *Portal) (string, error) {
	file, err := os.Open(masterIndexPath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# Scoped INDEX.md — 入口 [%s]\n\n", portal.Name))
	sb.WriteString(fmt.Sprintf("> 本文件基于总 INDEX.md 自动修订，仅展示当前入口 [%s] 暴露的文件子集。\n\n", portal.Name))

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ">") || trimmed == "" {
			sb.WriteString(line + "\n")
			continue
		}

		links := markdownLinkRegex.FindAllStringSubmatch(line, -1)
		wikilinks := wikilinkRegex.FindAllStringSubmatch(line, -1)

		if len(links) == 0 && len(wikilinks) == 0 {
			sb.WriteString(line + "\n")
			continue
		}

		allowed := false
		for _, m := range links {
			target := m[2]
			if idx := strings.Index(target, "#"); idx != -1 {
				target = target[:idx]
			}
			if target == "" || portal.IsFileAllowed(target) || portal.IsFileAllowed("indexes/"+target) {
				allowed = true
				break
			}
		}

		for _, m := range wikilinks {
			target := m[1]
			if portal.IsFileAllowed("notes/"+target+".md") || portal.IsFileAllowed("indexes/"+target+".md") || portal.IsFileAllowed(target) {
				allowed = true
				break
			}
		}

		if allowed {
			sb.WriteString(line + "\n")
		}
	}

	return sb.String(), scanner.Err()
}

// FormatPortalEntryMarkdown renders the comprehensive "固定path入口" context document.
func FormatPortalEntryMarkdown(portal *Portal, kbRoot string) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("# 知识库入口指南 — Portal: `%s`\n\n", portal.Name))
	sb.WriteString("> 这是一个基于单一真实知识库（Single KB）切分的专用入口。\n")
	sb.WriteString("> 本入口仅暴露知识库的授权子集文件。\n\n")

	sb.WriteString("## 1. AGENTS.md (守则与 RAG 操作指南)\n\n")
	if b, err := os.ReadFile(portal.AgentsPath); err == nil {
		sb.WriteString(string(b) + "\n\n")
	} else {
		sb.WriteString("*(暂无特定 AGENTS.md，遵循知识库全局默认行为)*\n\n")
	}

	sb.WriteString("## 2. INDEX.md (入口专属索引)\n\n")
	if b, err := os.ReadFile(portal.IndexPath); err == nil && len(b) > 0 {
		sb.WriteString(string(b) + "\n\n")
	} else {
		sb.WriteString("*(尚未配置专属 INDEX.md)*\n\n")
	}

	files := portal.GetFileList()
	sb.WriteString(fmt.Sprintf("## 3. 文件访问范围 (共 %d 项)\n\n", len(files)))
	for _, f := range files {
		sb.WriteString(fmt.Sprintf("- [`%s`](file/%s)\n", f, f))
	}
	sb.WriteString("\n")

	sb.WriteString("## 4. Agent 交互入口与 API 操作指南\n\n")

	sb.WriteString("### 4.1 Agent 初始提示词/上下文注入入口 (System Prompt Entry):\n")
	sb.WriteString("- **URL**: `GET /{portal}/` 或 `GET /{portal}/entry`\n")
	sb.WriteString("- **说明**: 获取当前入口的完整约束文档（包含守则、索引与授权清单）。若需要结构化 JSON 数据，请附加 Header `Accept: application/json` 或参数 `?format=json`。\n\n")

	sb.WriteString("### 4.2 RAG 智能检索问答入口 (Scoped RAG Context):\n")
	sb.WriteString("- **URL**: `GET /{portal}/rag?q=<查询问题>`\n")
	sb.WriteString("- **说明**: 严格限制仅在当前入口的 `filelist.txt` 文件子集内做切片检索，直接返回可拼入 Agent 上下文的 Markdown 引用段落，绝不越界暴露未授权知识。\n")
	sb.WriteString(fmt.Sprintf("```bash\ncurl \"http://<host>:8080/%s/rag?q=如何处理超时问题\"\n```\n\n", portal.Name))

	sb.WriteString("### 4.3 读取授权知识库文档原文 (Direct Full-Text Read):\n")
	sb.WriteString("- **URL**: `GET /{portal}/<相对路径>` 或 `GET /{portal}/file/<相对路径>`\n")
	sb.WriteString("- **说明**: 仅当路径命中 `filelist.txt` 白名单时返回原文；未命中或越界尝试将直接返回 `403 Forbidden`。\n")
	sb.WriteString(fmt.Sprintf("```bash\ncurl http://<host>:8080/%s/notes/sop-session-task-execution.md\n```\n\n", portal.Name))

	sb.WriteString("### 4.4 关键字列表搜索入口 (Keyword Search):\n")
	sb.WriteString("- **URL**: `GET /{portal}/search?q=<关键词>`\n")
	sb.WriteString(fmt.Sprintf("```bash\ncurl \"http://<host>:8080/%s/search?q=cache\"\n```\n\n", portal.Name))

	sb.WriteString("### 4.5 固定经验教训回传入口 (Lessons Learned Upload):\n")
	sb.WriteString("- **URL**: `POST /{portal}/upload`\n")
	sb.WriteString("- **说明**: 任何 Agent 在执行过程中发现的新坑、知识纠错或总结，均应通过此接口自动回传沉淀。\n")
	sb.WriteString(fmt.Sprintf("```bash\n# 方式 1: 直接上传 Markdown 文件\ncurl -X POST \"http://<host>:8080/%s/upload?filename=agent-lesson.md\" \\\n  -H \"Content-Type: text/markdown\" \\\n  --data-binary @lesson.md\n\n# 方式 2: JSON 结构化上传\ncurl -X POST \"http://<host>:8080/%s/upload\" \\\n  -H \"Content-Type: application/json\" \\\n  -d '{\"title\":\"XX服务排查教训\",\"content\":\"具体经验总结...\"}'\n```\n\n", portal.Name, portal.Name))

	return sb.String()
}
