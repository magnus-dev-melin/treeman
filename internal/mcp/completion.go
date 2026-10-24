package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stubbedev/treeman/internal/store"
)

// completionHandler dispatches MCP `completion/complete` requests to
// the right value source. Triggered when an interactive client tabs
// through prompt-argument fields or fills in a resource-template
// variable (`treeman://worktrees/{slug}/…`). The MCP spec only
// supports completion for prompt args + resource-template URI
// variables — not tool arguments — so this surface is narrower than
// it sounds.
//
// All sources are READ-ONLY against the SQLite store + the resolved
// config; no engine I/O happens here (latency matters — completions
// can fire on every keystroke).
func completionHandler(ctx context.Context, req *mcpsdk.CompleteRequest) (*mcpsdk.CompleteResult, error) {
	if req == nil || req.Params == nil || req.Params.Ref == nil {
		return emptyCompletion(), nil
	}
	ref := req.Params.Ref
	argName := req.Params.Argument.Name
	prefix := req.Params.Argument.Value

	var (
		values []string
		err    error
	)
	switch ref.Type {
	case "ref/prompt":
		values, err = completeForPromptArg(ctx, argName)
	case "ref/resource":
		if strings.HasPrefix(ref.URI, "treeman://worktrees/") && argName == "slug" {
			values, err = worktreeSlugCompletions(ctx)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("complete %s %q argument %q: %w", ref.Type, ref.Name+ref.URI, argName, err)
	}

	values = filterByPrefix(values, prefix)
	sort.Strings(values)

	// MCP caps the per-response page at 100 values; truncate so
	// HasMore reflects whether more would have been available.
	hasMore := false
	if len(values) > 100 {
		hasMore = true
		values = values[:100]
	}
	return &mcpsdk.CompleteResult{
		Completion: mcpsdk.CompletionResultDetails{
			Values:  values,
			HasMore: hasMore,
			Total:   len(values),
		},
	}, nil
}

func emptyCompletion() *mcpsdk.CompleteResult {
	return &mcpsdk.CompleteResult{
		Completion: mcpsdk.CompletionResultDetails{Values: []string{}},
	}
}

func completeForPromptArg(ctx context.Context, argName string) ([]string, error) {
	switch argName {
	case "worktree":
		return worktreeSlugCompletions(ctx)
	case "branch":
		return branchCompletions(ctx)
	case "run_id":
		return recentRunIDCompletions(ctx)
	case "fingerprint":
		return snapshotFingerprintCompletions(ctx)
	}
	return nil, nil
}

func queryCompletionStrings(ctx context.Context, what, q string, keep func(string) bool) ([]string, error) {
	st, err := openStore(ctx)
	if err != nil {
		return nil, fmt.Errorf("open the store to list %s: %w", what, err)
	}
	defer func() { _ = st.Close() }()
	rows, err := st.DB.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", what, err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scan %s: %w", what, err)
		}
		if keep(v) {
			out = append(out, v)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list %s: %w", what, err)
	}
	return out, nil
}

func nonEmpty(v string) bool { return v != "" }

func worktreeSlugCompletions(ctx context.Context) ([]string, error) {
	return queryCompletionStrings(ctx, "worktree slugs", `
		SELECT w.slug FROM worktrees w
		WHERE w.deleted_at IS NULL AND w.slug IS NOT NULL
		AND `+store.WorktreeNotTearingDown+`
		ORDER BY w.slug`, nonEmpty)
}

func branchCompletions(ctx context.Context) ([]string, error) {
	return queryCompletionStrings(ctx, "worktree branches", `
		SELECT DISTINCT w.branch FROM worktrees w
		WHERE w.deleted_at IS NULL AND w.branch IS NOT NULL AND w.branch != ''
		AND `+store.WorktreeNotTearingDown+`
		ORDER BY w.branch`, nonEmpty)
}

func recentRunIDCompletions(ctx context.Context) ([]string, error) {
	payloads, err := queryCompletionStrings(ctx, "recent run ids", `
		SELECT payload_json FROM events
		WHERE payload_json LIKE '%"run_id":"%'
		ORDER BY id DESC LIMIT 200`, nonEmpty)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var out []string
	for _, p := range payloads {
		id := extractRunID(p)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
		if len(out) >= 50 {
			break
		}
	}
	return out, nil
}

// extractRunID reaches into a payload JSON string and pulls the
// run_id value out without a full decode. Returns "" when the
// pattern doesn't match.
func extractRunID(payload string) string {
	const needle = `"run_id":"`
	i := strings.Index(payload, needle)
	if i < 0 {
		return ""
	}
	start := i + len(needle)
	end := strings.Index(payload[start:], `"`)
	if end < 0 {
		return ""
	}
	return payload[start : start+end]
}

func snapshotFingerprintCompletions(ctx context.Context) ([]string, error) {
	return queryCompletionStrings(ctx, "snapshot fingerprints", `
		SELECT fingerprint FROM snapshots ORDER BY last_used_at DESC LIMIT 100`, nonEmpty)
}

func filterByPrefix(values []string, prefix string) []string {
	if prefix == "" {
		return values
	}
	prefix = strings.ToLower(prefix)
	out := values[:0]
	for _, v := range values {
		if strings.HasPrefix(strings.ToLower(v), prefix) {
			out = append(out, v)
		}
	}
	return out
}
