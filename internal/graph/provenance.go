package graph

// Provenance describes the strength of evidence used to resolve an edge.
type Provenance string

const (
	Exact    Provenance = "exact"
	Scoped   Provenance = "scoped"
	NameOnly Provenance = "name_only"
	Inferred Provenance = "inferred"

	ConfExact    = 0.90
	ConfScoped   = 0.80
	ConfNameOnly = 0.70
	ConfInferred = 0.50
)

// ConfidenceForProvenance returns the stable confidence band for a tier.
func ConfidenceForProvenance(provenance Provenance) float64 {
	switch provenance {
	case Exact:
		return ConfExact
	case Scoped:
		return ConfScoped
	case NameOnly:
		return ConfNameOnly
	default:
		return ConfInferred
	}
}

// ProvenanceForStrategy classifies resolver strategies without changing storage.
func ProvenanceForStrategy(strategy string) Provenance {
	switch strategy {
	case "import", "recv_type":
		return Exact
	case "same_file", "same_dir", "same_subtree", "public_api", "embedded":
		return Scoped
	case "unique", "non_fixture":
		return NameOnly
	default:
		return Inferred
	}
}

// ConfidenceForStrategy returns the confidence band for a resolver strategy.
func ConfidenceForStrategy(strategy string) float64 {
	return ConfidenceForProvenance(ProvenanceForStrategy(strategy))
}
