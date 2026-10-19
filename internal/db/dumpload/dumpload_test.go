package dumpload

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
)

type copyBlock struct{ stmt, data string }

// split collects what streamStatements yields from r. Test-only — the
// production path streams to a database handle and never materializes
// the slices.
func split(tb testing.TB, r io.Reader, d dialect) ([]string, []copyBlock, error) {
	tb.Helper()
	var stmts []string
	var copies []copyBlock
	err := streamStatements(context.Background(), r, d, statementSink{
		onStmt: func(stmt string) error {
			stmts = append(stmts, strings.TrimSpace(stmt))
			return nil
		},
		onCopy: func(stmt string, data io.Reader) error {
			b, err := io.ReadAll(data)
			copies = append(copies, copyBlock{strings.TrimSpace(stmt), string(b)})
			return err
		},
	})
	return stmts, copies, err
}

func mustSplit(t *testing.T, s string, d dialect) []string {
	t.Helper()
	stmts, _, err := split(t, strings.NewReader(s), d)
	if err != nil {
		t.Fatalf("split %q: %v", s, err)
	}
	return stmts
}

func TestSplitStatements(t *testing.T) {
	for _, tc := range []struct {
		name string
		d    dialect
		in   string
		want []string
	}{
		{"simple", dialectMySQL, "SELECT 1; SELECT 2; SELECT 3", []string{"SELECT 1", "SELECT 2", "SELECT 3"}},
		{"semicolon in string", dialectMySQL, "INSERT INTO t VALUES ('a;b'); SELECT 1;", []string{"INSERT INTO t VALUES ('a;b')", "SELECT 1"}},
		{"line comment", dialectMySQL, "-- comment;\nSELECT 1;", []string{"SELECT 1"}},
		{"block comment dropped", dialectMySQL, "/* multi;\nline */ SELECT 1; /* trailing */ SELECT 2;", []string{"SELECT 1", "SELECT 2"}},
		{"backtick identifier", dialectMySQL, "CREATE TABLE `weird;name` (id INT); SELECT 1;", []string{"CREATE TABLE `weird;name` (id INT)", "SELECT 1"}},
		{
			"mysql backslash escape keeps later semicolons inside the literal", dialectMySQL,
			`INSERT INTO t VALUES ('O\'Brien','a;b'),('&amp;x');SELECT 1;`,
			[]string{`INSERT INTO t VALUES ('O\'Brien','a;b'),('&amp;x')`, "SELECT 1"},
		},
		{"doubled quote", dialectMySQL, "SELECT 'it''s;';SELECT 2;", []string{"SELECT 'it''s;'", "SELECT 2"}},
		{"mysql executable comments are kept", dialectMySQL, "/*!40101 SET NAMES utf8 */;\nSELECT 1;", []string{"/*!40101 SET NAMES utf8 */", "SELECT 1"}},
		{"mysql hash comment", dialectMySQL, "# note;\nSELECT 1;", []string{"SELECT 1"}},
		{"mysql -- needs a space", dialectMySQL, "SELECT 1--1;", []string{"SELECT 1--1"}},
		{
			"mysql DELIMITER block", dialectMySQL,
			"DELIMITER ;;\nCREATE TRIGGER tr AFTER INSERT ON t FOR EACH ROW BEGIN SET @a=1; SET @b=2; END ;;\nDELIMITER ;\nSELECT 1;",
			[]string{"CREATE TRIGGER tr AFTER INSERT ON t FOR EACH ROW BEGIN SET @a=1; SET @b=2; END", "SELECT 1"},
		},
		{
			"mysqldump trigger in executable comment under DELIMITER", dialectMySQL,
			"DELIMITER ;;\n/*!50003 CREATE*/ /*!50003 TRIGGER tr AFTER INSERT ON t FOR EACH ROW SET @a=1 */;;\nDELIMITER ;\n",
			[]string{"/*!50003 CREATE*/ /*!50003 TRIGGER tr AFTER INSERT ON t FOR EACH ROW SET @a=1 */"},
		},
		{
			"postgres dollar-quoted function body", dialectPostgres,
			"CREATE FUNCTION f() RETURNS int AS $$ BEGIN RETURN 1; END; $$ LANGUAGE plpgsql;\nSELECT 1;",
			[]string{"CREATE FUNCTION f() RETURNS int AS $$ BEGIN RETURN 1; END; $$ LANGUAGE plpgsql", "SELECT 1"},
		},
		{
			"postgres tagged dollar quote containing $$", dialectPostgres,
			"DO $body$ BEGIN PERFORM '$$;'; END $body$;SELECT 2;",
			[]string{"DO $body$ BEGIN PERFORM '$$;'; END $body$", "SELECT 2"},
		},
		{"postgres positional parameter is not a dollar quote", dialectPostgres, "PREPARE p AS SELECT $1;SELECT 2;", []string{"PREPARE p AS SELECT $1", "SELECT 2"}},
		{"postgres plain string keeps backslash", dialectPostgres, `SELECT 'a\';SELECT 2;`, []string{`SELECT 'a\'`, "SELECT 2"}},
		{"postgres E string escapes", dialectPostgres, `SELECT E'a\';b';SELECT 2;`, []string{`SELECT E'a\';b'`, "SELECT 2"}},
		{"postgres nested comments", dialectPostgres, "/* a /* b; */ c; */ SELECT 1;", []string{"SELECT 1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mustSplit(t, tc.in, tc.d); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestSplitPostgresCopyStreamsDataBlock(t *testing.T) {
	dump := "CREATE TABLE t (a int, b text);\n" +
		"COPY public.t (a, b) FROM stdin;\n1\tx;y\n2\t\\N\n\\.\n" +
		"SELECT setval('s', 2);\n"
	stmts, copies, err := split(t, strings.NewReader(dump), dialectPostgres)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"CREATE TABLE t (a int, b text)", "SELECT setval('s', 2)"}; !reflect.DeepEqual(stmts, want) {
		t.Errorf("stmts = %q, want %q", stmts, want)
	}
	if want := []copyBlock{{"COPY public.t (a, b) FROM stdin", "1\tx;y\n2\t\\N\n"}}; !reflect.DeepEqual(copies, want) {
		t.Errorf("copies = %q, want %q", copies, want)
	}
}

func TestSplitRejectsTruncatedInput(t *testing.T) {
	for _, tc := range []struct {
		d  dialect
		in string
	}{
		{dialectMySQL, "INSERT INTO t VALUES ('unterminated ; literal"},
		{dialectMySQL, "/* unterminated comment"},
		{dialectPostgres, "SELECT $$ never closed"},
		{dialectPostgres, "COPY t FROM stdin;\n1\n"},
	} {
		if _, _, err := split(t, strings.NewReader(tc.in), tc.d); err == nil || !strings.Contains(err.Error(), "unterminated") {
			t.Errorf("%q: err = %v, want an unterminated error", tc.in, err)
		}
	}
}

// FuzzSplitIsReadSizeIndependent: the split must not depend on how the
// input arrives, so a one-byte-at-a-time reader yields exactly what a
// whole-buffer reader does.
func FuzzSplitIsReadSizeIndependent(f *testing.F) {
	for _, s := range []string{
		"SELECT 1; SELECT 2",
		`INSERT INTO t VALUES ('a\'b', 'c;d');`,
		"DELIMITER ;;\nSELECT 1;;\nDELIMITER ;\n",
		"/*!40101 SET x=1 */;-- c\nSELECT 1;",
		"CREATE FUNCTION f() AS $$ x; $$;",
		"COPY t FROM stdin;\n1\n\\.\nSELECT 1;",
		"",
	} {
		f.Add(s, false)
		f.Add(s, true)
	}
	f.Fuzz(func(t *testing.T, in string, pg bool) {
		d := dialectMySQL
		if pg {
			d = dialectPostgres
		}
		wantStmts, wantCopies, wantErr := split(t, strings.NewReader(in), d)
		gotStmts, gotCopies, gotErr := split(t, iotest.OneByteReader(strings.NewReader(in)), d)
		if (wantErr == nil) != (gotErr == nil) || !reflect.DeepEqual(gotStmts, wantStmts) || !reflect.DeepEqual(gotCopies, wantCopies) {
			t.Fatalf("read size changed the split of %q:\n whole: %q %q %v\n bytes: %q %q %v",
				in, wantStmts, wantCopies, wantErr, gotStmts, gotCopies, gotErr)
		}
	})
}

// syntheticDump builds a mysqldump-shaped blob: comments, a CREATE,
// and many extended-INSERT statements carrying quoted string literals
// with embedded semicolons — the byte mix that dominates real dumps.
func syntheticDump(targetBytes int) string {
	var b strings.Builder
	b.WriteString("-- MySQL dump\n/*!40101 SET NAMES utf8 */;\n")
	b.WriteString("CREATE TABLE `t` (`id` INT, `v` TEXT);\n")
	row := "INSERT INTO `t` VALUES (1,'a;b\"c`d, lorem ipsum dolor sit amet');\n"
	for b.Len() < targetBytes {
		b.WriteString(row)
	}
	return b.String()
}

func BenchmarkStreamStatements(b *testing.B) {
	dump := syntheticDump(8 << 20)
	b.SetBytes(int64(len(dump)))
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := split(b, strings.NewReader(dump), dialectMySQL); err != nil {
			b.Fatal(err)
		}
	}
}
