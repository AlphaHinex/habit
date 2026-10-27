package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestSanitizeFileName(t *testing.T) {
	cases := map[string]string{
		"../etc/passwd":  "passwd",
		"a b#c?.pdf":     "a_b_c_.pdf",
		"报告 (final).pdf": "报告_(final).pdf",
		"":               "file",
		"..":             "file",
		"notes.md":       "notes.md",
	}
	for in, want := range cases {
		if got := sanitizeFileName(in); got != want {
			t.Fatalf("sanitizeFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplitFileName(t *testing.T) {
	base, ext := splitFileName("report.v2.pdf")
	if base != "report.v2" || ext != ".pdf" {
		t.Fatalf("got %q %q", base, ext)
	}
}

func TestImageResourceNamesAreUnique(t *testing.T) {
	now := time.Now()
	seen := map[string]bool{}
	for i := 0; i < 2000; i++ {
		if name := imageResourceName(now); seen[name] {
			t.Fatalf("duplicate resource name: %s", name)
		} else {
			seen[name] = true
		}
	}
}

func TestAttachNewContent(t *testing.T) {
	now := time.Date(2026, 10, 27, 9, 41, 0, 0, shanghaiLoc)
	got := attachNewContent("", "BODY", now)
	if !strings.HasPrefix(got, "# 2026年10月27日 09:41") || !strings.Contains(got, "BODY") || !strings.HasSuffix(got, "# Latest") {
		t.Fatalf("unexpected fresh output: %q", got)
	}
	if strings.HasPrefix(got, "\n") {
		t.Fatalf("fresh output should not start with a blank line: %q", got)
	}

	existing := "# 2026年10月26日 08:00 Monday\n\nOLD\n\n# Latest"
	got = attachNewContent(existing, "NEW", now)
	if strings.Count(got, "# Latest") != 1 {
		t.Fatalf("expect exactly one Latest marker: %q", got)
	}
	if !strings.Contains(got, "OLD") || !strings.Contains(got, "NEW") {
		t.Fatalf("old content lost: %q", got)
	}
}

func TestSeenSetDedup(t *testing.T) {
	s := newSeenSet(time.Hour)
	if !s.MarkIfNew("id-1") {
		t.Fatal("first sight should be new")
	}
	if s.MarkIfNew("id-1") {
		t.Fatal("second sight should be duplicate")
	}
	if !s.MarkIfNew("id-2") {
		t.Fatal("different key should be new")
	}
}

func TestIntervalLimiterSpacing(t *testing.T) {
	l := newIntervalLimiter(50 * time.Millisecond)
	ctx := context.Background()
	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := l.Wait(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("limiter did not space calls: %s", elapsed)
	}
}

func TestIntervalLimiterRespectsCancel(t *testing.T) {
	l := newIntervalLimiter(time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	if err := l.Wait(ctx); err != nil {
		t.Fatalf("first call should not wait: %v", err)
	}
	cancel()
	if err := l.Wait(ctx); err == nil {
		t.Fatal("expected context error")
	}
}
