// Package slug derives a worktree slug from the branch name / path,
// and exposes deterministic redis-db indices so worktrees retain
// their per-engine namespace across treeman upgrades.
//
// The SysV cksum reimplementation matches POSIX `cksum` byte-for-
// byte so external shell hooks that compute the same redis indices
// agree with treeman's slugs.
package slug

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"lukechampine.com/blake3"
)

// Source records where the slug came from.
type Source int

const (
	// SourceTicket — the slug was extracted from a Jira-style ticket
	// pattern (`[A-Z]+-\d+`) in the branch name or directory.
	SourceTicket Source = iota
	// SourcePathHash — fallback when no ticket was found; slug is a
	// stable `wt_<blake3(path)[:8]>`.
	SourcePathHash
	// SourceMain — derived by ForMain() for a repo's main worktree.
	// Always branch-driven (`main_<sanitised_branch>_<tag>`) so a non-ticket
	// branch like `develop` doesn't collapse to the repo's path hash,
	// which would share a DB across every branch ever checked out at
	// the repo root.
	SourceMain
)

// Slug is the worktree identifier feeding every per-engine template
// (`{slug}`, `{slug_dash}`, `{slug_redis_queue}`, `{slug_redis_cache}`).
type Slug struct {
	Value  string
	Source Source
}

// Dashed returns the slug with `_` replaced by `-`. Used by the
// `{slug_dash}` template key for S3 / minio buckets where
// underscores are invalid.
func (s Slug) Dashed() string {
	return strings.ReplaceAll(s.Value, "_", "-")
}

// RedisDBRange returns the inclusive logical-DB window the slug →
// redis-index derivation maps into. Default 6..15.
func RedisDBRange() (lo, hi uint8) { return redisDBMin, redisDBMax }

var redisDBMin, redisDBMax uint8 = 6, 15

// SetRedisDBRange pins the logical-DB window RedisIndices maps into.
// The config loaders call it from connections.redis db_min/db_max so
// hosted Redis tiers and proxies that expose fewer than 16 databases
// still get deterministic per-slug indices. Not concurrency-safe by
// design: like ui.SetColorMode it's set once at config load, before
// any template rendering fans out.
func SetRedisDBRange(lo, hi int) error {
	if lo < 0 || hi > 15 {
		return fmt.Errorf("redis db range [%d, %d] outside logical DB space 0..15", lo, hi)
	}
	if lo > hi {
		return fmt.Errorf("redis db range min %d > max %d", lo, hi)
	}
	// Bounds-checked above; the conversions cannot truncate.
	redisDBMin, redisDBMax = uint8(lo), uint8(hi) //nolint:gosec // range-validated 0..15
	return nil
}

// RedisIndices returns (queue_db, cache_db), each inside the
// configured window (default 6..15), derived deterministically from
// the slug via POSIX `cksum`. Generalised window: the digit-pair
// selection becomes modulo span, so a narrowed range (e.g. a
// single-DB hosted server: 0..0) collapses every slug onto the same
// index deterministically — prefix isolation keeps worktrees distinct.
func (s Slug) RedisIndices() (queueDB, cacheDB uint8) {
	h := sysvCksum([]byte(s.Value))
	span := int(redisDBMax-redisDBMin) + 1
	lo := int(redisDBMin)
	queueDB = uint8(int(h)%span + lo)    //nolint:gosec // result is within the 0..15 window by construction
	cacheDB = uint8(int(h/10)%span + lo) //nolint:gosec // same window guarantee
	return
}

var ticketRe = regexp.MustCompile(`([A-Z]+)-([0-9]+)`)

// For derives a linked worktree's slug purely from its filesystem
// PATH. The `branch` parameter is accepted for signature compatibility
// with the call sites that happen to know the branch, but is
// deliberately IGNORED — folding the branch into the slug is what the
// two failure modes this function guards against both come down to.
//
// Two properties fall out of keying strictly on the canonical path:
//
//   - Branch-stable. The directory is fixed for a worktree's lifetime,
//     so the slug — and with it the worktree's DB names, patched
//     `.env`, ports, redis indices — does NOT churn when the branch
//     swaps underneath it via an in-worktree `git checkout`. This is
//     the foundation `branch_scoped` swapping relies on, and the reason
//     ResolveIdentity has always called us with an empty branch.
//
//   - Collision-free across a shared ticket. Two worktrees whose names
//     embed the SAME Jira ticket (`PROJ-1234` checked out in two
//     different directories, or two branches of one ticket) get
//     DISTINCT slugs, because the path hash always disambiguates. The
//     previous derivation returned a bare `proj_1234` for both and
//     silently overlapped their storage — same DB names, same redis
//     DBs, same patched `.env`.
//
// A ticket-named directory keeps a readable prefix for ergonomics
// (`proj_1234_<pathhash>`); a generic directory is `wt_<pathhash>`. The
// path hash is the sole carrier of uniqueness in both shapes.
func For(worktreePath string, _ string) Slug {
	tag := pathTag(worktreePath)
	if ticket, ok := extractTicket(filepath.Base(worktreePath)); ok {
		return Slug{Value: composeTicket(ticket, tag), Source: SourceTicket}
	}
	return Slug{Value: "wt_" + tag, Source: SourcePathHash}
}

