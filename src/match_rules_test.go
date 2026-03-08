package main

import (
	"regexp"
	"strings"
	"testing"
)

func TestNormalizeMatchRuleWildcardCivitai(t *testing.T) {
	entry := normalizeMatchRuleEntry("*.civitai.*")
	if !strings.HasPrefix(entry, "regexp:") {
		t.Fatalf("expected regexp rule, got %q", entry)
	}
	raw := strings.TrimPrefix(entry, "regexp:")
	re, err := regexp.Compile(raw)
	if err != nil {
		t.Fatalf("compile regexp failed: %v", err)
	}
	if !re.MatchString("image.civitai.com") {
		t.Fatalf("expected image.civitai.com to match %q", raw)
	}
	if !re.MatchString("civitai.com") {
		t.Fatalf("expected civitai.com to match %q", raw)
	}
	if re.MatchString("example.com") {
		t.Fatalf("did not expect example.com to match %q", raw)
	}
}
