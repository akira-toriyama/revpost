package core

import "testing"

func TestParseTarget(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		owner, repo, n, err := ParseTarget("octo-cat/My.Repo#123")
		if err != nil {
			t.Fatalf("ParseTarget: %v", err)
		}
		if owner != "octo-cat" || repo != "My.Repo" || n != 123 {
			t.Errorf("= (%q, %q, %d), want (octo-cat, My.Repo, 123)", owner, repo, n)
		}
	})

	bad := map[string]string{
		"no hash":      "o/r",
		"no slash":     "orepo#7",
		"empty owner":  "/r#7",
		"empty repo":   "o/#7",
		"zero number":  "o/r#0",
		"negative":     "o/r#-1",
		"non-numeric":  "o/r#abc",
		"two slashes":  "o/r/x#7",
		"two hashes":   "o/r#7#8",
		"whitespace":   "o / r#7",
		"flag-looking": "-o/r#7",
		"empty":        "",
		// A structurally valid target whose owner/repo is not a legal GitHub name
		// is a usage error (exit 2) — not an internal/not-found error after gh
		// mangles the endpoint.
		"bad repo char":    "o/hello?world#7",
		"repo dot":         "o/.#7",
		"repo dotdot":      "o/..#7",
		"owner underscore": "under_score/r#7", // '_' is legal in a repo, not an owner
	}
	for name, s := range bad {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := ParseTarget(s); err == nil {
				t.Errorf("ParseTarget(%q) succeeded; want a validation error", s)
			} else if ExitCode(err) != int(CodeValidation) {
				t.Errorf("ParseTarget(%q) exit = %d, want validation", s, ExitCode(err))
			}
		})
	}
}

func TestParseSnapWithin(t *testing.T) {
	ok := map[string]int{
		"":          0, // disabled — drop instead of snap
		"within:1":  1,
		"within:12": 12,
	}
	for s, want := range ok {
		if got, err := ParseSnapWithin(s); err != nil || got != want {
			t.Errorf("ParseSnapWithin(%q) = (%d, %v), want (%d, nil)", s, got, err, want)
		}
	}
	for _, s := range []string{"3", "within:0", "within:-1", "within:x", "foo", "within:"} {
		if _, err := ParseSnapWithin(s); err == nil {
			t.Errorf("ParseSnapWithin(%q) succeeded; want a validation error", s)
		} else if ExitCode(err) != int(CodeValidation) {
			t.Errorf("ParseSnapWithin(%q) exit = %d, want validation", s, ExitCode(err))
		}
	}
}

func TestValidateEvent(t *testing.T) {
	for in, want := range map[string]string{
		"COMMENT":         "COMMENT",
		"comment":         "COMMENT",
		"request_changes": "REQUEST_CHANGES",
		"APPROVE":         "APPROVE",
	} {
		if got, err := ValidateEvent(in); err != nil || got != want {
			t.Errorf("ValidateEvent(%q) = (%q, %v), want (%q, nil)", in, got, err, want)
		}
	}
	for _, s := range []string{"PENDING", "DISMISS", "", "approve!"} {
		if _, err := ValidateEvent(s); err == nil {
			t.Errorf("ValidateEvent(%q) succeeded; want a validation error", s)
		} else if ExitCode(err) != int(CodeValidation) {
			t.Errorf("ValidateEvent(%q) exit = %d, want validation", s, ExitCode(err))
		}
	}
}
