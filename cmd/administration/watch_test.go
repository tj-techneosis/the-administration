package main

import "testing"

func TestShortNamesDropWhatTheyShare(t *testing.T) {
	got := shortNames([]string{"acme-shop-phone-agent", "acme-shop-pricing-critic-agent"})
	if got["acme-shop-phone-agent"] != "phone" || got["acme-shop-pricing-critic-agent"] != "pricing-critic" {
		t.Fatalf("got %v", got)
	}
	if one := shortNames([]string{"reader"}); one["reader"] != "" {
		t.Fatalf("one professional needs no column, got %v", one)
	}
}

func TestToolLabelShowsTheTarget(t *testing.T) {
	cases := map[string]string{
		toolLabel("mcp__playwright__browser_navigate", map[string]any{"url": "http://x/reserve"}): "navigate http://x/reserve",
		toolLabel("mcp__playwright__browser_resize", map[string]any{"width": 320, "height": 568}): "resize 320x568",
		toolLabel("Read", map[string]any{"file_path": "src/a.ts"}):                                "Read src/a.ts",
		toolLabel("Read", map[string]any{"file_path": "/var/tmp/c/r1/app/src/lib/a.ts"}):          "Read …/src/lib/a.ts",
		toolLabel("mcp__playwright__browser_snapshot", nil):                                       "snapshot",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestFirstLineSkipsPlaywrightsErrorHeading(t *testing.T) {
	if got := firstLine("### Error\nError: Socket directory path is too long"); got != "Error: Socket directory path is too long" {
		t.Fatalf("got %q", got)
	}
}

func TestToolLabelShowsTheCommandServersArgs(t *testing.T) {
	got := toolLabel("mcp__eq_linux_cli__run_imgconv", map[string]any{"args": []any{"photos/a b.heic", "-f", "png"}})
	if want := `run_imgconv "photos/a b.heic" -f png`; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWatchShowsARecordedFindingAsOne(t *testing.T) {
	line := []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__house__record_finding","input":{"what":"the total is $5 high","where":"/cart","mark":"NEW"}}]}}`)
	got := watchLines(line)
	if len(got) != 1 || got[0] != "★ FINDING: NEW: the total is $5 high (at /cart)" {
		t.Fatalf("%q", got)
	}
}
