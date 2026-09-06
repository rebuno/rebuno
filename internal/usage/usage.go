package usage

import (
	"encoding/json"
	"strings"
)

type Tokens struct {
	Input  int
	Output int
}

func (t Tokens) Found() bool { return t.Input > 0 || t.Output > 0 }

var inputKeys = map[string]bool{
	"prompt_tokens":     true,
	"input_tokens":      true,
	"inputTokens":       true,
	"promptTokenCount":  true,
	"prompt_eval_count": true,
}

var outputKeys = map[string]bool{
	"completion_tokens":    true,
	"output_tokens":        true,
	"outputTokens":         true,
	"candidatesTokenCount": true,
	"eval_count":           true,
}

// Each direction takes the maximum seen, so a stream reporting input and output
// in separate events resolves to a single pair.
func Parse(result []byte) Tokens {
	var envelope struct {
		Body string `json:"body"`
	}
	payload := result
	if err := json.Unmarshal(result, &envelope); err == nil && envelope.Body != "" {
		payload = []byte(envelope.Body)
	}

	var found Tokens
	for _, obj := range jsonObjects(payload) {
		scan(obj, &found)
	}
	return found
}

func jsonObjects(payload []byte) []any {
	var single any
	if err := json.Unmarshal(payload, &single); err == nil {
		return []any{single}
	}

	var objs []any
	for _, line := range strings.Split(string(payload), "\n") {
		data, ok := strings.CutPrefix(strings.TrimSpace(line), "data:")
		if !ok {
			continue
		}
		var obj any
		if err := json.Unmarshal([]byte(strings.TrimSpace(data)), &obj); err == nil {
			objs = append(objs, obj)
		}
	}
	return objs
}

func scan(node any, found *Tokens) {
	switch n := node.(type) {
	case map[string]any:
		for key, value := range n {
			if num, ok := value.(float64); ok {
				if inputKeys[key] && int(num) > found.Input {
					found.Input = int(num)
				}
				if outputKeys[key] && int(num) > found.Output {
					found.Output = int(num)
				}
				continue
			}
			scan(value, found)
		}
	case []any:
		for _, item := range n {
			scan(item, found)
		}
	}
}
