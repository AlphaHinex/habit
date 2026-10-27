package main

import (
	"context"
	"errors"
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

func TestSplitMessageContentSingleTypes(t *testing.T) {
	parts, err := splitMessageContent("text", `{"text":"hello"}`)
	if err != nil || len(parts) != 1 || parts[0].kind != partText || parts[0].text != "hello" {
		t.Fatalf("text: parts=%+v err=%v", parts, err)
	}

	parts, err = splitMessageContent("image", `{"image_key":"img_1"}`)
	if err != nil || len(parts) != 1 || parts[0].imageKey != "img_1" {
		t.Fatalf("image: parts=%+v err=%v", parts, err)
	}

	parts, err = splitMessageContent("file", `{"file_key":"fk_1","file_name":"a.pdf"}`)
	if err != nil || len(parts) != 1 || parts[0].fileKey != "fk_1" || parts[0].fileName != "a.pdf" {
		t.Fatalf("file: parts=%+v err=%v", parts, err)
	}

	parts, err = splitMessageContent("media", `{"file_key":"fk_2","image_key":"img_2","file_name":"v.mp4"}`)
	if err != nil || len(parts) != 1 || parts[0].kind != partMedia || parts[0].imageKey != "img_2" {
		t.Fatalf("media: parts=%+v err=%v", parts, err)
	}
}

func TestSplitMessageContentPost(t *testing.T) {
	raw := `{"title":"日报","content":[[{"tag":"text","text":"今天进度"},{"tag":"a","text":"链接","href":"https://example.com"}],[{"tag":"img","image_key":"img_1"},{"tag":"media","file_key":"fk_1"}],[{"tag":"file","file_key":"fk_2","file_name":"报告.pdf"}]]}`

	parts, err := splitMessageContent("post", raw)
	if err != nil {
		t.Fatal(err)
	}

	want := []struct{ kind, text, key string }{
		{partText, "日报", ""},
		{partText, "今天进度[链接](https://example.com)", ""},
		{partImage, "", "img_1"},
		{partMedia, "", "fk_1"},
		{partFile, "", "fk_2"},
	}

	if len(parts) != len(want) {
		t.Fatalf("got %d parts, want %d: %+v", len(parts), len(want), parts)
	}
	for i, w := range want {
		if parts[i].kind != w.kind {
			t.Fatalf("part %d kind = %q, want %q", i, parts[i].kind, w.kind)
		}
		if w.text != "" && parts[i].text != w.text {
			t.Fatalf("part %d text = %q, want %q", i, parts[i].text, w.text)
		}
		if w.key != "" && parts[i].fileKey != w.key && parts[i].imageKey != w.key {
			t.Fatalf("part %d key = %q/%q, want %q", i, parts[i].fileKey, parts[i].imageKey, w.key)
		}
	}
	if parts[4].fileName != "报告.pdf" {
		t.Fatalf("file name = %q, want 报告.pdf", parts[4].fileName)
	}
}

func TestSplitMessageContentPostLocale(t *testing.T) {
	raw := `{"zh_cn":{"title":"","content":[[{"tag":"text","text":"hi"},{"tag":"img","image_key":"img_9"}]]}}`

	parts, err := splitMessageContent("post", raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 || parts[0].text != "hi" || parts[1].imageKey != "img_9" {
		t.Fatalf("unexpected parts: %+v", parts)
	}
}

func TestSplitMessageContentCodeAndDivider(t *testing.T) {
	raw := `{"content":[[{"tag":"code_block","language":"go","text":"fmt.Println(1)"},{"tag":"hr"}]]}`

	parts, err := splitMessageContent("post", raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 || parts[0].kind != partCode || parts[0].language != "go" {
		t.Fatalf("unexpected parts: %+v", parts)
	}
	if parts[1].kind != partText || parts[1].text != "---" {
		t.Fatalf("unexpected parts: %+v", parts)
	}
}

func TestSplitMessageContentErrors(t *testing.T) {
	if _, err := splitMessageContent("sticker", `{"file_key":"x"}`); !errors.Is(err, errUnsupportedType) {
		t.Fatalf("want errUnsupportedType, got %v", err)
	}
	if _, err := splitMessageContent("post", `{"title":"","content":[]}`); !errors.Is(err, errInvalidContent) {
		t.Fatalf("want errInvalidContent, got %v", err)
	}
	if _, err := splitMessageContent("file", `{"file_key":"x"}`); !errors.Is(err, errInvalidContent) {
		t.Fatalf("want errInvalidContent, got %v", err)
	}
}

func TestRenderTextPart(t *testing.T) {
	now := time.Now()

	got, err := renderPart(context.Background(), &messageJob{msgType: "text"}, contentPart{kind: partText, text: "hi"}, now)
	if err != nil || got != "```\nhi\n```" {
		t.Fatalf("plain text message should stay fenced: %q err=%v", got, err)
	}

	got, err = renderPart(context.Background(), &messageJob{msgType: "post"}, contentPart{kind: partText, text: "hi"}, now)
	if err != nil || got != "hi" {
		t.Fatalf("post text should stay raw: %q err=%v", got, err)
	}
}
