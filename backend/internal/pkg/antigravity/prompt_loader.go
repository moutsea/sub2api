package antigravity

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// 提示词文件路径（相对于项目根目录）
const promptFilePath = "resources/anti.txt"

// 提示词缓存（启动时加载一次）
var (
	cachedPromptSections map[string]string
	promptCacheMutex     sync.RWMutex
	promptLoadOnce       sync.Once
)

// PromptSection 定义可用的提示词部分
type PromptSection string

const (
	SectionIdentity              PromptSection = "identity"
	SectionUserInformation       PromptSection = "user_information"
	SectionToolCalling           PromptSection = "tool_calling"
	SectionWebDevelopment        PromptSection = "web_application_development"
	SectionUserRules             PromptSection = "user_rules"
	SectionWorkflows             PromptSection = "workflows"
	SectionKnowledgeDiscovery    PromptSection = "knowledge_discovery"
	SectionPersistentContext     PromptSection = "persistent_context"
	SectionCommunicationStyle    PromptSection = "communication_style"
)

// loadPromptFile 加载并解析提示词文件
// 文件格式：使用 XML 风格的标签分隔不同部分，如 <identity>...</identity>
func loadPromptFile() error {
	// 查找提示词文件（支持多个可能的路径）
	possiblePaths := []string{
		promptFilePath,
		filepath.Join("backend", promptFilePath),
		filepath.Join("..", promptFilePath),
		filepath.Join("..", "..", promptFilePath),
		filepath.Join("..", "..", "..", promptFilePath),
		filepath.Join("internal", "pkg", "antigravity", "..", "..", "..", promptFilePath),
	}

	var content []byte
	var err error
	var usedPath string

	for _, path := range possiblePaths {
		content, err = os.ReadFile(path)
		if err == nil {
			usedPath = path
			break
		}
	}

	if err != nil {
		log.Printf("[PromptLoader] Warning: Failed to load prompt file: %v. Using fallback minimal prompt.", err)
		// 使用最小化的回退提示词
		cachedPromptSections = getFallbackPromptSections()
		return nil
	}

	log.Printf("[PromptLoader] Loaded prompt file from: %s (%d bytes)", usedPath, len(content))

	// 解析文件内容，提取各个部分
	sections := parsePromptSections(string(content))
	if len(sections) == 0 {
		log.Printf("[PromptLoader] Warning: No sections found in prompt file, using fallback")
		cachedPromptSections = getFallbackPromptSections()
		return nil
	}

	cachedPromptSections = sections
	log.Printf("[PromptLoader] Parsed %d sections: %v", len(sections), getSectionNames(sections))
	return nil
}

// parsePromptSections 解析提示词文件，提取各个 XML 标签部分
func parsePromptSections(content string) map[string]string {
	sections := make(map[string]string)

	// 匹配 XML 风格的标签：<tag_name>...</tag_name>
	// Go 不支持后向引用，所以需要手动匹配闭合标签
	lines := strings.Split(content, "\n")
	var currentTag string
	var currentContent strings.Builder
	inTag := false

	for _, line := range lines {
		// 检测开始标签
		if match := regexp.MustCompile(`^<([a-z_]+)>$`).FindStringSubmatch(strings.TrimSpace(line)); match != nil {
			if inTag {
				// 已经在一个标签中，保存之前的内容
				if currentTag != "" {
					sections[currentTag] = fmt.Sprintf("<%s>%s</%s>", currentTag, currentContent.String(), currentTag)
				}
				currentContent.Reset()
			}
			currentTag = match[1]
			inTag = true
			continue
		}

		// 检测结束标签
		if match := regexp.MustCompile(`^</([a-z_]+)>$`).FindStringSubmatch(strings.TrimSpace(line)); match != nil {
			if inTag && match[1] == currentTag {
				sections[currentTag] = fmt.Sprintf("<%s>%s</%s>", currentTag, currentContent.String(), currentTag)
				currentTag = ""
				currentContent.Reset()
				inTag = false
			}
			continue
		}

		// 标签内的内容
		if inTag {
			if currentContent.Len() > 0 {
				currentContent.WriteString("\n")
			}
			currentContent.WriteString(line)
		}
	}

	// 处理未闭合的标签
	if inTag && currentTag != "" {
		sections[currentTag] = fmt.Sprintf("<%s>%s</%s>", currentTag, currentContent.String(), currentTag)
	}

	return sections
}

