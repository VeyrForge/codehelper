package orchestrator

import (
	"context"
	"encoding/json"
)

// CallMetrics records bytes and estimated tokens for one tool invocation.
type CallMetrics struct {
	Tool       string `json:"tool"`
	ReqBytes   int    `json:"req_bytes"`
	RespBytes  int    `json:"resp_bytes"`
	RespTokens int    `json:"resp_tokens"`
}

// UsageTotals aggregates metering across a run.
type UsageTotals struct {
	ToolCalls  int           `json:"tool_calls"`
	ReqBytes   int           `json:"req_bytes"`
	RespBytes  int           `json:"resp_bytes"`
	RespTokens int           `json:"resp_tokens"`
	PerTool    []CallMetrics `json:"per_tool,omitempty"`
}

// MeteredInvoker wraps a ToolInvoker and records usage.
type MeteredInvoker struct {
	Inner ToolInvoker
	Last  UsageTotals
}

// Call invokes the inner tool and records metrics.
func (m *MeteredInvoker) Call(ctx context.Context, name string, args map[string]any) (string, error) {
	reqJSON, _ := json.Marshal(args)
	out, err := m.Inner.Call(ctx, name, args)
	m.record(name, len(reqJSON), len(out))
	return out, err
}

func (m *MeteredInvoker) record(tool string, reqB, respB int) {
	toks := estimateTokens(respB)
	m.Last.ToolCalls++
	m.Last.ReqBytes += reqB
	m.Last.RespBytes += respB
	m.Last.RespTokens += toks
	m.Last.PerTool = append(m.Last.PerTool, CallMetrics{
		Tool: tool, ReqBytes: reqB, RespBytes: respB, RespTokens: toks,
	})
}

// Reset clears accumulated metrics.
func (m *MeteredInvoker) Reset() { m.Last = UsageTotals{} }

func estimateTokens(respBytes int) int {
	if respBytes <= 0 {
		return 0
	}
	return (respBytes + 3) / 4
}
