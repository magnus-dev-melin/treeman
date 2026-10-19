package mcp

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/stubbedev/treeman/internal/yamlpatch"
)

// Everything an MCP client reads is forwarded to an LLM, so credentials
// are scrubbed first. Conservative by design — a false positive hides a
// value, a false negative leaks one.

const redacted = "***REDACTED***"

var (
	// URI userinfo: scheme://user:password@host.
	uriUserinfo = regexp.MustCompile(`([a-z][a-z0-9+.-]*://)([^:/@\s]+):([^@\s]+)@`)
	// A secret-named key followed by its value, in env (`KEY=v`), YAML
	// (`key: v`) or JSON (`"key": "v"`) shape. Only the value is
	// replaced, so JSON payloads stay well-formed.
	secretAssignment = regexp.MustCompile(
		`(?i)(\b(?:password|passwd|secret|token|api[_-]?key|access[_-]?key|secret[_-]?key|private[_-]?key|auth)["']?\s*[:=]\s*["']?)([^\s'",}\n]+)`,
	)
	// Self-identifying token shapes: AWS access key ids, GitHub tokens,
	// JWTs.
	secretTokens = []*regexp.Regexp{
		regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
		regexp.MustCompile(`\bgh[pos]_[A-Za-z0-9]{36,}\b`),
		regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`),
	}
	// envRef is a `$NAME` / `${NAME}` reference: it names where a
	// secret comes from and is not one itself.
	envRef = regexp.MustCompile(`^\$\{?[A-Za-z_][A-Za-z0-9_]*\}?$`)
)

// redactSecrets scrubs credential-shaped text out of free-form strings
// (hook output, event messages and payloads). Idempotent.
func redactSecrets(s string) string {
	if s == "" {
		return s
	}
	out := uriUserinfo.ReplaceAllString(s, "$1$2:"+redacted+"@")
	out = secretAssignment.ReplaceAllString(out, "${1}"+redacted)
	for _, re := range secretTokens {
		out = re.ReplaceAllString(out, redacted)
	}
	return out
}

// isSecretKey reports whether a config key or path element names a
// credential (Password, secret_key, api-token, …).
func isSecretKey(key string) bool {
	k := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(key))
	for _, s := range []string{"password", "passwd", "secret", "token", "apikey", "accesskey", "privatekey", "credential"} {
		if strings.Contains(k, s) {
			return true
		}
	}
	return k == "auth" || k == "pass" || k == "pwd"
}

// redactValue returns v with every value held under a secret-named key
// replaced (env references excepted) and credentials scrubbed from all
// other strings. key is the map key or path element v sits under.
func redactValue(key string, v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = redactValue(k, x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = redactValue(key, x)
		}
		return out
	case string:
		if isSecretKey(key) && t != "" && !envRef.MatchString(t) {
			return redacted
		}
		return redactSecrets(t)
	case nil, bool:
		return t
	default:
		if isSecretKey(key) {
			return redacted
		}
		return t
	}
}

// redactedJSON marshals v after redacting it structurally.
func redactedJSON(v any, indent bool) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var generic any
	if err := json.Unmarshal(b, &generic); err != nil {
		return nil, err
	}
	generic = redactValue("", generic)
	if indent {
		return json.MarshalIndent(generic, "", "  ")
	}
	return json.Marshal(generic)
}

// redactJSONAt redacts a JSON document that sits at the dotted config
// path `path` (its last element decides whether a bare scalar is a
// secret). Undecodable input is returned scrubbed as text.
func redactJSONAt(path, js string) string {
	if js == "" {
		return js
	}
	var v any
	if err := json.Unmarshal([]byte(js), &v); err != nil {
		return redactSecrets(js)
	}
	leaf := path
	if _, after, ok := strings.CutLast(path, "."); ok {
		leaf = after
	}
	b, err := json.Marshal(redactValue(leaf, v))
	if err != nil {
		return redactSecrets(js)
	}
	return string(b)
}

// redactYAML returns a YAML document with secret-named values replaced
// and credentials scrubbed from every other scalar, keeping its
// comments and key order.
func redactYAML(body []byte) (string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return "", fmt.Errorf("parse yaml: %w", err)
	}
	redactNode(&doc, "")
	out, err := yamlpatch.Marshal(&doc)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func redactNode(n *yaml.Node, key string) {
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			redactNode(n.Content[i+1], n.Content[i].Value)
		}
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range n.Content {
			redactNode(c, key)
		}
	case yaml.ScalarNode:
		if isSecretKey(key) && n.Value != "" && !envRef.MatchString(n.Value) {
			n.Value, n.Tag, n.Style = redacted, "!!str", 0
			return
		}
		n.Value = redactSecrets(n.Value)
	case yaml.AliasNode:
	}
}
