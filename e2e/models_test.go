package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestModels(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      []string
		env       []string
		agent     string
		defaultID string
		ids       []string
	}{
		{"codex", []string{"models", "--json"}, nil, "codex", "gpt-6-astra",
			[]string{"gpt-6-astra", "gpt-6.1-sol", "gpt-6-sol", "gpt-6-luna"}},
		{"claude", []string{"models", "--agent=claude", "--json"}, nil, "claude", "claude-fable-5-1",
			[]string{"claude-fable-5-1", "claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-4-5"}},
		{"environment", []string{"models", "--json"}, []string{"CODEXMON_AGENT=claude"}, "claude", "claude-fable-5-1",
			[]string{"claude-fable-5-1", "claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-4-5"}},
		{"explicit wins", []string{"models", "--agent", "codex", "--json"}, []string{"CODEXMON_AGENT=claude"}, "codex", "gpt-6-astra",
			[]string{"gpt-6-astra", "gpt-6.1-sol", "gpt-6-sol", "gpt-6-luna"}},
		{"padded environment", []string{"models", "--json"}, []string{"CODEXMON_AGENT= claude "}, "claude", "claude-fable-5-1",
			[]string{"claude-fable-5-1", "claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-4-5"}},
		{"blank environment", []string{"models", "--json"}, []string{"CODEXMON_AGENT= \t "}, "codex", "gpt-6-astra",
			[]string{"gpt-6-astra", "gpt-6.1-sol", "gpt-6-sol", "gpt-6-luna"}},
		{"padded flag", []string{"models", "--agent", " claude ", "--json"}, []string{"CODEXMON_AGENT=codex"}, "claude", "claude-fable-5-1",
			[]string{"claude-fable-5-1", "claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-4-5"}},
		{"blank flag uses environment", []string{"models", "--agent", " \t ", "--json"}, []string{"CODEXMON_AGENT=claude"}, "claude", "claude-fable-5-1",
			[]string{"claude-fable-5-1", "claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-4-5"}},
		{"codex empty terminator", []string{"models", "--json", "--"}, nil, "codex", "gpt-6-astra",
			[]string{"gpt-6-astra", "gpt-6.1-sol", "gpt-6-sol", "gpt-6-luna"}},
		{"claude empty terminator", []string{"models", "--agent", "claude", "--json", "--"}, nil, "claude", "claude-fable-5-1",
			[]string{"claude-fable-5-1", "claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-4-5"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := append([]string{"CODEXMON_AGENT=", "CODEXMON_CODEX=/nonexistent/codex", "CODEXMON_CLAUDE=/nonexistent/claude"}, tc.env...)
			r := runCodexmon(t, t.TempDir(), env, tc.args...)
			if r.code != 0 || r.stderr != "" {
				t.Fatalf("exit %d: %s", r.code, r.stderr)
			}
			var catalog struct {
				Agent  string `json:"agent"`
				Models []struct {
					ID          string `json:"id"`
					Name        string `json:"name"`
					Description string `json:"description"`
					Default     bool   `json:"default"`
				} `json:"models"`
			}
			if err := json.Unmarshal([]byte(r.stdout), &catalog); err != nil {
				t.Fatal(err)
			}
			if catalog.Agent != tc.agent {
				t.Fatalf("agent = %q, want %q", catalog.Agent, tc.agent)
			}
			var ids []string
			defaults := 0
			for _, m := range catalog.Models {
				ids = append(ids, m.ID)
				if m.Name == "" || m.Description == "" {
					t.Errorf("missing model metadata: %+v", m)
				}
				if m.Default {
					defaults++
					if m.ID != tc.defaultID {
						t.Errorf("default = %q, want %q", m.ID, tc.defaultID)
					}
				}
			}
			if defaults != 1 || !reflect.DeepEqual(ids, tc.ids) {
				t.Errorf("defaults = %d, ids = %v; want 1, %v", defaults, ids, tc.ids)
			}
		})
	}
}

func TestModelsTextAndErrors(t *testing.T) {
	home := t.TempDir()
	env := []string{"CODEXMON_AGENT="}
	r := runCodexmon(t, home, env, "models", "--agent", "claude")
	if r.code != 0 || !strings.Contains(r.stdout, "claude-opus-5-5") || !strings.Contains(r.stderr, "availability depends") {
		t.Fatalf("text catalog: %+v", r)
	}
	defaults := 0
	for i, line := range strings.Split(strings.TrimSpace(r.stdout), "\n") {
		fields := strings.Fields(line)
		if i == 0 {
			if len(fields) == 0 || fields[0] != "MODEL" {
				t.Fatalf("missing header: %q", line)
			}
			continue
		}
		if len(fields) == 0 || !strings.HasPrefix(fields[0], "claude-") {
			t.Fatalf("non-model row on stdout: %q", line)
		}
		for _, field := range fields {
			if field == "yes" {
				defaults++
				if fields[0] != "claude-fable-5-1" {
					t.Errorf("wrong text default: %q", line)
				}
			}
		}
	}
	if defaults != 1 {
		t.Errorf("text defaults = %d, want 1", defaults)
	}
	for _, args := range [][]string{
		{"models", "--agent", "unknown"},
		{"models", "--agent"},
		{"models", "--json=true"},
		{"models", "--unknown"},
		{"models", "unexpected"},
		{"models", "--agent", "claude", "--help"},
	} {
		r := runCodexmon(t, home, env, args...)
		if r.code != 1 || r.stderr == "" || r.stdout != "" {
			t.Errorf("%v: %+v", args, r)
		}
		if strings.Contains(r.stderr, "codexmon run") {
			t.Errorf("unsupported native models suggestion: %s", r.stderr)
		}
	}
}

