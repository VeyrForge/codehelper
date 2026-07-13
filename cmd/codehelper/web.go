package main

import (
	"encoding/json"
	"fmt"

	"github.com/VeyrForge/codehelper/internal/web"
	"github.com/spf13/cobra"
)

func webCmd() *cobra.Command {
	var (
		method      string
		body        string
		timeoutSec  int
		follow      bool
		insecure    bool
		extractText bool
		status      int
		contains    []string
		absent      []string
		regex       string
		jsonPath    string
		jsonVal     string
		maxLatency  int
		asJSON      bool
	)
	c := &cobra.Command{
		Use:   "web <url>",
		Short: "Verify a web endpoint over HTTP (fast Playwright alternative, no browser)",
		Long: "Fetch a URL and assert on status/content/JSON/latency. Ideal for verifying\n" +
			"APIs, server-rendered pages, and health checks during verify/finish gates.\n" +
			"Does not render client-side JavaScript.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res := web.Run(cmd.Context(), web.Check{
				URL: args[0], Method: method, Body: body, TimeoutSec: timeoutSec,
				FollowRedirect: follow, Insecure: insecure, ExtractText: extractText,
				ExpectStatus: status, ExpectContains: contains, ExpectAbsent: absent,
				ExpectRegex: regex, ExpectJSONPath: jsonPath, ExpectJSONVal: jsonVal,
				MaxLatencyMs: maxLatency,
			})
			if asJSON {
				b, _ := json.MarshalIndent(res, "", "  ")
				fmt.Println(string(b))
			} else {
				printWebResult(res)
			}
			if !res.Passed {
				return fmt.Errorf("web check failed")
			}
			return nil
		},
	}
	c.Flags().StringVar(&method, "method", "GET", "HTTP method")
	c.Flags().StringVar(&body, "body", "", "request body")
	c.Flags().IntVar(&timeoutSec, "timeout", 15, "timeout seconds")
	c.Flags().BoolVar(&follow, "follow", false, "follow redirects")
	c.Flags().BoolVar(&insecure, "insecure", false, "skip TLS verification")
	c.Flags().BoolVar(&extractText, "extract-text", false, "print extracted text from HTML")
	c.Flags().IntVar(&status, "expect-status", 0, "assert HTTP status")
	c.Flags().StringArrayVar(&contains, "expect-contains", nil, "assert body contains string (repeatable)")
	c.Flags().StringArrayVar(&absent, "expect-absent", nil, "assert body lacks string (repeatable)")
	c.Flags().StringVar(&regex, "expect-regex", "", "assert body matches regex")
	c.Flags().StringVar(&jsonPath, "expect-json-path", "", "dotted JSON path to assert")
	c.Flags().StringVar(&jsonVal, "expect-json-value", "", "expected value at JSON path")
	c.Flags().IntVar(&maxLatency, "max-latency-ms", 0, "assert latency <= ms")
	c.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	return c
}

func printWebResult(res web.Result) {
	state := "PASS"
	if !res.Passed {
		state = "FAIL"
	}
	fmt.Printf("[%s] %s %s -> %d  %.1fms  %d bytes\n", state, res.Method, res.URL, res.StatusCode, res.LatencyMs, res.BodyBytes)
	if res.Error != "" {
		fmt.Println("  error:", res.Error)
	}
	for _, a := range res.Assertions {
		mark := "✓"
		if !a.Pass {
			mark = "✗"
		}
		fmt.Printf("  %s %s: %s\n", mark, a.Kind, a.Detail)
	}
	if res.Text != "" {
		fmt.Println("\n--- extracted text ---")
		fmt.Println(res.Text)
	}
}
