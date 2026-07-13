package helpcatalog

import "strings"

func stringsFold(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func stringsContainsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

func sortStrings(ss []string) {
	sortSlice(ss, func(a, b string) bool { return a < b })
}

func sortSlice[T any](ss []T, less func(a, b T) bool) {
	// tiny inline sort to avoid importing sort in every file
	for i := 0; i < len(ss); i++ {
		for j := i + 1; j < len(ss); j++ {
			if less(ss[j], ss[i]) {
				ss[i], ss[j] = ss[j], ss[i]
			}
		}
	}
}
