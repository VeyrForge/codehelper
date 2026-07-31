package review

type Severity string

const (
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

type CompletionState string

const (
	StatePlanned      CompletionState = "planned"
	StateChanged      CompletionState = "changed"
	StateReviewed     CompletionState = "reviewed"
	StateVerified     CompletionState = "verified"
	StateReleaseReady CompletionState = "release_ready"
)

type Finding struct {
	Severity     Severity `json:"severity"`
	Category     string   `json:"category"`
	File         string   `json:"file,omitempty"`
	Line         int      `json:"line,omitempty"`
	Symbol       string   `json:"symbol,omitempty"`
	Message      string   `json:"message"`
	Evidence     []string `json:"evidence,omitempty"`
	SuggestedFix string   `json:"suggested_fix,omitempty"`
}

type MissingTest struct {
	Symbol      string   `json:"symbol"`
	Risk        string   `json:"risk"`
	NeededTests []string `json:"needed_tests"`
}

type ReviewResult struct {
	Summary         string    `json:"summary"`
	Risk            string    `json:"risk"`
	Findings        []Finding `json:"findings"`
	RequiredActions []string  `json:"required_actions,omitempty"`
}

type Completion struct {
	CompletionState   CompletionState `json:"completion_state"`
	CanClaimDone      bool            `json:"can_claim_done"`
	MissingBeforeDone []string        `json:"missing_before_done,omitempty"`
}

type ReleaseReadiness struct {
	Ship               bool       `json:"ship"`
	Summary            string     `json:"summary"`
	Risk               string     `json:"risk"`
	BlockingFindings   []Finding  `json:"blocking_findings,omitempty"`
	RequiredBeforeDone []string   `json:"required_before_done,omitempty"`
	Completion         Completion `json:"completion"`
}
