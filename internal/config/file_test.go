package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/samvad-hq/samvad-news-harvester/internal/config"
	"github.com/stretchr/testify/require"
)

type item struct {
	ID   string `yaml:"id" json:"id"`
	Name string `yaml:"name" json:"name"`
}

func (i item) Ident() string { return i.ID }

func (i item) Validate() error {
	if i.ID == "" {
		return errors.New("id is required")
	}
	return nil
}

func writeFile(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestDecodeFileYAML(t *testing.T) {
	t.Parallel()

	path := writeFile(t, "items.yaml", "items:\n  - id: a\n    name: Alpha\n")

	var file struct {
		Items []item `yaml:"items" json:"items"`
	}
	require.NoError(t, config.DecodeFile(path, &file))
	require.Len(t, file.Items, 1)
	require.Equal(t, "Alpha", file.Items[0].Name)
}

func TestDecodeFileJSON(t *testing.T) {
	t.Parallel()

	path := writeFile(t, "items.json", `{"items":[{"id":"a","name":"Alpha"}]}`)

	var file struct {
		Items []item `yaml:"items" json:"items"`
	}
	require.NoError(t, config.DecodeFile(path, &file))
	require.Len(t, file.Items, 1)
	require.Equal(t, "Alpha", file.Items[0].Name)
}

func TestDecodeFileExpandsEnvironment(t *testing.T) {
	t.Setenv("TEST_ITEM_NAME", "FromEnv")

	path := writeFile(t, "items.yaml", "items:\n  - id: a\n    name: ${TEST_ITEM_NAME}\n")

	var file struct {
		Items []item `yaml:"items" json:"items"`
	}
	require.NoError(t, config.DecodeFile(path, &file))
	require.Equal(t, "FromEnv", file.Items[0].Name)
}

func TestDecodeFileRejects(t *testing.T) {
	t.Parallel()

	t.Run("missing file", func(t *testing.T) {
		var file struct{}
		require.Error(t, config.DecodeFile(filepath.Join(t.TempDir(), "nope.yaml"), &file))
	})

	t.Run("empty path", func(t *testing.T) {
		var file struct{}
		require.Error(t, config.DecodeFile("", &file))
	})

	t.Run("unknown extension", func(t *testing.T) {
		var file struct{}
		require.Error(t, config.DecodeFile(writeFile(t, "items.toml", "x = 1"), &file))
	})

	t.Run("malformed yaml", func(t *testing.T) {
		var file struct {
			Items []item `yaml:"items"`
		}
		require.Error(t, config.DecodeFile(writeFile(t, "bad.yaml", "items:\n  - id: [unclosed\n"), &file))
	})
}

func TestValidateList(t *testing.T) {
	t.Parallel()

	t.Run("accepts valid items", func(t *testing.T) {
		require.NoError(t, config.ValidateList([]item{{ID: "a"}, {ID: "b"}}))
	})

	t.Run("rejects an empty list", func(t *testing.T) {
		require.ErrorContains(t, config.ValidateList([]item{}), "no entries")
	})

	t.Run("reports the failing index", func(t *testing.T) {
		err := config.ValidateList([]item{{ID: "a"}, {ID: ""}})
		require.ErrorContains(t, err, "entry 1")
	})

	t.Run("rejects duplicate ids", func(t *testing.T) {
		err := config.ValidateList([]item{{ID: "a"}, {ID: "a"}})
		require.ErrorContains(t, err, "duplicate")
	})

	t.Run("reports both validation and duplicate errors for same entry", func(t *testing.T) {
		// Entry 0 is invalid (empty ID), entry 1 is a duplicate of entry 0
		err := config.ValidateList([]item{{ID: ""}, {ID: ""}})
		require.ErrorContains(t, err, "entry 0")
		require.ErrorContains(t, err, "id is required")
		require.ErrorContains(t, err, "entry 1")
		require.ErrorContains(t, err, "duplicate")
	})
}

func TestDecodeFileExpandsOnlyBracedEnv(t *testing.T) {
	t.Run("${VAR} expands to value", func(t *testing.T) {
		t.Setenv("TEST_VAR", "value")
		path := writeFile(t, "items.yaml", "items:\n  - id: a\n    name: ${TEST_VAR}\n")

		var file struct {
			Items []item `yaml:"items"`
		}
		require.NoError(t, config.DecodeFile(path, &file))
		require.Equal(t, "value", file.Items[0].Name)
	})

	t.Run("bare $NAME is not expanded", func(t *testing.T) {
		t.Setenv("TEST_VAR", "value")
		path := writeFile(t, "items.yaml", "items:\n  - id: a\n    name: $TEST_VAR\n")

		var file struct {
			Items []item `yaml:"items"`
		}
		require.NoError(t, config.DecodeFile(path, &file))
		require.Equal(t, "$TEST_VAR", file.Items[0].Name)
	})

	t.Run("dollar signs in values survive", func(t *testing.T) {
		path := writeFile(t, "items.yaml", "items:\n  - id: a\n    name: p$5cr3t\n")

		var file struct {
			Items []item `yaml:"items"`
		}
		require.NoError(t, config.DecodeFile(path, &file))
		require.Equal(t, "p$5cr3t", file.Items[0].Name)
	})

	t.Run("unset ${VAR} becomes empty", func(t *testing.T) {
		// When a variable is unset, it becomes empty string, which validation
		// then rejects by field name if the field is required.
		path := writeFile(t, "items.yaml", "items:\n  - id: ${UNSET_VAR}\n    name: test\n")

		var file struct {
			Items []item `yaml:"items"`
		}
		require.NoError(t, config.DecodeFile(path, &file))
		require.Equal(t, "", file.Items[0].ID)
		// The empty ID should be caught during validation
		err := config.ValidateList(file.Items)
		require.Error(t, err)
		require.ErrorContains(t, err, "id is required")
	})
}