// getSectionNames 获取所有部分的名称（用于日志）
func getSectionNames(sections map[string]string) []string {
	names := make([]string, 0, len(sections))
	for name := range sections {
		names = append(names, name)
	}
	return names
}

// getFallbackPromptSections 返回最小化的回退提示词
func getFallbackPromptSections() map[string]string {
	return map[string]string{
		string(SectionIdentity): `<identity>
You are Antigravity, a powerful agentic AI coding assistant designed by the Google Deepmind team working on Advanced Agentic Coding.
You are pair programming with a USER to solve their coding task.
</identity>`,
		string(SectionToolCalling): `<tool_calling>
Call tools as you normally would. When using tools that accept file path arguments, ALWAYS use the absolute file path.
</tool_calling>`,
		string(SectionWebDevelopment): `<web_application_development>
Build beautiful, modern web applications using best practices. Use HTML for structure, JavaScript for logic, and Vanilla CSS for styling.
</web_application_development>`,
		string(SectionCommunicationStyle): `<communication_style>
- **Formatting**: Format responses in github-style markdown.
- **Helpfulness**: Respond like a helpful software engineer.
- **Ask for clarification**: If unsure about intent, ask rather than assuming.
</communication_style>`,
	}
}

// GetPromptSection 获取指定的提示词部分
// 如果尚未加载，会自动加载文件
func GetPromptSection(section PromptSection) string {
	// 确保提示词文件已加载
	promptLoadOnce.Do(func() {
		if err := loadPromptFile(); err != nil {
			log.Printf("[PromptLoader] Error loading prompt file: %v", err)
		}
	})

	promptCacheMutex.RLock()
	defer promptCacheMutex.RUnlock()

	if content, ok := cachedPromptSections[string(section)]; ok {
		return content
	}

	log.Printf("[PromptLoader] Warning: Section %q not found in prompt file", section)
	return ""
}

// BuildAntigravityPrompt 根据场景动态组装 Antigravity 提示词
// Options:
//   - hasTools: 是否包含工具定义（决定是否添加 tool_calling 部分）
//   - hasMCPTools: 是否包含 MCP 工具（决定是否添加 MCP XML 协议）
//   - includeWebDev: 是否包含 Web 开发指导（可选，默认不包含以减少 token 消耗）
//   - ignoreMode: 是否启用忽略模式（将提示词包装在 ignore 标签中）
func BuildAntigravityPrompt(hasTools bool, hasMCPTools bool, includeWebDev bool, ignoreMode bool) string {
	var sb strings.Builder

	// 1. 核心身份（必需）
	sb.WriteString(GetPromptSection(SectionIdentity))
	sb.WriteString("\n")

	// 2. 工具调用指导（仅在有工具时添加）
	if hasTools {
		sb.WriteString(GetPromptSection(SectionToolCalling))
		sb.WriteString("\n")
	}

	// 3. Web 开发指导（可选）
	if includeWebDev {
		sb.WriteString(GetPromptSection(SectionWebDevelopment))
		sb.WriteString("\n")
	}

	// 4. 沟通风格（必需）
	sb.WriteString(GetPromptSection(SectionCommunicationStyle))
	sb.WriteString("\n")

	// 5. MCP XML 协议（仅在有 MCP 工具时添加）
	if hasMCPTools {
		sb.WriteString(mcpXMLProtocol)
		sb.WriteString("\n")
	}

	// 6. 结束标记
	sb.WriteString("--- [SYSTEM_PROMPT_END] ---")

	prompt := sb.String()

	// 7. 忽略模式（如果启用）
	if ignoreMode {
		prompt = buildIgnoreInstruction(prompt)
	}

	return prompt
}

// buildIgnoreInstruction 构建忽略指令，让 AI 忽略 Antigravity 身份提示词
// 用于某些特殊场景，例如用户明确不想要 Antigravity 身份时
func buildIgnoreInstruction(prompt string) string {
	return "Please ignore the following instructions:\n[IGNORE_START]\n" + prompt + "\n[IGNORE_END]"
}

// ReloadPromptFile 重新加载提示词文件（用于热更新）
// 调用此函数后，下次 GetPromptSection 会使用新的内容
func ReloadPromptFile() error {
	promptCacheMutex.Lock()
	defer promptCacheMutex.Unlock()

	// 重置 sync.Once，强制重新加载
	promptLoadOnce = sync.Once{}

	return loadPromptFile()
}