func TestModelsNativePassthrough(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		env    []string
		argv   string
		result string
		code   int
		json   bool
	}{
		{"cursor flag", []string{"models", "--agent", "cursor"}, nil, "models\n", "CURSOR_NATIVE_MODELS_OK", 0, false},
		{"cursor environment", []string{"models"}, []string{"CODEXMON_AGENT= cursor "}, "models\n", "CURSOR_NATIVE_MODELS_OK", 0, false},
		{"cursor json", []string{"models", "--agent", "cursor", "--json"}, nil, "models\n", "CURSOR_NATIVE_MODELS_OK", 0, true},
		{"cursor native argument", []string{"models", "--agent", "cursor", "--help"}, nil, "models\n--help\n", "CURSOR_MODELS_HELP_OK", 0, false},
		{"cursor interleaved codexmon flags", []string{"models", "--help", "--json", "--agent", "cursor"}, nil, "models\n--help\n", "CURSOR_MODELS_HELP_OK", 0, true},
		{"cursor terminator", []string{"models", "--agent", "cursor", "--", "--json", "--agent", "codex"}, nil, "models\n--json\n--agent\ncodex\n", "CURSOR_NATIVE_MODELS_OK", 0, false},
		{"cursor json before terminator", []string{"models", "--agent", "cursor", "--json", "--", "--json"}, nil, "models\n--json\n", "CURSOR_NATIVE_MODELS_OK", 0, true},
		{"cursor literal terminator", []string{"models", "--agent", "cursor", "--", "--", "--json"}, nil, "models\n--\n--json\n", "CURSOR_NATIVE_MODELS_OK", 0, false},
		{"cursor native failure", []string{"models", "--agent", "cursor", "--fail"}, nil, "models\n--fail\n", "native models error", 7, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			argsFile := filepath.Join(home, "args.txt")
			env := append([]string{"CODEXMON_AGENT=", "FAKE_ARGS_FILE=" + argsFile}, tc.env...)
			r := runCodexmon(t, home, env, tc.args...)
			if r.code != tc.code {
				t.Fatalf("exit = %d, want %d: %+v", r.code, tc.code, r)
			}
			argv, err := os.ReadFile(argsFile)
			if err != nil || string(argv) != tc.argv {
				t.Fatalf("native argv = %q, want %q: %v", argv, tc.argv, err)
			}
			if tc.json {
				var job struct {
					Agent    string `json:"agent"`
					State    string `json:"state"`
					Result   string `json:"result"`
					ExitCode int    `json:"exit_code"`
				}
				if err := json.Unmarshal([]byte(r.stdout), &job); err != nil {
					t.Fatal(err)
				}
				if job.Agent != "cursor" || job.State != "completed" || job.ExitCode != 0 || strings.TrimSpace(job.Result) != tc.result {
					t.Fatalf("native JSON result: %+v", job)
				}
			} else if tc.code == 0 {
				if !strings.Contains(r.stdout, tc.result) {
					t.Fatalf("missing native result on stdout: %+v", r)
				}
			} else {
				foundError := false
				for _, line := range strings.Split(r.stdout, "\n") {
					if strings.HasPrefix(line, "error:") && strings.Contains(line, tc.result) {
						foundError = true
					}
				}
				if !foundError {
					t.Fatalf("missing native error in status report: %+v", r)
				}
			}
		})
	}
	// Explicit run can still reach Cursor's native models command directly.
	home := t.TempDir()
	argsFile := filepath.Join(home, "args.txt")
	r := runCodexmon(t, home, []string{"CODEXMON_AGENT=", "FAKE_ARGS_FILE=" + argsFile}, "run", "--agent", "cursor", "--", "models")
	if r.code != 0 || !strings.Contains(r.stdout, "CURSOR_NATIVE_MODELS_OK") {
		t.Fatalf("native escape hatch: %+v", r)
	}
	argv, err := os.ReadFile(argsFile)
	if err != nil || string(argv) != "models\n" {
		t.Fatalf("native escape hatch argv = %q: %v", argv, err)
	}
}
