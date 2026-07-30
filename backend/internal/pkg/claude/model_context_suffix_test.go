package claude

import "testing"

func TestSplitModelContextSuffix(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		wantModel string
		want1M    bool
	}{
		// Claude Code CLI 对 auto 模式 Bash 安全分类器发出的正是这种写法。
		{"sonnet5 1m", "claude-sonnet-5[1m]", "claude-sonnet-5", true},
		{"opus5 1m", "claude-opus-5[1m]", "claude-opus-5", true},
		{"uppercase suffix", "claude-sonnet-5[1M]", "claude-sonnet-5", true},
		{"surrounding space", "  claude-opus-5[1m]  ", "claude-opus-5", true},

		{"plain model untouched", "claude-sonnet-5", "claude-sonnet-5", false},
		{"hyphen 1m untouched", "claude-sonnet-4-6-1m", "claude-sonnet-4-6-1m", false},
		{"empty", "", "", false},
		// 未知方括号后缀原样保留，让上游报错而不是网关猜语义。
		{"unknown suffix", "claude-sonnet-5[2m]", "claude-sonnet-5[2m]", false},
		{"suffix only", "[1m]", "[1m]", false},
		{"not a suffix", "claude[1m]-sonnet", "claude[1m]-sonnet", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotModel, got1M := SplitModelContextSuffix(tt.model)
			if gotModel != tt.wantModel || got1M != tt.want1M {
				t.Fatalf("SplitModelContextSuffix(%q) = (%q, %v), want (%q, %v)",
					tt.model, gotModel, got1M, tt.wantModel, tt.want1M)
			}
		})
	}
}

func TestEnsureContext1MBeta(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", BetaContext1M},
		{"whitespace only", "   ", BetaContext1M},
		{"already present", BetaContext1M, BetaContext1M},
		{"present among others", BetaOAuth + "," + BetaContext1M, BetaOAuth + "," + BetaContext1M},
		{"append", BetaOAuth, BetaOAuth + "," + BetaContext1M},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EnsureContext1MBeta(tt.input); got != tt.want {
				t.Fatalf("EnsureContext1MBeta(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
