// Package taskstore persists client-agnostic agent tasks under .codehelper/tasks/.
package taskstore

import (
	"time"

	"github.com/VeyrForge/codehelper/internal/freshness"
	"github.com/VeyrForge/codehelper/internal/patterns"
	"github.com/VeyrForge/codehelper/internal/profile"
)

const (
	StatusOpen    = "open"
	StatusDone    = "done"
	StatusBlocked = "blocked"

	TodoPlanned        = "planned"
	TodoApproved       = "approved"
	TodoInProgress     = "in_progress"
	TodoNeedsUserInput = "needs_user_input"
	TodoBlocked        = "blocked"
	TodoVerifying      = "verifying"
	TodoFailed         = "failed"
	TodoDebugging      = "debugging"
	TodoComplete       = "complete"
	TodoSkipped        = "skipped"

	CommandPending  = "pending"
	CommandApproved = "approved"
	CommandRejected = "rejected"
	CommandRan      = "ran"
	CommandFailed   = "failed"
)

// Task is the client-independent work unit (goal.md §29).
type Task struct {
	ID                  string               `json:"id"`
	UserRequest         string               `json:"user_request"`
	Title               string               `json:"title"`
	Status              string               `json:"status"`
	Mode                string               `json:"mode,omitempty"`
	Plan                Plan                 `json:"plan"`
	Todos               []Todo               `json:"todos"`
	Decisions           []string             `json:"decisions,omitempty"`
	DecisionPoints      []DecisionPoint      `json:"decision_points,omitempty"`
	Messages            []Message            `json:"messages,omitempty"`
	ChangedFiles        []string             `json:"changed_files,omitempty"`
	Commands            []CommandRecord      `json:"commands,omitempty"`
	VerificationResults []VerificationResult `json:"verification_results,omitempty"`
	ReviewResults       []ReviewResult       `json:"review_results,omitempty"`
	MemoryProposals     []MemoryProposal     `json:"memory_proposals,omitempty"`
	FinalSummary        string               `json:"final_summary,omitempty"`
	Events              []Event              `json:"events,omitempty"`
	CreatedAt           time.Time            `json:"created_at"`
	UpdatedAt           time.Time            `json:"updated_at"`
}

// CodeRef points to existing project code discovered during planning.
type CodeRef struct {
	Path   string `json:"path"`
	Symbol string `json:"symbol,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// PlanOption is one implementation approach (goal.md §10).
type PlanOption struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Pros        []string `json:"pros,omitempty"`
	Cons        []string `json:"cons,omitempty"`
}

// Plan is the editable implementation plan attached to a task.
type Plan struct {
	Goal                  string                  `json:"goal"`
	CurrentUnderstanding  string                  `json:"current_understanding,omitempty"`
	Assumptions           []string                `json:"assumptions,omitempty"`
	ExpandRequest         patterns.ExpandOutput   `json:"expand_request"`
	ProjectProfile        *profile.ProjectProfile `json:"project_profile,omitempty"`
	Freshness             *freshness.Report       `json:"freshness,omitempty"`
	ExistingCodeFound     []CodeRef               `json:"existing_code_found,omitempty"`
	ReuseCandidates       []string                `json:"reuse_candidates,omitempty"`
	ImpactTier            string                  `json:"impact_tier,omitempty"`
	ImplementationOptions []PlanOption            `json:"implementation_options,omitempty"`
	RecommendedOption     string                  `json:"recommended_option,omitempty"`
	DoneCriteria          []string                `json:"done_criteria,omitempty"`
	ResearchSummary       *ResearchSummary        `json:"research_summary,omitempty"`
}

// ResearchSummary captures goal.md §15 research output on a plan.
type ResearchSummary struct {
	Needed         string   `json:"needed,omitempty"`
	Sources        []string `json:"sources,omitempty"`
	Recommendation string   `json:"recommendation,omitempty"`
	Avoid          []string `json:"avoid,omitempty"`
	ProjectImpact  string   `json:"project_impact,omitempty"`
}

// DecisionPoint is a user choice with options (goal.md §12).
type DecisionPoint struct {
	ID          string   `json:"id"`
	Question    string   `json:"question"`
	Options     []string `json:"options,omitempty"`
	Pros        []string `json:"pros,omitempty"`
	Cons        []string `json:"cons,omitempty"`
	Recommended string   `json:"recommended,omitempty"`
	Chosen      string   `json:"chosen,omitempty"`
}

// Message is one chat entry tied to a task (goal.md §29).
type Message struct {
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
	Source    string    `json:"source,omitempty"`
	TodoID    string    `json:"todo_id,omitempty"`
}

// CommandRecord tracks proposed and run commands (goal.md §19).
type CommandRecord struct {
	ID      string    `json:"id"`
	Command []string  `json:"command"`
	Purpose string    `json:"purpose,omitempty"`
	Mode    string    `json:"mode,omitempty"`
	Status  string    `json:"status"`
	Output  string    `json:"output,omitempty"`
	TodoID  string    `json:"todo_id,omitempty"`
	Created time.Time `json:"created_at"`
	RanAt   time.Time `json:"ran_at,omitempty"`
}

// VerificationResult stores verify gate output.
type VerificationResult struct {
	Timestamp time.Time `json:"timestamp"`
	TodoID    string    `json:"todo_id,omitempty"`
	Passed    bool      `json:"passed"`
	Summary   string    `json:"summary,omitempty"`
	Details   string    `json:"details,omitempty"`
}

// ReviewResult stores review gate output.
type ReviewResult struct {
	Timestamp time.Time `json:"timestamp"`
	TodoID    string    `json:"todo_id,omitempty"`
	Passed    bool      `json:"passed"`
	Summary   string    `json:"summary,omitempty"`
	Details   string    `json:"details,omitempty"`
}

// MemoryProposal is a pending project memory item (goal.md §25).
type MemoryProposal struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	Text       string    `json:"text"`
	Status     string    `json:"status"`
	ProposedAt time.Time `json:"proposed_at"`
	ResolvedAt time.Time `json:"resolved_at,omitempty"`
}

// Todo is one executable step in the plan (goal.md §11).
type Todo struct {
	ID                  string   `json:"id"`
	Title               string   `json:"title"`
	Goal                string   `json:"goal,omitempty"`
	Description         string   `json:"description"`
	UserNotes           string   `json:"user_notes,omitempty"`
	Files               []string `json:"files,omitempty"`
	ReuseSymbols        []string `json:"reuse_symbols,omitempty"`
	AvoidDuplicating    []string `json:"avoid_duplicating,omitempty"`
	RequiredContext     []string `json:"required_context,omitempty"`
	ImplementationNotes string   `json:"implementation_notes,omitempty"`
	Risks               []string `json:"risks,omitempty"`
	SecurityChecks      []string `json:"security_checks,omitempty"`
	PerformanceChecks   []string `json:"performance_checks,omitempty"`
	ContractChecks      []string `json:"contract_checks,omitempty"`
	VerifyCommands      []string `json:"verify_commands,omitempty"`
	ManualVerification  string   `json:"manual_verification,omitempty"`
	Status              string   `json:"status"`
	BlockedReason       string   `json:"blocked_reason,omitempty"`
	Evidence            string   `json:"evidence,omitempty"`
	DebugAttempts       int      `json:"debug_attempts,omitempty"`
}

// Event is a timeline entry for task history.
type Event struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Actor     string    `json:"actor,omitempty"`
	Details   string    `json:"details,omitempty"`
	TodoID    string    `json:"todo_id,omitempty"`
}
