package security

import (
	"encoding/json"
	"os"
)

type SarifIssue struct {
	Tool     string `json:"tool"`
	Severity string `json:"severity"`
	Rule     string `json:"rule"`
	File     string `json:"file"`
	Message  string `json:"message"`
}

type sarifRoot struct {
	Runs []struct {
		Tool struct {
			Driver struct {
				Name string `json:"name"`
			} `json:"driver"`
		} `json:"tool"`
		Results []struct {
			RuleID  string `json:"ruleId"`
			Level   string `json:"level"`
			Message struct {
				Text string `json:"text"`
			} `json:"message"`
			Locations []struct {
				PhysicalLocation struct {
					ArtifactLocation struct {
						URI string `json:"uri"`
					} `json:"artifactLocation"`
				} `json:"physicalLocation"`
			} `json:"locations"`
		} `json:"results"`
	} `json:"runs"`
}

func ImportSARIF(path string) ([]SarifIssue, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var root sarifRoot
	if err := json.Unmarshal(b, &root); err != nil {
		return nil, err
	}
	out := make([]SarifIssue, 0, 16)
	for _, run := range root.Runs {
		tool := run.Tool.Driver.Name
		for _, r := range run.Results {
			file := ""
			if len(r.Locations) > 0 {
				file = r.Locations[0].PhysicalLocation.ArtifactLocation.URI
			}
			out = append(out, SarifIssue{
				Tool: tool, Severity: r.Level, Rule: r.RuleID, File: file, Message: r.Message.Text,
			})
		}
	}
	return out, nil
}
