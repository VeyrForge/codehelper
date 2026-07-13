package agentapi

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/paths"
	"github.com/VeyrForge/codehelper/internal/profile"
	"github.com/VeyrForge/codehelper/internal/review"
	"github.com/VeyrForge/codehelper/internal/verify"
)

type verifyRequest struct {
	LintCmd         string   `json:"lint_cmd,omitempty"`
	BuildCmd        string   `json:"build_cmd,omitempty"`
	TestCmd         string   `json:"test_cmd,omitempty"`
	ExecMode        string   `json:"exec_mode,omitempty"`
	AllowedCommands []string `json:"allowed_commands,omitempty"`
	TimeoutSeconds  int      `json:"timeout_seconds,omitempty"`
	ChangedPaths    []string `json:"changed_paths,omitempty"`
	UseProfile      *bool    `json:"use_profile,omitempty"`
}

type reviewRequest struct {
	Base            string `json:"base,omitempty"`
	SeverityFloor   string `json:"severity_floor,omitempty"`
	IncludeTests    *bool  `json:"include_tests,omitempty"`
	IncludeSecurity *bool  `json:"include_security,omitempty"`
}

type finishRequest struct {
	Base            string `json:"base,omitempty"`
	VerifyRan       bool   `json:"verify_ran,omitempty"`
	VerifyAbstained bool   `json:"verify_abstained,omitempty"`
	VerifyReason    string `json:"verify_reason,omitempty"`
}

func (s *Server) registerGateRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/verify", s.handleVerify)
	mux.HandleFunc("POST /v1/review", s.handleReview)
	mux.HandleFunc("POST /v1/finish", s.handleFinish)
}

func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	var req verifyRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		return
	}
	ctx := r.Context()
	root := s.WorkspaceRoot

	useProfile := req.UseProfile == nil || *req.UseProfile
	hasExplicit := strings.TrimSpace(req.LintCmd) != "" ||
		strings.TrimSpace(req.BuildCmd) != "" ||
		strings.TrimSpace(req.TestCmd) != ""

	if hasExplicit {
		mode := verify.ExecMode(strings.ToLower(strings.TrimSpace(req.ExecMode)))
		if mode == "" {
			mode = verify.ExecArgv
		}
		res, err := verify.Run(ctx, verify.Request{
			RepoRoot:        root,
			LintCmd:         req.LintCmd,
			BuildCmd:        req.BuildCmd,
			TestCmd:         req.TestCmd,
			ExecMode:        mode,
			AllowedCommands: req.AllowedCommands,
			TimeoutSeconds:  req.TimeoutSeconds,
			ChangedPaths:    req.ChangedPaths,
		})
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, res)
		return
	}

	if !useProfile {
		writeJSONError(w, http.StatusBadRequest, "provide lint_cmd/build_cmd/test_cmd or use_profile=true")
		return
	}

	cmds, err := profileVerifyCommands(root)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	mode := verify.ExecMode(strings.ToLower(strings.TrimSpace(req.ExecMode)))
	if mode == "" {
		mode = verify.ExecArgv
	}
	outcomes := verify.RunCommandLines(ctx, cmds, verify.RunCommandsOptions{
		RepoRoot:        root,
		ExecMode:        mode,
		AllowedCommands: req.AllowedCommands,
		TimeoutSeconds:  req.TimeoutSeconds,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"accepted":        !verify.HasFailures(outcomes),
		"verify_commands": cmds,
		"outcomes":        outcomes,
		"failures_text":   verify.FailuresText(outcomes),
	})
}

func (s *Server) handleReview(w http.ResponseWriter, r *http.Request) {
	var req reviewRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		return
	}
	out, err := runReviewDiff(r.Context(), s.WorkspaceRoot, req)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleFinish(w http.ResponseWriter, r *http.Request) {
	var req finishRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		return
	}
	out, err := runFinishCheck(r.Context(), s.WorkspaceRoot, req)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func runReviewDiff(ctx context.Context, root string, req reviewRequest) (any, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	st, err := graph.Open(paths.DBPath(abs))
	if err != nil {
		return nil, err
	}
	defer st.Close()
	base := strings.TrimSpace(req.Base)
	if base == "" {
		base = "HEAD~1"
	}
	floor := review.SeverityMedium
	if sf := strings.TrimSpace(req.SeverityFloor); sf != "" {
		floor = review.Severity(strings.ToLower(sf))
	}
	incTests := req.IncludeTests == nil || *req.IncludeTests
	incSec := req.IncludeSecurity == nil || *req.IncludeSecurity
	return review.ReviewDiff(ctx, st, review.DiffRequest{
		RepoRoot:           abs,
		RepoName:           filepath.Base(abs),
		Base:               base,
		SeverityFloor:      floor,
		IncludeTests:       incTests,
		IncludeSecurity:    incSec,
		IncludePerformance: true,
		IncludeContracts:   true,
	})
}

func runFinishCheck(ctx context.Context, root string, req finishRequest) (review.FinishCheckOutput, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return review.FinishCheckOutput{}, err
	}
	st, err := graph.Open(paths.DBPath(abs))
	if err != nil {
		return review.FinishCheckOutput{}, err
	}
	defer st.Close()
	base := strings.TrimSpace(req.Base)
	if base == "" {
		base = "HEAD~1"
	}
	repoName := filepath.Base(abs)
	rv, err := review.ReviewDiff(ctx, st, review.DiffRequest{
		RepoRoot: abs, RepoName: repoName, Base: base, SeverityFloor: review.SeverityMedium,
		IncludeTests: true, IncludeSecurity: true, IncludePerformance: true, IncludeContracts: true,
	})
	if err != nil {
		return review.FinishCheckOutput{}, err
	}
	cg, err := review.ContractGuard(ctx, st, abs, repoName, base)
	if err != nil {
		return review.FinishCheckOutput{}, err
	}
	tg, err := review.TestGap(ctx, st, abs, repoName, base)
	if err != nil {
		return review.FinishCheckOutput{}, err
	}
	rr := review.BuildReleaseReadiness(rv, cg, tg, review.RiskScore(rv.Findings))
	return review.BuildFinishCheck(review.FinishCheckInput{
		BaseRef:         base,
		VerifyRan:       req.VerifyRan,
		VerifyAbstained: req.VerifyAbstained,
		VerifyReason:    req.VerifyReason,
		Release:         rr,
	}), nil
}

func profileVerifyCommands(root string) ([]string, error) {
	pr, err := profile.Read(root)
	if err != nil || pr == nil {
		if _, werr := profile.Write(root); werr != nil {
			return nil, werr
		}
		pr, err = profile.Read(root)
		if err != nil {
			return nil, err
		}
	}
	if pr == nil {
		return nil, nil
	}
	var cmds []string
	cmds = append(cmds, pr.TestCommands...)
	cmds = append(cmds, pr.LintCommands...)
	out := cmds[:0]
	for _, c := range cmds {
		if strings.TrimSpace(c) != "" {
			out = append(out, c)
		}
	}
	return out, nil
}

// FinishCheckJSON marshals finish output for task events.
func FinishCheckJSON(out review.FinishCheckOutput) string {
	b, _ := json.Marshal(out)
	return string(b)
}
