package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// envRef matches ${VAR}. Only the braced form is expanded: a bare $NAME
// would also match values that merely contain a dollar sign, and this
// file is where credentials live.
var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Item is one entry in a configuration list. Both source.Config and
// sink.Config implement it, which is what lets one loader serve both.
type Item interface {
	// Ident returns the entry's unique identifier, used to reject duplicates.
	Ident() string
	// Validate reports whether the entry is usable.
	Validate() error
}

// expandEnv replaces only ${VAR} references from the environment, not bare
// $NAME, so values containing a dollar sign like p$5cr3t or $.foo survive.
func expandEnv(raw []byte) []byte {
	return envRef.ReplaceAllFunc(raw, func(m []byte) []byte {
		name := envRef.FindSubmatch(m)[1]
		return []byte(os.Getenv(string(name)))
	})
}

// DecodeFile reads path and decodes it into dst. The format is chosen by
// file extension: .yaml and .yml as YAML, .json as JSON.
//
// Only ${VAR} references (braced form) are expanded from the environment
// before decoding, so credentials live in the environment rather than in
// the file. A bare $NAME is not expanded. An unset variable expands to an
// empty string, which validation then rejects with a message naming the field.
func DecodeFile(path string, dst any) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("config: file path is empty")
	}

	raw, err := os.ReadFile(path) //nolint:gosec // the path is operator-supplied configuration
	if err != nil {
		return fmt.Errorf("config: read %s: %w", path, err)
	}

	expanded := expandEnv(raw)

	switch ext := strings.ToLower(filepath.Ext(path)); ext {
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(expanded, dst); err != nil {
			return fmt.Errorf("config: decode yaml %s: %w", path, err)
		}
	case ".json":
		if err := json.Unmarshal(expanded, dst); err != nil {
			return fmt.Errorf("config: decode json %s: %w", path, err)
		}
	default:
		return fmt.Errorf("config: %s has unsupported extension %q (use .yaml, .yml or .json)", path, ext)
	}
	return nil
}

// ValidateList checks every entry and rejects duplicate identifiers,
// reporting all problems at once rather than stopping at the first.
func ValidateList[T Item](items []T) error {
	if len(items) == 0 {
		return errors.New("config: file contains no entries")
	}

	var errs []error
	seen := make(map[string]int, len(items))

	for i, it := range items {
		// Check validity and duplicates independently so both errors are reported.
		if err := it.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("entry %d: %w", i, err))
		}

		id := it.Ident()
		if first, dup := seen[id]; dup {
			errs = append(errs, fmt.Errorf("entry %d: duplicate id %q, first used by entry %d", i, id, first))
		} else {
			seen[id] = i
		}
	}

	return errors.Join(errs...)
}
