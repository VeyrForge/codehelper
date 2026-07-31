package mcpsvc

import "github.com/mark3labs/mcp-go/mcp"

func ptrBool(b bool) *bool { return &b }

// annotReadOnlyClosedWorld: index/graph reads; no side effects on repo.
func annotReadOnlyClosedWorld() mcp.ToolOption {
	return mcp.WithToolAnnotation(mcp.ToolAnnotation{
		ReadOnlyHint:    ptrBool(true),
		DestructiveHint: ptrBool(false),
		IdempotentHint:  ptrBool(true),
		OpenWorldHint:   ptrBool(false),
	})
}

// annotReadOnlyOpenWorld: read-only but reaches the network (e.g. docs fetch).
func annotReadOnlyOpenWorld() mcp.ToolOption {
	return mcp.WithToolAnnotation(mcp.ToolAnnotation{
		ReadOnlyHint:    ptrBool(true),
		DestructiveHint: ptrBool(false),
		IdempotentHint:  ptrBool(true),
		OpenWorldHint:   ptrBool(true),
	})
}

// annotOpenWorld: may reach external systems (SSH, remote hosts); not read-only.
func annotOpenWorld() mcp.ToolOption {
	return mcp.WithToolAnnotation(mcp.ToolAnnotation{
		ReadOnlyHint:    ptrBool(false),
		DestructiveHint: ptrBool(false),
		IdempotentHint:  ptrBool(true),
		OpenWorldHint:   ptrBool(true),
	})
}

// annotVerify: runs user-supplied subprocess commands (argv/shell); not read-only.
func annotVerify() mcp.ToolOption {
	return mcp.WithToolAnnotation(mcp.ToolAnnotation{
		ReadOnlyHint:    ptrBool(false),
		DestructiveHint: ptrBool(false),
		IdempotentHint:  ptrBool(true),
		OpenWorldHint:   ptrBool(true),
	})
}

// annotIndexerWrite: mutates the codehelper graph index (e.g. rename apply, scip_import).
func annotIndexerWrite() mcp.ToolOption {
	return mcp.WithToolAnnotation(mcp.ToolAnnotation{
		ReadOnlyHint:    ptrBool(false),
		DestructiveHint: ptrBool(true),
		IdempotentHint:  ptrBool(false),
		OpenWorldHint:   ptrBool(false),
	})
}

// annotWorkspaceWrite: overwrites or patches files on disk.
func annotWorkspaceWrite() mcp.ToolOption {
	return mcp.WithToolAnnotation(mcp.ToolAnnotation{
		ReadOnlyHint:    ptrBool(false),
		DestructiveHint: ptrBool(true),
		IdempotentHint:  ptrBool(false),
		OpenWorldHint:   ptrBool(false),
	})
}

// annotWorkspaceRevert: restores prior file state; same token is idempotent.
func annotWorkspaceRevert() mcp.ToolOption {
	return mcp.WithToolAnnotation(mcp.ToolAnnotation{
		ReadOnlyHint:    ptrBool(false),
		DestructiveHint: ptrBool(true),
		IdempotentHint:  ptrBool(true),
		OpenWorldHint:   ptrBool(false),
	})
}

// annotTaskMutate: writes task files under .codehelper/tasks (non-destructive to source).
func annotTaskMutate() mcp.ToolOption {
	return mcp.WithToolAnnotation(mcp.ToolAnnotation{
		ReadOnlyHint:    ptrBool(false),
		DestructiveHint: ptrBool(false),
		IdempotentHint:  ptrBool(false),
		OpenWorldHint:   ptrBool(false),
	})
}

// annotProjectProfile: may write project_profile.json when refresh=true.
func annotProjectProfile() mcp.ToolOption {
	return mcp.WithToolAnnotation(mcp.ToolAnnotation{
		ReadOnlyHint:    ptrBool(false),
		DestructiveHint: ptrBool(false),
		IdempotentHint:  ptrBool(true),
		OpenWorldHint:   ptrBool(false),
	})
}
