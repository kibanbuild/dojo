package repo

import "testing"

func rows(status map[string]string) []DoctorCheck {
	var out []DoctorCheck
	for id, s := range status {
		out = append(out, DoctorCheck{ID: id, Status: s})
	}
	return out
}

func allOK(ids []string) map[string]string {
	m := map[string]string{}
	for _, id := range ids {
		m[id] = "ok"
	}
	return m
}

func TestStepReadiness(t *testing.T) {
	both := func() map[string]string {
		m := allOK(o3Required)
		for k, v := range allOK(o4Required) {
			m[k] = v
		}
		return m
	}
	with := func(m map[string]string, id, status string) map[string]string {
		if status == "" {
			delete(m, id)
		} else {
			m[id] = status
		}
		return m
	}
	tests := []struct {
		name           string
		checks         []DoctorCheck
		wantO3, wantO4 bool
	}{
		{"no rows is not ready", nil, false, false},
		{"all ok", rows(both()), true, true},
		{"only machine rows", rows(allOK(o3Required)), true, false},
		{"warn does not block", rows(with(both(), "memory", "warn")), true, true},
		{"unknown does not block", rows(with(both(), "ruleset", "unknown")), true, true},
		{"a machine fail blocks O3 only", rows(with(both(), "disk", "fail")), false, true},
		{"a key fail blocks O4 only", rows(with(both(), "key_signing", "fail")), true, false},
		{"a missing required id is not ready", rows(with(both(), "git", "")), false, true},
		{"a missing O4 id is not ready", rows(with(both(), "agents_md", "")), true, false},
		{"advisory fail never blocks", rows(with(with(both(), "editor", "fail"), "git_identity", "fail")), true, true},
		{"an mcp fail never blocks", rows(with(both(), "mcp", "fail")), true, true},
		{"advisory rows alone are not ready", rows(map[string]string{"editor": "ok", "git_identity": "ok"}), false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := StepReadiness(tc.checks); got.O3 != tc.wantO3 || got.O4 != tc.wantO4 {
				t.Errorf("O3=%v O4=%v, want %v %v", got.O3, got.O4, tc.wantO3, tc.wantO4)
			}
		})
	}
}

func TestValidDoctorCheckID(t *testing.T) {
	for _, id := range []string{"memory", "disk", "cpu", "virtualization", "vm_manager", "git", "git_identity", "git_email_github",
		"editor", "ssh_key", "ssh_keygen", "git_signing", "key_signing", "key_auth", "repo", "ruleset", "agents_md", "mcp"} {
		if !ValidDoctorCheckID(id) {
			t.Errorf("%s must be accepted from the CLI", id)
		}
	}
	for _, id := range []string{"", "two_factor", "secret_scanning", "Memory", "nope", "memory "} {
		if ValidDoctorCheckID(id) {
			t.Errorf("%q must be refused", id)
		}
	}
}

func TestCheckGroup(t *testing.T) {
	for id, want := range map[string]string{"memory": "machine", "ssh_key": "key", "ruleset": "repo", "two_factor": "advisory", "secret_scanning": "advisory", "mcp": "advisory", "nope": ""} {
		if got := checkGroup(id); got != want {
			t.Errorf("%s: %q, want %q", id, got, want)
		}
	}
}
