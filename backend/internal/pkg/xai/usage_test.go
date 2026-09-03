package xai

import "testing"

func TestIncludeIndependentReasoningTokens(t *testing.T) {
	tests := []struct {
		name                 string
		input, output, total int64
		reasoning            int64
		want                 int64
	}{
		{name: "xai chat shape", input: 32, output: 9, total: 135, reasoning: 94, want: 103},
		{name: "already folded", input: 32, output: 103, total: 135, reasoning: 94, want: 103},
		{name: "missing total", input: 32, output: 9, total: 0, reasoning: 94, want: 9},
		{name: "inconsistent total", input: 32, output: 9, total: 30, reasoning: 94, want: 9},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IncludeIndependentReasoningTokens(tt.input, tt.output, tt.total, tt.reasoning); got != tt.want {
				t.Fatalf("got %d, want %d", got, tt.want)
			}
		})
	}
}
