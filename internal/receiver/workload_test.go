package receiver

import (
	"strings"
	"testing"
)

func TestRestrictedEnvironmentIncludesOnlyMappedSecrets(t *testing.T) {
	t.Setenv("ONTHEGO_SENTINEL_SECRET", "must-not-leak")
	t.Setenv("HF_TOKEN", "mapped-token")
	values := restrictedEnvironment(map[string]string{"HF_TOKEN": "hf-daytona"})
	joined := strings.Join(values, "\n")
	if strings.Contains(joined, "must-not-leak") || strings.Contains(joined, "ONTHEGO_SENTINEL_SECRET") {
		t.Fatalf("unmapped environment secret leaked: %s", joined)
	}
	if !strings.Contains(joined, "HF_TOKEN=mapped-token") {
		t.Fatalf("mapped secret missing: %s", joined)
	}
}
