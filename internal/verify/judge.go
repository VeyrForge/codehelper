package verify

import "context"

// Judge scores a patch or gate bundle; default implementation is nil (disabled).
type Judge interface {
	Score(ctx context.Context, req Request, partial *Result) (confidence float64, reasons []string, err error)
}
