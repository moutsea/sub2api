package antigravity

import (
	"strings"
	"testing"
)

func TestBuildAntigravityPrompt(t *testing.T) {
	tests := []struct {
		name           string
		hasTools       bool
		hasMCPTools    bool
		includeWebDev  bool
		ignoreMode     bool
		wantContains   []string
		wantNotContain []string
	}{
		{
			name:        "Minimal prompt (no tools, no MCP, no web dev)",
			hasTools:    false,
			hasMCPTools: false,
			includeWebDev: false,
			ignoreMode: false,
			wantContains: []string{
				"<identity>",
				"Antigravity",
				"<communication_style>",
				"SYSTEM_PROMPT_END",
			},
			wantNotContain: []string{
				"<tool_calling>",
				"<web_application_development>",
				"MCP XML",
				"IGNORE",
			},
		},
		{
			name:        "With tools (no MCP)",
			hasTools:    true,
			hasMCPTools: false,
			includeWebDev: false,
			ignoreMode: false,
			wantContains: []string{
				"<identity>",
				"<tool_calling>",
				"<communication_style>",
			},
			wantNotContain: []string{
				"<web_application_development>",
				"MCP XML",
			},
		},
		{
			name:        "With MCP tools",
			hasTools:    true,
			hasMCPTools: true,
			includeWebDev: false,
			ignoreMode: false,
			wantContains: []string{
				"<identity>",
				"<tool_calling>",
				"MCP XML",
				"<communication_style>",
			},
			wantNotContain: []string{
				"<web_application_development>",
			},
		},
		{
			name:        "With web development",
			hasTools:    true,
			hasMCPTools: false,
			includeWebDev: true,
			ignoreMode: false,
			wantContains: []string{
				"<identity>",
				"<tool_calling>",
				"<web_application_development>",
				"<communication_style>",
			},
			wantNotContain: []string{
				"MCP XML",
			},
		},
		{
			name:        "Ignore mode",
			hasTools:    false,
			hasMCPTools: false,
			includeWebDev: false,
			ignoreMode: true,
			wantContains: []string{
				"IGNORE",
				"<identity>",
			},
			wantNotContain: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildAntigravityPrompt(tt.hasTools, tt.hasMCPTools, tt.includeWebDev, tt.ignoreMode)

			// 验证包含的内容
			for _, want := range tt.wantContains {
				if !strings.Contains(result, want) {
					t.Errorf("BuildAntigravityPrompt() missing expected content %q\nGot: %s", want, result)
				}
			}

			// 验证不包含的内容
			for _, notWant := range tt.wantNotContain {
				if strings.Contains(result, notWant) {
					t.Errorf("BuildAntigravityPrompt() contains unexpected content %q\nGot: %s", notWant, result)
				}
			}
		})
	}
}

func TestGetPromptSection(t *testing.T) {
	tests := []struct {
		section      PromptSection
		wantContains string
	}{
		{
			section:      SectionIdentity,
			wantContains: "Antigravity",
		},
		{
			section:      SectionToolCalling,
			wantContains: "tool",
		},
		{
			section:      SectionCommunicationStyle,
			wantContains: "Formatting",
		},
	}

	for _, tt := range tests {
		t.Run(string(tt.section), func(t *testing.T) {
			result := GetPromptSection(tt.section)
			if result == "" {
				t.Errorf("GetPromptSection(%q) returned empty string", tt.section)
			}
			if !strings.Contains(strings.ToLower(result), strings.ToLower(tt.wantContains)) {
				t.Errorf("GetPromptSection(%q) missing expected content %q\nGot: %s", tt.section, tt.wantContains, result)
			}
		})
	}
}

func TestDefaultIdentityPatch(t *testing.T) {
	tests := []struct {
		name        string
		hasTools    bool
		hasMCPTools bool
		wantContains []string
	}{
		{
			name:        "No tools, no MCP",
			hasTools:    false,
			hasMCPTools: false,
			wantContains: []string{
				"<identity>",
				"<communication_style>",
			},
		},
		{
			name:        "With tools, no MCP",
			hasTools:    true,
			hasMCPTools: false,
			wantContains: []string{
				"<identity>",
				"<tool_calling>",
				"<communication_style>",
			},
		},
		{
			name:        "With MCP tools",
			hasTools:    true,
			hasMCPTools: true,
			wantContains: []string{
				"<identity>",
				"<tool_calling>",
				"MCP XML",
				"<communication_style>",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := defaultIdentityPatch(tt.hasTools, tt.hasMCPTools)

			for _, want := range tt.wantContains {
				if !strings.Contains(result, want) {
					t.Errorf("defaultIdentityPatch() missing expected content %q", want)
				}
			}
		})
	}
}

func TestParsePromptSections(t *testing.T) {
	content := `<identity>
You are Antigravity
</identity>
<tool_calling>
Use tools wisely
</tool_calling>
<communication_style>
Be helpful
</communication_style>`

	sections := parsePromptSections(content)

	expectedSections := []string{"identity", "tool_calling", "communication_style"}
	for _, section := range expectedSections {
		if _, ok := sections[section]; !ok {
			t.Errorf("parsePromptSections() missing section %q", section)
		}
	}

	// 验证内容完整性（包含 XML 标签）
	if !strings.Contains(sections["identity"], "<identity>") {
		t.Errorf("parsePromptSections() should preserve XML tags")
	}
	if !strings.Contains(sections["identity"], "Antigravity") {
		t.Errorf("parsePromptSections() should preserve content")
	}
}
