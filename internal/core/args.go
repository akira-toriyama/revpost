package core

import (
	"regexp"
	"strconv"
	"strings"
)

// GitHub restricts owner (user/org) names to letters, digits and hyphens, and
// repo names to letters, digits, dot, hyphen and underscore. Validating the
// charset here keeps a typo'd target a usage error (exit 2) instead of letting a
// mangled endpoint surface as an internal or not-found failure once gh runs.
var (
	ownerName = regexp.MustCompile(`^[A-Za-z0-9-]+$`)
	repoName  = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// ParseTarget parses the positional "owner/repo#N" argument into its parts. It is
// strict: exactly one '/', exactly one '#', non-empty owner/repo, a positive PR
// number, no whitespace, and no leading '-' (which would look like a flag). A bad
// target is a validation error — fix the argument, do not retry.
func ParseTarget(s string) (owner, repo string, number int, err error) {
	if s == "" || strings.ContainsAny(s, " \t\r\n") {
		return "", "", 0, Validationf("bad-target", "target must be owner/repo#N (got %q)", s)
	}
	if strings.HasPrefix(s, "-") {
		return "", "", 0, Validationf("bad-target", "target %q looks like a flag; expected owner/repo#N", s)
	}
	hash := strings.IndexByte(s, '#')
	if hash < 0 || strings.ContainsRune(s[hash+1:], '#') {
		return "", "", 0, Validationf("bad-target", "target must contain one #N (got %q)", s)
	}
	repoPart, numPart := s[:hash], s[hash+1:]

	slash := strings.IndexByte(repoPart, '/')
	if slash < 0 || strings.ContainsRune(repoPart[slash+1:], '/') {
		return "", "", 0, Validationf("bad-target", "target must be owner/repo#N (got %q)", s)
	}
	owner, repo = repoPart[:slash], repoPart[slash+1:]
	if owner == "" || repo == "" {
		return "", "", 0, Validationf("bad-target", "target needs a non-empty owner and repo (got %q)", s)
	}
	if !ownerName.MatchString(owner) {
		return "", "", 0, Validationf("bad-target",
			"owner %q is not a valid GitHub name (letters, digits, hyphen)", owner)
	}
	if repo == "." || repo == ".." || !repoName.MatchString(repo) {
		return "", "", 0, Validationf("bad-target",
			"repo %q is not a valid GitHub name (letters, digits, dot, hyphen, underscore)", repo)
	}

	number, convErr := strconv.Atoi(numPart)
	if convErr != nil || number < 1 {
		return "", "", 0, Validationf("bad-target", "PR number must be a positive integer (got %q)", numPart)
	}
	return owner, repo, number, nil
}

// ParseSnapWithin parses the --snap flag value. "" disables snapping (stray
// anchors drop); "within:N" enables it for a positive N. Any other shape is a
// validation error, so a typo can never silently disable snapping.
func ParseSnapWithin(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	const prefix = "within:"
	if !strings.HasPrefix(s, prefix) {
		return 0, Validationf("bad-snap", "--snap must be of the form within:N (got %q)", s)
	}
	n, err := strconv.Atoi(s[len(prefix):])
	if err != nil || n < 1 {
		return 0, Validationf("bad-snap", "--snap within:N requires a positive integer (got %q)", s)
	}
	return n, nil
}

// ValidateEvent normalizes and checks the review event. PENDING is intentionally
// excluded — it is the multi-step draft dance revpost exists to avoid.
func ValidateEvent(s string) (string, error) {
	switch e := strings.ToUpper(strings.TrimSpace(s)); e {
	case "COMMENT", "REQUEST_CHANGES", "APPROVE":
		return e, nil
	default:
		return "", Validationf("bad-event",
			"--event must be COMMENT, REQUEST_CHANGES, or APPROVE (got %q)", s)
	}
}