// slugSuffix carries the optional TREEMAN_SLUG_SUFFIX namespace tag
// (CI job disambiguation, #62). Read once from the env at package
// init — the value cannot meaningfully change mid-process, and every
// For/ForMain call site (CLI, daemon, MCP) must agree on it. It is
// folded into every slug's tag, so unset means byte-identical slugs to
// a suffix-less build.
var slugSuffix string

func init() { SetSlugSuffix(os.Getenv("TREEMAN_SLUG_SUFFIX")) }

// SetSlugSuffix pins the namespace suffix mixed into every slug
// derivation. CI recipe on a shared runner or shared DB server:
//
//	TREEMAN_SLUG_SUFFIX=$GITHUB_RUN_ID treeman prepare --no-daemon
//
// so two jobs for the same branch render distinct database names
// instead of clobbering each other's `main_<branch>_<tag>` schemas.
// Empty clears it.
func SetSlugSuffix(s string) { slugSuffix = s }

// pathTag is the path-keyed tag of every linked-worktree slug. The
// canonical absolute path is used; it falls back to the raw path when
// Abs fails (e.g. the cwd was removed) so a slug is still produced.
func pathTag(worktreePath string) string {
	canonical, err := filepath.Abs(worktreePath)
	if err != nil {
		canonical = worktreePath
	}
	return tag(canonical)
}

// tag is the stable 8-hex-char blake3 digest that ends every slug. The
// namespace suffix (when set) is folded into the digest so identical
// inputs land in distinct namespaces. Every slug ending in a fixed-width
// tag is what makes slugs prefix-free: no slug is a prefix of another,
// so a prefix drop of one slug's names never reaches a sibling's.
func tag(s string) string {
	sum := blake3.Sum256([]byte(s + "\x00" + slugSuffix))
	return hexEncode(sum[:])[:8]
}

// composeTicket joins a readable prefix with its tag inside the 32-char
// identifier budget every engine's name template must fit. Uniqueness
// lives in the tag, never the prefix, so truncating an over-long prefix
// can't collide two distinct inputs.
func composeTicket(readable, tag string) string {
	out := readable + "_" + tag
	if len(out) <= 32 {
		return out
	}
	keep := max(32-1-len(tag), 1)
	return strings.TrimRight(readable[:keep], "_") + "_" + tag
}

// ForMain derives the slug for a repo's main worktree (the repo root
// checkout). The branch drives it, so checking out a non-ticket branch
// like `develop` at the repo root gets its own databases instead of
// sharing one path-hash slug across every branch.
//
// The readable part is the sanitised branch; the tag is keyed on the
// raw branch name, so branches that sanitise alike (`feature/foo`,
// `feature-foo`, `Feature_Foo`) still get distinct slugs.
//
// Detached HEAD (empty branch) gets `main_detached_<tag>` keyed off the
// repo path so two detached repos don't collide. Empty branch is the
// edge case at boot before the daemon has detected HEAD.
func ForMain(worktreePath string, branch string) Slug {
	if branch == "" {
		return Slug{Value: "main_detached_" + tag(worktreePath), Source: SourceMain}
	}
	readable := "main"
	if s := sanitiseBranch(branch); s != "" {
		readable += "_" + s
	}
	return Slug{Value: composeTicket(readable, tag(branch)), Source: SourceMain}
}

// sanitiseBranch lowercases the branch name and replaces every
// non-alphanumeric run with a single underscore so the result is a
// valid identifier across MySQL/Postgres/Redis prefixes. Leading and
// trailing underscores are trimmed.
func sanitiseBranch(branch string) string {
	var b strings.Builder
	prevUnderscore := true
	for _, r := range strings.ToLower(branch) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevUnderscore = false
		default:
			if !prevUnderscore {
				b.WriteByte('_')
				prevUnderscore = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}

// extractTicket pulls a Jira-style `[A-Z]+-\d+` ticket out of s and
// renders it `prefix_num` (lowercased). Length budgeting and the
// path-hash suffix are composeTicket's job, so no truncation or hashing
// happens here.
func extractTicket(s string) (string, bool) {
	caps := ticketRe.FindStringSubmatch(s)
	if caps == nil {
		return "", false
	}
	return strings.ToLower(caps[1]) + "_" + caps[2], true
}

// sysvCksum reproduces the POSIX `cksum` (SysV) algorithm. CRC-32
// over (data || length octets little-endian), polynomial
// 0x04C11DB7, initial 0, final NOT.
//
// We need this in-process because the bash hooks use POSIX `cksum`
// and treeman's redis indices must agree with hand-computed ones.
func sysvCksum(input []byte) uint32 {
	var crc uint32
	for _, b := range input {
		crc = crcUpdate(crc, b)
	}
	length := uint64(len(input))
	for length != 0 {
		crc = crcUpdate(crc, byte(length&0xff))
		length >>= 8
	}
	return ^crc
}

func crcUpdate(crc uint32, b byte) uint32 {
	crc ^= uint32(b) << 24
	for range 8 {
		if crc&0x80000000 != 0 {
			crc = (crc << 1) ^ 0x04C11DB7
		} else {
			crc <<= 1
		}
	}
	return crc
}

const hexDigits = "0123456789abcdef"

func hexEncode(b []byte) string {
	out := make([]byte, 0, len(b)*2)
	for _, x := range b {
		out = append(out, hexDigits[x>>4], hexDigits[x&0x0f])
	}
	return string(out)
}
