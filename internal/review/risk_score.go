package review

func RiskScore(findings []Finding) string {
	score := 0
	for _, f := range findings {
		switch f.Severity {
		case SeverityCritical:
			score += 8
		case SeverityHigh:
			score += 5
		case SeverityMedium:
			score += 2
		default:
			score++
		}
	}
	switch {
	case score >= 12:
		return "critical"
	case score >= 7:
		return "high"
	case score >= 3:
		return "medium"
	default:
		return "low"
	}
}
