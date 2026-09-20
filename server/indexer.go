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

	sb.WriteString("## 4. API 端点操作指南\n\n")
	sb.WriteString("### 读取单个文件 (严格限制在子集内):\n")
	sb.WriteString(fmt.Sprintf("```bash\ncurl http://<host>:%s/%s/file/<relPath>\n```\n\n", "8080", portal.Name))

	sb.WriteString("### 入口子集内搜索:\n")
	sb.WriteString(fmt.Sprintf("```bash\ncurl \"http://<host>:%s/%s/search?q=<keyword>\"\n```\n\n", "8080", portal.Name))

	sb.WriteString("### 固定上传入口 (回传经验教训优化知识库):\n")
	sb.WriteString("使用过此入口的 Agent 或人类可将实测总结、采坑教训或补充材料直接回传：\n\n")
	sb.WriteString(fmt.Sprintf("```bash\n# 1. 直接上传 Markdown / 文本:\ncurl -X POST \"http://<host>:%s/%s/upload?filename=lesson-learned.md\" \\\n  -H \"Content-Type: text/markdown\" \\\n  --data-binary @my-experience.md\n\n# 2. JSON 结构化上传:\ncurl -X POST \"http://<host>:%s/%s/upload\" \\\n  -H \"Content-Type: application/json\" \\\n  -d '{\"title\":\"测试采坑记录\",\"content\":\"...\"}'\n```\n\n", "8080", portal.Name, "8080", portal.Name))

	return sb.String()
}
