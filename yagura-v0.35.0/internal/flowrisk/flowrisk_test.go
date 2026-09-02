package flowrisk

import "testing"

func steps(caps ...string) []Step {
	out := make([]Step, len(caps))
	for i, c := range caps {
		out[i] = Step{Name: c + "-op", Capability: c}
	}
	return out
}

func kinds(fr []FlowRisk) map[string]bool {
	m := map[string]bool{}
	for _, f := range fr {
		m[f.Kind] = true
	}
	return m
}

func TestAnalyze_Exfiltration(t *testing.T) {
	// secret read THEN network send = exfiltration kill chain.
	fr := Analyze(steps(CapSecretRead, CapNetwork))
	if !kinds(fr)["exfiltration"] {
		t.Errorf("expected exfiltration flow, got %+v", fr)
	}
}

func TestAnalyze_OrderMatters(t *testing.T) {
	// network BEFORE secret read is not the exfiltration pattern.
	fr := Analyze(steps(CapNetwork, CapSecretRead))
	if kinds(fr)["exfiltration"] {
		t.Errorf("network-then-secret is not exfiltration, got %+v", fr)
	}
}

func TestAnalyze_InjectionToExec(t *testing.T) {
	fr := Analyze(steps(CapFetchUntrusted, CapExec))
	if !kinds(fr)["injection-to-exec"] {
		t.Errorf("expected injection-to-exec, got %+v", fr)
	}
}

func TestAnalyze_UntrustedToDisk(t *testing.T) {
	fr := Analyze(steps(CapFetchUntrusted, CapWrite))
	if !kinds(fr)["untrusted-to-disk"] {
		t.Errorf("expected untrusted-to-disk, got %+v", fr)
	}
}

func TestAnalyze_CleanSequence(t *testing.T) {
	if fr := Analyze(steps(CapNetwork, "other", CapWrite)); len(fr) != 0 {
		t.Errorf("no source→sink pattern present, got %+v", fr)
	}
}

// the reported pair is the earliest source and the earliest sink after it.
func TestAnalyze_EarliestPair(t *testing.T) {
	s := steps(CapSecretRead, "other", CapNetwork, CapNetwork)
	fr := Analyze(s)
	var ex *FlowRisk
	for i := range fr {
		if fr[i].Kind == "exfiltration" {
			ex = &fr[i]
		}
	}
	if ex == nil {
		t.Fatal("expected exfiltration flow")
	}
	if ex.From != 0 || ex.To != 2 {
		t.Errorf("expected earliest pair (0→2), got %d→%d", ex.From, ex.To)
	}
}

func TestClassifyTool(t *testing.T) {
	cases := map[string]string{
		"os.Getenv":    CapSecretRead,
		"read_secret":  CapSecretRead,
		"http.Post":    CapNetwork,
		"sendEmail":    CapNetwork,
		"exec.Command": CapExec,
		"run_shell":    CapExec,
		"web_fetch":    CapFetchUntrusted,
		"downloadURL":  CapFetchUntrusted,
		"os.WriteFile": CapWrite,
		"add":          "other",
	}
	for name, want := range cases {
		if got := ClassifyTool(name); got != want {
			t.Errorf("ClassifyTool(%q) = %q, want %q", name, got, want)
		}
	}
}
