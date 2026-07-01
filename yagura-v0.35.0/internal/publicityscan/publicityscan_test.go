package publicityscan

import "testing"

func ruleSet(fs []Finding) map[string]bool {
	m := map[string]bool{}
	for _, f := range fs {
		m[f.RuleID] = true
	}
	return m
}

func TestScan_AbsoluteHomePath(t *testing.T) {
	fs := Scan("see /Users/hiroro/work/skill and /home/alice/.config")
	if len(fs) != 2 {
		t.Fatalf("expected 2 home-path findings, got %d: %+v", len(fs), fs)
	}
	for _, f := range fs {
		if f.RuleID != "absolute-home-path" || f.Severity != SevHigh {
			t.Errorf("unexpected finding %+v", f)
		}
	}
}

func TestScan_GenericUsernamesIgnored(t *testing.T) {
	// CI/generic usernames must not be flagged.
	fs := Scan("/home/runner/work and /home/user/repo and /Users/ubuntu/x and /home/root/y")
	if len(fs) != 0 {
		t.Errorf("generic usernames should not be flagged, got %+v", fs)
	}
}

func TestScan_WindowsHomePath(t *testing.T) {
	fs := Scan(`path C:\Users\Hiro\Documents\skill`)
	if len(fs) != 1 || fs[0].RuleID != "absolute-home-path" {
		t.Errorf("expected windows home-path finding, got %+v", fs)
	}
}

func TestScan_InternalHostname(t *testing.T) {
	fs := Scan("connect to db01.internal:5432 and api.corp/health")
	if len(fs) != 2 {
		t.Fatalf("expected 2 internal-hostname findings, got %+v", fs)
	}
	for _, f := range fs {
		if f.RuleID != "internal-hostname" {
			t.Errorf("unexpected %+v", f)
		}
	}
}

func TestScan_InternalHostnameNoFilenameFalsePositive(t *testing.T) {
	// settings.local.json must NOT be flagged as an internal hostname.
	fs := Scan("edit .claude/settings.local.json and config.internal.yaml")
	if r := ruleSet(fs); r["internal-hostname"] {
		t.Errorf("filenames like settings.local.json must not be flagged: %+v", fs)
	}
}

func TestScan_PrivateIPButNotLoopback(t *testing.T) {
	fs := Scan("server 192.168.11.200 and 10.0.0.5 and 172.16.0.1 but bind 127.0.0.1:8090 and 8.8.8.8")
	got := 0
	for _, f := range fs {
		if f.RuleID == "private-ip" {
			got++
		}
	}
	if got != 3 {
		t.Errorf("expected 3 private IPs (loopback + public excluded), got %d: %+v", got, fs)
	}
}

func TestScan_EmailButNotExample(t *testing.T) {
	fs := Scan("contact hiroro@sonicgarden.jp not user@example.com nor noreply@anthropic.com")
	got := []Finding{}
	for _, f := range fs {
		if f.RuleID == "user-email" {
			got = append(got, f)
		}
	}
	if len(got) != 1 || got[0].Snippet != "hiroro@sonicgarden.jp" {
		t.Errorf("expected only the real email flagged, got %+v", got)
	}
}

func TestScan_CleanContent(t *testing.T) {
	clean := "Run `yagura` then open http://127.0.0.1:8090/dashboard. Use $HOME/.yagura/state."
	if fs := Scan(clean); len(fs) != 0 {
		t.Errorf("clean content should yield no findings, got %+v", fs)
	}
}

func TestSummarizeAndSort(t *testing.T) {
	fs := Scan("line\n/Users/bob/x has 10.0.0.1\nplain@example.com")
	s := Summarize(fs)
	if s.Total != 2 || s.BySeverity["HIGH"] != 1 || s.BySeverity["MEDIUM"] != 1 {
		t.Errorf("summary mismatch: %+v from %+v", s, fs)
	}
	SortFindings(fs)
	if fs[0].Line > fs[len(fs)-1].Line {
		t.Errorf("findings not sorted by line: %+v", fs)
	}
}

func TestScan_CIDRRangeNotFlagged(t *testing.T) {
	// CIDR range definitions (firewall docs) are not host leaks.
	fs := Scan("block -RemoteAddress 10.0.0.0/8 and 192.168.0.0/16 but host 10.0.0.5 leaks")
	got := 0
	for _, f := range fs {
		if f.RuleID == "private-ip" {
			got++
			if f.Snippet != "10.0.0.5" {
				t.Errorf("only the specific host should be flagged, got %q", f.Snippet)
			}
		}
	}
	if got != 1 {
		t.Errorf("expected 1 private-ip (CIDRs skipped), got %d: %+v", got, fs)
	}
}
