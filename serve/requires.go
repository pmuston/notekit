package serve

import (
	"os"
	"strings"
)

// requiresKey is the front-matter key naming environment variables the notebook
// needs (§2.5).
const requiresKey = "requires"

// envReport is what the page says about the notebook's declared environment.
type envReport struct {
	// Missing names the declared variables that are unset or empty. Names only:
	// §2.5 reads nothing from the environment but whether each is non-empty.
	Missing []string

	// Malformed is set when `requires` is present with an empty value, which is
	// what a YAML block sequence looks like to a reader that parses front matter
	// without a marshaller. §2.5 requires saying so rather than reporting
	// nothing, because an empty declaration is never what someone meant.
	Malformed bool
}

// Any reports whether there is anything to tell the reader.
func (r envReport) Any() bool { return r.Malformed || len(r.Missing) > 0 }

// checkRequires reads the `requires` declaration and reports what is missing.
//
// It never blocks: a notebook should open for reading without its credentials to
// hand, and a cell that needs one fails on its own terms with a better message
// than a refusal at the front door (§2.5).
func checkRequires(front map[string]string) envReport {
	raw, ok := front[requiresKey]
	if !ok {
		return envReport{}
	}
	// Present but empty: a block sequence, whose items front matter cannot see.
	if strings.TrimSpace(raw) == "" {
		return envReport{Malformed: true}
	}

	var rep envReport
	for _, name := range strings.Split(strings.Trim(strings.TrimSpace(raw), "[]"), ",") {
		name = strings.Trim(strings.TrimSpace(name), `"'`)
		if name == "" {
			continue
		}
		if os.Getenv(name) == "" {
			rep.Missing = append(rep.Missing, name)
		}
	}
	return rep
}
