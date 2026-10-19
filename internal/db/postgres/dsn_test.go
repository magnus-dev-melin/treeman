package postgres

import (
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/stubbedev/treeman/internal/config"
)

// QueryEscape wrote a space as '+', which pgx's userinfo and path
// decoding keep literally, so these credentials and names never
// reached the server intact.
func TestDSNRoundTripsThroughPgx(t *testing.T) {
	const user, pass, db = "app user", "p w+d:@/?#%", "my db"
	for _, host := range []string{"127.0.0.1", "/var/run/postgresql"} {
		got, err := pgconn.ParseConfig(dsn(config.PostgresConn{Host: host, Port: 5432, User: user, Password: pass}, db))
		if err != nil {
			t.Fatalf("host %s: parse: %v", host, err)
		}
		if got.User != user || got.Password != pass || got.Database != db || got.Host != host {
			t.Errorf("host %s: parsed user=%q pass=%q db=%q host=%q", host, got.User, got.Password, got.Database, got.Host)
		}
	}
}
