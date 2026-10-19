package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stubbedev/treeman/internal/config"
)

const secret = "hunter2-s3cret"

// JSON-shaped secrets (`"password":"…"`) slipped past the key pattern,
// which expected `password` to be followed directly by `:` or `=`.
func TestRedactSecretsCoversJSONAndKeepsItValid(t *testing.T) {
	in := `{"password":"` + secret + `","Password": "` + secret + `","dsn":"mysql://u:` + secret + `@h/db"}`
	out := redactSecrets(in)
	if strings.Contains(out, secret) {
		t.Fatalf("secret survived: %s", out)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("redaction broke the JSON: %v\n%s", err, out)
	}
}

func TestRedactMapHidesConfigCredentials(t *testing.T) {
	cfg := config.Config{}
	cfg.Connections.Mysql = &config.MysqlConn{Host: "db", User: "root", Password: secret}
	cfg.Connections.Postgres = &config.PostgresConn{Host: "pg", Password: "$PG_PASSWORD"}
	out, err := redactMap(map[string]any{"config": cfg})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), secret) {
		t.Fatalf("config_get leaked the password: %s", b)
	}
	if !strings.Contains(string(b), "$PG_PASSWORD") {
		t.Errorf("an env reference is not a secret and should stay visible: %s", b)
	}
}

func TestRedactYAMLKeepsCommentsAndEnvRefs(t *testing.T) {
	in := "# db settings\nconnections:\n  mysql:\n    password: " + secret + " # inline\n  postgres:\n    password: $PG_PASSWORD\n"
	out, err := redactYAML([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, secret) || !strings.Contains(out, "# db settings") || !strings.Contains(out, "$PG_PASSWORD") {
		t.Errorf("redacted yaml =\n%s", out)
	}
}

func TestRedactJSONAtUsesThePathLeaf(t *testing.T) {
	if got := redactJSONAt("connections.mysql.password", `"`+secret+`"`); strings.Contains(got, secret) {
		t.Errorf("config_set value leaked: %s", got)
	}
	if got := redactJSONAt(
		"connections.mysql",
		`{"host":"db","password":"`+secret+`"}`,
	); strings.Contains(got, secret) ||
		!strings.Contains(got, `"db"`) {
		t.Errorf("config_unset subtree = %s", got)
	}
}

// config_diff compared against the env-substituted resolved config and
// returned its credentials as the "old" side of a change.
func TestConfigDiffDoesNotLeakCurrentCredentials(t *testing.T) {
	repo := t.TempDir()
	body := "connections:\n  mysql:\n    host: db\n    user: root\n    password: " + secret + "\n"
	if err := os.WriteFile(filepath.Join(repo, ".treeman.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, out, err := configDiffTool(context.Background(), nil, configDiffIn{Repo: repo, Body: "connections:\n  mysql:\n    host: other\n"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), secret) {
		t.Fatalf("config_diff leaked the current password: %s", b)
	}
}
