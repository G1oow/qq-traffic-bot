package qqbot

import (
	"encoding/json"
	"testing"
)

func TestFlexibleInt64(t *testing.T) {
	for _, input := range []string{`7200`, `"7200"`} {
		var value flexibleInt64
		if err := json.Unmarshal([]byte(input), &value); err != nil {
			t.Fatalf("input %s: %v", input, err)
		}
		if value != 7200 {
			t.Fatalf("input %s: got %d", input, value)
		}
	}
}
