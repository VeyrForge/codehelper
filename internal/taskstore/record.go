package taskstore

import (
	"fmt"
	"strings"
	"time"
)

// RecordMessage appends a chat message to the task.
func (s *Store) RecordMessage(id string, msg Message) (*Task, error) {
	t, err := s.Load(id)
	if err != nil {
		return nil, err
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now().UTC()
	}
	t.Messages = append(t.Messages, msg)
	return t, s.Save(t)
}

// RecordChangedFiles merges unique repo-relative paths onto the task.
func (s *Store) RecordChangedFiles(id string, paths []string) (*Task, error) {
	t, err := s.Load(id)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, p := range t.ChangedFiles {
		seen[p] = struct{}{}
	}
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		t.ChangedFiles = append(t.ChangedFiles, p)
	}
	return t, s.Save(t)
}

// AddCommandProposal queues a command awaiting approval.
func (s *Store) AddCommandProposal(id string, rec CommandRecord) (*Task, error) {
	t, err := s.Load(id)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(rec.ID) == "" {
		rec.ID = fmt.Sprintf("cmd_%d", len(t.Commands)+1)
	}
	if rec.Created.IsZero() {
		rec.Created = time.Now().UTC()
	}
	if rec.Status == "" {
		rec.Status = CommandPending
	}
	t.Commands = append(t.Commands, rec)
	t.Events = append(t.Events, Event{
		Type: "command_proposed", Timestamp: time.Now().UTC(), Actor: "agent",
		Details: strings.Join(rec.Command, " "),
	})
	return t, s.Save(t)
}

// PatchCommand updates one command record by id.
func (s *Store) PatchCommand(id, cmdID string, patch func(*CommandRecord) error) (*Task, error) {
	t, err := s.Load(id)
	if err != nil {
		return nil, err
	}
	for i := range t.Commands {
		if t.Commands[i].ID != cmdID {
			continue
		}
		if err := patch(&t.Commands[i]); err != nil {
			return nil, err
		}
		return t, s.Save(t)
	}
	return nil, fmt.Errorf("command %q not found", cmdID)
}

// AddVerificationResult appends verify output.
func (s *Store) AddVerificationResult(id string, vr VerificationResult) (*Task, error) {
	t, err := s.Load(id)
	if err != nil {
		return nil, err
	}
	if vr.Timestamp.IsZero() {
		vr.Timestamp = time.Now().UTC()
	}
	t.VerificationResults = append(t.VerificationResults, vr)
	return t, s.Save(t)
}

// AddReviewResult appends review output.
func (s *Store) AddReviewResult(id string, rr ReviewResult) (*Task, error) {
	t, err := s.Load(id)
	if err != nil {
		return nil, err
	}
	if rr.Timestamp.IsZero() {
		rr.Timestamp = time.Now().UTC()
	}
	t.ReviewResults = append(t.ReviewResults, rr)
	return t, s.Save(t)
}

// ProposeMemory adds a pending memory proposal.
func (s *Store) ProposeMemory(id string, mp MemoryProposal) (*Task, error) {
	t, err := s.Load(id)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(mp.ID) == "" {
		mp.ID = fmt.Sprintf("mem_%d", len(t.MemoryProposals)+1)
	}
	if mp.ProposedAt.IsZero() {
		mp.ProposedAt = time.Now().UTC()
	}
	if mp.Status == "" {
		mp.Status = "pending"
	}
	t.MemoryProposals = append(t.MemoryProposals, mp)
	t.Events = append(t.Events, Event{
		Type: "memory_proposed", Timestamp: time.Now().UTC(), Actor: "agent", Details: mp.Text,
	})
	return t, s.Save(t)
}

// ResolveMemoryProposal marks a proposal approved or rejected.
func (s *Store) ResolveMemoryProposal(id, proposalID, status string) (*Task, error) {
	t, err := s.Load(id)
	if err != nil {
		return nil, err
	}
	for i := range t.MemoryProposals {
		if t.MemoryProposals[i].ID != proposalID {
			continue
		}
		t.MemoryProposals[i].Status = status
		t.MemoryProposals[i].ResolvedAt = time.Now().UTC()
		t.Events = append(t.Events, Event{
			Type: "memory_" + status, Timestamp: time.Now().UTC(), Actor: "user", Details: proposalID,
		})
		return t, s.Save(t)
	}
	return nil, fmt.Errorf("memory proposal %q not found", proposalID)
}

// RecordDecisionChoice stores the user's choice for a decision point.
func (s *Store) RecordDecisionChoice(id, decisionID, choice string) (*Task, error) {
	t, err := s.Load(id)
	if err != nil {
		return nil, err
	}
	found := false
	for i := range t.DecisionPoints {
		if t.DecisionPoints[i].ID != decisionID {
			continue
		}
		t.DecisionPoints[i].Chosen = strings.TrimSpace(choice)
		found = true
		break
	}
	if !found {
		return nil, fmt.Errorf("decision %q not found", decisionID)
	}
	t.Decisions = append(t.Decisions, fmt.Sprintf("%s: %s", decisionID, choice))
	t.Events = append(t.Events, Event{
		Type: "decision_recorded", Timestamp: time.Now().UTC(), Actor: "user",
		Details: decisionID + "=" + choice,
	})
	return t, s.Save(t)
}

// SetFinalSummary stores the task completion summary.
func (s *Store) SetFinalSummary(id, summary string) (*Task, error) {
	t, err := s.Load(id)
	if err != nil {
		return nil, err
	}
	t.FinalSummary = strings.TrimSpace(summary)
	t.Events = append(t.Events, Event{
		Type: "final_summary", Timestamp: time.Now().UTC(), Actor: "agent", Details: "saved",
	})
	return t, s.Save(t)
}
