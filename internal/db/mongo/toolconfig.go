package mongo

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// The MongoDB database tools take the connection string from a
// `--config` YAML file as well as from `--uri`. A `--uri` argument puts
// the credentials in the process arguments, which every local user can
// read through ps — for a container's processes too — so every tool
// call goes through a config file instead.

func toolConfigYAML(uri string) ([]byte, error) {
	b, err := yaml.Marshal(map[string]string{"uri": uri})
	if err != nil {
		return nil, fmt.Errorf("render mongo tool config: %w", err)
	}
	return b, nil
}

// ToolConfigFile writes a 0600 tool config carrying uri for a host-side
// mongodump / mongorestore and returns its path and a remover the
// caller must run once the tool exits.
func ToolConfigFile(uri string) (path string, remove func(), err error) {
	body, err := toolConfigYAML(uri)
	if err != nil {
		return "", nil, err
	}
	f, err := os.CreateTemp("", "treeman-mongo-*.yaml")
	if err != nil {
		return "", nil, fmt.Errorf("create mongo tool config: %w", err)
	}
	remove = func() { _ = os.Remove(f.Name()) }
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		remove()
		return "", nil, fmt.Errorf("write mongo tool config: %w", err)
	}
	if err := f.Close(); err != nil {
		remove()
		return "", nil, fmt.Errorf("write mongo tool config: %w", err)
	}
	return f.Name(), remove, nil
}

// containerToolConfig writes a tool config carrying uri inside the
// container — through the exec's stdin, never its arguments — and
// returns its in-container path and a remover.
func containerToolConfig(ctx context.Context, engineBin, cid, uri string) (path string, remove func(), err error) {
	body, err := toolConfigYAML(uri)
	if err != nil {
		return "", nil, err
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", nil, fmt.Errorf("name mongo tool config: %w", err)
	}
	path = "/tmp/_tm_mongo_cfg_" + hex.EncodeToString(suffix[:]) + ".yaml"
	cmd := exec.CommandContext(ctx, engineBin, "exec", "-i", cid, "sh", "-c", `umask 077 && cat > "$1"`, "sh", path)
	cmd.Stdin = bytes.NewReader(body)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", nil, fmt.Errorf("%s exec: write mongo tool config: %w (%s)", engineBin, err, strings.TrimSpace(string(out)))
	}
	remove = func() {
		rmCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(rmCtx, engineBin, "exec", cid, "rm", "-f", path).CombinedOutput(); err != nil {
			slog.Error("mongo tool config holding credentials was not removed from the container",
				"container", cid, "path", path, "err", err, "output", strings.TrimSpace(string(out)))
		}
	}
	return path, remove, nil
}
