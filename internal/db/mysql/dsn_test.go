package mysql

import (
	"testing"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/stubbedev/treeman/internal/config"
)

// TestConnDSNKeepsCredentialsVerbatim: the driver must read back exactly
// the user and password it was given, including characters URL escaping
// would rewrite, over both TCP and a unix socket.
func TestConnDSNKeepsCredentialsVerbatim(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  config.MysqlConn
		net  string
		addr string
	}{
		{"tcp", config.MysqlConn{Host: "127.0.0.1", Port: 3306, User: "kontainer", Password: "7.8-eV88~l(!U%qB"}, "tcp", "127.0.0.1:3306"},
		{"ipv6", config.MysqlConn{Host: "::1", Port: 3307, User: "a b", Password: "p@ss:w/rd?#&="}, "tcp", "[::1]:3307"},
		{"socket", config.MysqlConn{Host: "/run/mysqld/mysqld.sock", User: "root", Password: "x(y)%z"}, "unix", "/run/mysqld/mysqld.sock"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dsn, err := connDSN(tc.cfg)
			if err != nil {
				t.Fatal(err)
			}
			got, err := gomysql.ParseDSN(dsn)
			if err != nil {
				t.Fatal(err)
			}
			if got.User != tc.cfg.User || got.Passwd != tc.cfg.Password {
				t.Errorf("credentials = %q / %q, want %q / %q", got.User, got.Passwd, tc.cfg.User, tc.cfg.Password)
			}
			if got.Net != tc.net || got.Addr != tc.addr {
				t.Errorf("address = %s(%s), want %s(%s)", got.Net, got.Addr, tc.net, tc.addr)
			}
			if !got.ParseTime || !got.MultiStatements {
				t.Errorf("parseTime=%v multiStatements=%v, want both true", got.ParseTime, got.MultiStatements)
			}
		})
	}
}
