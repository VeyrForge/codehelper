package parser

import (
	"path/filepath"
	"strings"
)

// Next.js App Router densify helpers.
//
// Path roles (page/layout/route_handler/loading/error/…) also live in
// tsFrameworkRole for early tagging. This file owns named-export densify and
// a path-role fallback used when tsFrameworkRole did not already assign one:
//   - root/src middleware.ts → role=middleware
//   - generateMetadata / generateStaticParams → role=metadata|static_params
//   - GET/POST/… with next/server (or route.*) → role=route_handler
//   - files with 'use server' → exported functions role=server_action
//
// Additive only; dynamic action names are not guessed.

// nextExportRole returns a Next-specific role for a named export when path
// convention did not already assign one.
func nextExportRole(relPath, name string, frameworks []string, buf []byte) string {
	p := strings.ToLower(filepath.ToSlash(relPath))
	isNext := containsFramework(frameworks, string(FrameworkNextJS)) ||
		looksLikeNextAppRouterPath(p) || looksLikeNextMiddlewarePath(p) ||
		looksLikeNextServerActionFile(buf) ||
		strings.Contains(string(buf), "next/") || strings.Contains(string(buf), "next/server") ||
		strings.Contains(string(buf), "next/headers") || strings.Contains(string(buf), "next/navigation")
	if !isNext {
		return ""
	}
	if looksLikeNextMiddlewarePath(p) {
		return "middleware"
	}
	if role := nextNamedExportRole(name); role != "" {
		// HTTP method names densify as route_handler only in route.* files or
		// modules that clearly use next/server (shared handler modules).
		if role != "route_handler" || nextRouteHandlerContext(p, buf) {
			return role
		}
	}
	if role := nextAppRouterPathRole(p); role != "" {
		return role
	}
	if looksLikeNextServerActionFile(buf) {
		return "server_action"
	}
	return ""
}

// nextNamedExportRole maps well-known Next App Router export names to roles.
func nextNamedExportRole(name string) string {
	switch name {
	case "generateMetadata", "generateViewport", "generateImageMetadata":
		return "metadata"
	case "generateStaticParams":
		return "static_params"
	case "middleware":
		return "middleware"
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
		return "route_handler"
	default:
		return ""
	}
}

// nextAppRouterPathRole maps App Router convention filenames under app/ to roles.
func nextAppRouterPathRole(relPath string) string {
	p := strings.ToLower(filepath.ToSlash(relPath))
	if !strings.Contains(p, "/app/") && !strings.HasPrefix(p, "app/") {
		return ""
	}
	base := strings.ToLower(filepath.Base(p))
	switch {
	case strings.HasPrefix(base, "page."):
		return "page"
	case strings.HasPrefix(base, "layout."):
		return "layout"
	case strings.HasPrefix(base, "route."):
		return "route_handler"
	case strings.HasPrefix(base, "loading."):
		return "loading"
	case strings.HasPrefix(base, "error."):
		return "error"
	case strings.HasPrefix(base, "template."):
		return "template"
	case strings.HasPrefix(base, "default."):
		return "default"
	case strings.HasPrefix(base, "not-found."):
		return "not_found"
	default:
		return ""
	}
}

func nextRouteHandlerContext(relPath string, buf []byte) bool {
	p := strings.ToLower(filepath.ToSlash(relPath))
	if nextAppRouterPathRole(p) == "route_handler" {
		return true
	}
	base := strings.ToLower(filepath.Base(p))
	if strings.HasPrefix(base, "route.") {
		return true
	}
	s := string(buf)
	return strings.Contains(s, "next/server") || strings.Contains(s, "NextResponse") ||
		strings.Contains(s, "NextRequest")
}

func looksLikeNextMiddlewarePath(p string) bool {
	p = strings.ToLower(filepath.ToSlash(p))
	base := filepath.Base(p)
	if !strings.HasPrefix(base, "middleware.") {
		return false
	}
	// Root or src/middleware.* — not under app/ (App Router convention files).
	if strings.Contains(p, "/app/") || strings.HasPrefix(p, "app/") {
		return false
	}
	dir := filepath.ToSlash(filepath.Dir(p))
	return dir == "." || dir == "" || dir == "src" || strings.HasSuffix(dir, "/src")
}

func looksLikeNextServerActionFile(buf []byte) bool {
	if len(buf) == 0 {
		return false
	}
	// Prefer a directive near the top (first ~400 bytes) to avoid matching
	// string literals deeper in the file.
	head := buf
	if len(head) > 400 {
		head = head[:400]
	}
	s := string(head)
	return strings.Contains(s, "'use server'") || strings.Contains(s, `"use server"`) ||
		strings.Contains(s, "`use server`")
}
