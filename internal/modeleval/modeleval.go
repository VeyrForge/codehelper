// Package modeleval runs local task suites against an external model command (optional).
package modeleval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Task is one evaluation prompt with expectations.
type Task struct {
	Name             string   `json:"name"`
	Prompt           string   `json:"prompt"`
	ExpectedFindings []string `json:"expected_findings,omitempty"`
	Expected         []string `json:"expected,omitempty"`
}

// Suite is a batch of model-eval tasks.
type Suite struct {
	Tasks []Task `json:"tasks"`
}

// Result summarizes pass/fail per task.
type Result struct {
	Model   string    `json:"model"`
	Passed  int       `json:"passed"`
	Failed  int       `json:"failed"`
	Results []Outcome `json:"results"`
}

// Outcome is one task result.
type Outcome struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
}

// LoadSuite reads suite JSON.
func LoadSuite(r io.Reader) (Suite, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return Suite{}, err
	}
	var peek struct {
		Queries []json.RawMessage `json:"queries"`
		Tasks   []json.RawMessage `json:"tasks"`
	}
	if err := json.Unmarshal(raw, &peek); err != nil {
		return Suite{}, err
	}
	if len(peek.Queries) > 0 && len(peek.Tasks) == 0 {
		return Suite{}, fmt.Errorf("suite has \"queries\" but no \"tasks\": this is a retrieval eval suite — use `codehelper eval`, not `model-eval`")
	}
	var s Suite
	if err := json.Unmarshal(raw, &s); err != nil {
		return Suite{}, err
	}
	return s, nil
}

// Run invokes commandTemplate with {{model}} and {{prompt}} substituted for each task.
// If commandTemplate is empty, runs heuristic local check only (expected substrings vs empty response).
func Run(ctx context.Context, model string, suite Suite, commandTemplate string) (*Result, error) {
	res := &Result{Model: model}
	cmdTpl := strings.TrimSpace(commandTemplate)
	for _, t := range suite.Tasks {
		o := Outcome{Name: t.Name}
		text := ""
		var runErr error
		if cmdTpl != "" {
			cmdLine := strings.ReplaceAll(cmdTpl, "{{model}}", model)
			cmdLine = strings.ReplaceAll(cmdLine, "{{prompt}}", t.Prompt)
			fields := strings.Fields(cmdLine)
			if len(fields) == 0 {
				o.Pass = false
				o.Detail = "empty command"
				res.Results = append(res.Results, o)
				res.Failed++
				continue
			}
			c := exec.CommandContext(ctx, fields[0], fields[1:]...)
			var buf bytes.Buffer
			c.Stdout = &buf
			c.Stderr = &buf
			runErr = c.Run()
			text = buf.String()
			if runErr != nil {
				o.Detail = runErr.Error()
			}
		} else {
			o.Detail = "no CODEHELPER_MODEL_EVAL_CMD set; substring checks skipped"
		}
		lt := strings.ToLower(text)
		pass := runErr == nil
		if cmdTpl == "" {
			pass = true
		}
		exp := append([]string{}, t.ExpectedFindings...)
		exp = append(exp, t.Expected...)
		hasExp := false
		for _, e := range exp {
			e = strings.TrimSpace(strings.ToLower(e))
			if e == "" {
				continue
			}
			hasExp = true
			if !strings.Contains(lt, e) {
				pass = false
				o.Detail = fmt.Sprintf("missing expected fragment: %s", e)
				break
			}
		}
		if !hasExp && cmdTpl == "" {
			pass = true
			o.Detail = "no expectations and no runner; vacuous pass"
		}
		o.Pass = pass
		if pass {
			res.Passed++
		} else {
			res.Failed++
		}
		res.Results = append(res.Results, o)
	}
	return res, nil
}

// RunFromEnv uses CODEHELPER_MODEL_EVAL_CMD as template if set.
func RunFromEnv(ctx context.Context, model string, suite Suite) (*Result, error) {
	cmd := strings.TrimSpace(os.Getenv("CODEHELPER_MODEL_EVAL_CMD"))
	return Run(ctx, model, suite, cmd)
}
