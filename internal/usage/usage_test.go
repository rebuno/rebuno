package usage

import (
	"encoding/json"
	"testing"
)

func envelope(t *testing.T, body string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"status":  200,
		"headers": map[string]string{"content-type": "application/json"},
		"body":    body,
	})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return b
}

func TestParse(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantIn      int
		wantOut     int
		wantMissing bool
	}{
		{
			name:    "openai chat completions",
			body:    `{"usage":{"prompt_tokens":120,"completion_tokens":45,"total_tokens":165}}`,
			wantIn:  120,
			wantOut: 45,
		},
		{
			name:    "openai responses",
			body:    `{"usage":{"input_tokens":300,"output_tokens":80,"output_tokens_details":{"reasoning_tokens":64}}}`,
			wantIn:  300,
			wantOut: 80,
		},
		{
			name:    "anthropic messages",
			body:    `{"usage":{"input_tokens":50,"output_tokens":22,"cache_read_input_tokens":900}}`,
			wantIn:  50,
			wantOut: 22,
		},
		{
			name:    "gemini",
			body:    `{"usageMetadata":{"promptTokenCount":17,"candidatesTokenCount":9,"totalTokenCount":26}}`,
			wantIn:  17,
			wantOut: 9,
		},
		{
			name:    "bedrock converse",
			body:    `{"usage":{"inputTokens":11,"outputTokens":3,"totalTokens":14}}`,
			wantIn:  11,
			wantOut: 3,
		},
		{
			name: "anthropic stream combines message_start and message_delta",
			body: "event: message_start\n" +
				`data: {"type":"message_start","message":{"usage":{"input_tokens":700,"output_tokens":1}}}` + "\n\n" +
				"event: message_delta\n" +
				`data: {"type":"message_delta","usage":{"output_tokens":250}}` + "\n\n",
			wantIn:  700,
			wantOut: 250,
		},
		{
			name: "openai stream reports usage in the final chunk",
			body: `data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n" +
				`data: {"choices":[],"usage":{"prompt_tokens":8,"completion_tokens":2}}` + "\n\n" +
				"data: [DONE]\n\n",
			wantIn:  8,
			wantOut: 2,
		},
		{
			name:        "unknown provider yields nothing",
			body:        `{"output":"hello","meta":{"latency_ms":12}}`,
			wantMissing: true,
		},
		{
			name:        "streaming without usage yields nothing",
			body:        `data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\ndata: [DONE]\n\n",
			wantMissing: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Parse(envelope(t, tt.body))
			if got.Input != tt.wantIn || got.Output != tt.wantOut {
				t.Fatalf("Parse = {in:%d out:%d}, want {in:%d out:%d}", got.Input, got.Output, tt.wantIn, tt.wantOut)
			}
			if got.Found() == tt.wantMissing {
				t.Fatalf("Found() = %v, want %v", got.Found(), !tt.wantMissing)
			}
		})
	}
}

func TestParseWithoutEnvelope(t *testing.T) {
	got := Parse([]byte(`{"usage":{"prompt_tokens":5,"completion_tokens":6}}`))
	if got.Input != 5 || got.Output != 6 {
		t.Fatalf("Parse = %+v, want {5 6}", got)
	}
}
