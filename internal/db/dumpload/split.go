package dumpload

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// dialect selects the SQL lexing rules the statement splitter applies.
type dialect uint8

const (
	// dialectMySQL: backslash escapes inside '…' and "…", backtick
	// identifiers, `#` and `-- ` line comments, DELIMITER directives,
	// and executable `/*! … */` / optimizer-hint `/*+ … */` comments
	// kept verbatim because the server runs them.
	dialectMySQL dialect = iota
	// dialectPostgres: "…" identifiers, backslash escapes only in E'…'
	// strings, nested block comments, $tag$ dollar quoting, and
	// COPY … FROM stdin data blocks.
	dialectPostgres
)

// statementSink receives what the splitter finds: each statement, and
// for a Postgres COPY … FROM stdin the statement together with a
// reader over its data block. onCopy must consume data to EOF.
type statementSink struct {
	onStmt func(stmt string) error
	onCopy func(stmt string, data io.Reader) error
}

// streamStatements splits the dump in r into statements and hands each
// to sink in order. Memory is bounded
// by the largest single statement; a COPY data block is streamed, never
// buffered whole. Blank statements and plain comments are skipped.
func streamStatements(ctx context.Context, r io.Reader, d dialect, sink statementSink) error {
	s := &splitter{r: bufio.NewReaderSize(r, 1<<20), d: d, delim: ";"}
	for n := 0; ; n++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		stmt, err := s.next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("split dump after statement #%d: %w", n, err)
		}
		if d == dialectPostgres && copyFromStdin.MatchString(stmt) {
			if sink.onCopy == nil {
				return fmt.Errorf("dump statement #%d is COPY … FROM stdin, which this loader cannot stream", n)
			}
			err = sink.onCopy(stmt, &copyData{r: s.r, skipRest: true})
		} else {
			err = sink.onStmt(stmt)
		}
		if err != nil {
			return fmt.Errorf("apply dump stmt #%d: %w", n, err)
		}
	}
}

var copyFromStdin = regexp.MustCompile(`(?is)^\s*COPY\b.*\bFROM\s+STDIN\b`)

type splitter struct {
	r     *bufio.Reader
	d     dialect
	delim string
	buf   bytes.Buffer
}

// next returns the next non-blank statement without its delimiter, or
// io.EOF once the input is exhausted.
func (s *splitter) next() (string, error) {
	s.buf.Reset()
	for {
		b, err := s.r.ReadByte()
		if errors.Is(err, io.EOF) {
			return s.take(io.EOF)
		}
		if err != nil {
			return "", err
		}
		done, err := s.step(b)
		if err != nil {
			return "", err
		}
		if done {
			if stmt, err := s.take(nil); stmt != "" || err != nil {
				return stmt, err
			}
		}
	}
}

// take returns the buffered statement, or eof when it is blank.
func (s *splitter) take(eof error) (string, error) {
	stmt := s.buf.String()
	s.buf.Reset()
	if strings.TrimSpace(stmt) == "" {
		if eof != nil {
			return "", eof
		}
		return "", nil
	}
	return stmt, nil
}

// step consumes the top-level byte b, reporting whether it ended a
// statement.
func (s *splitter) step(b byte) (bool, error) {
	mysql := s.d == dialectMySQL
	switch {
	case mysql && s.atStatementStart() && (b == 'D' || b == 'd') && s.peekFold("ELIMITER") && s.peekSpaceAt(len("ELIMITER")):
		return false, s.readDelimiter()
	case b == s.delim[0] && s.peekIs(s.delim[1:]):
		_, _ = s.r.Discard(len(s.delim) - 1)
		return true, nil
	case b == '$' && !mysql && !s.afterIdentByte():
		return false, s.dollar()
	}
	if handled, err := s.comment(b); handled || err != nil {
		return false, err
	}
	if q, backslash, ok := s.quoteOpener(b); ok {
		s.buf.WriteByte(b)
		return false, s.quoted(q, backslash)
	}
	s.buf.WriteByte(b)
	return false, nil
}

// comment skips (or, for MySQL executable comments, keeps) a comment
// starting at b, reporting whether b opened one.
func (s *splitter) comment(b byte) (bool, error) {
	mysql := s.d == dialectMySQL
	switch {
	case b == '-' && s.peekIs("-") && (!mysql || s.peekSpaceAt(1)):
		return true, s.skipLine()
	case b == '#' && mysql:
		return true, s.skipLine()
	case b == '/' && s.peekIs("*"):
		_, _ = s.r.Discard(1)
		return true, s.blockComment()
	}
	return false, nil
}

// quoteOpener reports whether b opens a quoted string or identifier,
// and whether a backslash escapes inside it.
func (s *splitter) quoteOpener(b byte) (q byte, backslash, ok bool) {
	mysql := s.d == dialectMySQL
	switch {
	case b == '\'':
		return b, mysql || s.afterEPrefix(), true
	case b == '"':
		return b, mysql, true
	case b == '`' && mysql:
		return b, false, true
	}
	return 0, false, false
}

func (s *splitter) atStatementStart() bool {
	return len(bytes.TrimSpace(s.buf.Bytes())) == 0
}

// readDelimiter handles a MySQL client `DELIMITER xx` line: the rest
// of the line is the new statement terminator, and the line itself is
// never sent to the server.
func (s *splitter) readDelimiter() error {
	_, _ = s.r.Discard(len("ELIMITER"))
	line, err := s.r.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	delim := strings.TrimSpace(line)
	if delim == "" {
		return errors.New("DELIMITER without a delimiter")
	}
	s.delim = delim
	s.buf.Reset()
	return nil
}

// quoted copies a quoted string or identifier through its closing
// quote. A doubled quote is an escaped quote; with backslash, `\x`
// escapes x.
func (s *splitter) quoted(q byte, backslash bool) error {
	for {
		b, err := s.r.ReadByte()
		if err != nil {
			return unterminated(fmt.Sprintf("%c-quoted text", q), err)
		}
		s.buf.WriteByte(b)
		switch {
		case backslash && b == '\\':
			e, err := s.r.ReadByte()
			if err != nil {
				return unterminated(fmt.Sprintf("%c-quoted text", q), err)
			}
			s.buf.WriteByte(e)
		case b == q && s.peekIs(string(q)):
			_, _ = s.r.Discard(1)
			s.buf.WriteByte(q)
		case b == q:
			return nil
		}
	}
}

// blockComment handles a comment whose `/*` was just consumed. MySQL's
// `/*! … */` and `/*+ … */` are code the server runs and are kept
// verbatim; other comments are dropped. Postgres comments nest.
func (s *splitter) blockComment() error {
	keep := false
	if s.d == dialectMySQL {
		if p, _ := s.r.Peek(1); len(p) == 1 && (p[0] == '!' || p[0] == '+') {
			keep = true
			s.buf.WriteString("/*")
		}
	}
	for depth := 1; ; {
		b, err := s.r.ReadByte()
		if err != nil {
			return unterminated("block comment", err)
		}
		switch {
		case b == '*' && s.peekIs("/"):
			_, _ = s.r.Discard(1)
			if keep {
				s.buf.WriteString("*/")
			}
			if depth--; depth == 0 {
				return nil
			}
		case b == '/' && s.peekIs("*") && s.d == dialectPostgres:
			_, _ = s.r.Discard(1)
			depth++
		case keep:
			s.buf.WriteByte(b)
		}
	}
}

// dollar handles a `$` at top level in Postgres: `$tag$ … $tag$` (tag
// optional) is a dollar-quoted string copied verbatim; anything else
// is a literal `$`.
func (s *splitter) dollar() error {
	tag, ok := s.peekDollarTag()
	if !ok {
		s.buf.WriteByte('$')
		return nil
	}
	_, _ = s.r.Discard(len(tag) + 1)
	closer := "$" + tag + "$"
	s.buf.WriteString(closer)
	for {
		b, err := s.r.ReadByte()
		if err != nil {
			return unterminated("dollar-quoted string "+closer, err)
		}
		s.buf.WriteByte(b)
		if b == '$' && bytes.HasSuffix(s.buf.Bytes(), []byte(closer)) {
			return nil
		}
	}
}

// peekDollarTag reports the tag of a dollar quote whose leading `$` was
// just consumed: identifier characters (not starting with a digit,
// which would be a positional parameter) followed by `$`.
func (s *splitter) peekDollarTag() (string, bool) {
	for n := 1; ; n++ {
		p, err := s.r.Peek(n)
		if len(p) < n {
			return "", false
		}
		c := p[n-1]
		if c == '$' {
			return string(p[:n-1]), true
		}
		if !isIdentByte(c) || (n == 1 && c >= '0' && c <= '9') || err != nil {
			return "", false
		}
	}
}

func (s *splitter) skipLine() error {
	_, err := s.r.ReadString('\n')
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func (s *splitter) peekIs(want string) bool {
	if want == "" {
		return true
	}
	p, _ := s.r.Peek(len(want))
	return string(p) == want
}

func (s *splitter) peekFold(want string) bool {
	p, _ := s.r.Peek(len(want))
	return strings.EqualFold(string(p), want)
}

// peekSpaceAt reports whether the byte at offset i ahead is whitespace
// or a control byte, or the input ends there.
func (s *splitter) peekSpaceAt(i int) bool {
	p, _ := s.r.Peek(i + 1)
	return len(p) <= i || p[i] <= ' '
}

// afterEPrefix reports whether the quote about to open is a Postgres
// E'…' escape string: the buffer ends in a standalone E.
func (s *splitter) afterEPrefix() bool {
	b := s.buf.Bytes()
	n := len(b)
	return n > 0 && (b[n-1] == 'E' || b[n-1] == 'e') && (n == 1 || !isIdentByte(b[n-2]))
}

func (s *splitter) afterIdentByte() bool {
	b := s.buf.Bytes()
	return len(b) > 0 && (isIdentByte(b[len(b)-1]) || b[len(b)-1] == '$')
}

func isIdentByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c >= 0x80
}

func unterminated(what string, err error) error {
	if errors.Is(err, io.EOF) {
		return fmt.Errorf("unterminated %s at end of dump", what)
	}
	return err
}

// copyData reads a COPY … FROM stdin data block line by line, ending
// before the `\.` terminator line (which it consumes). The rest of the
// COPY statement's own line is skipped first. A data block that ends
// without `\.` is an error.
type copyData struct {
	r        *bufio.Reader
	skipRest bool
	midLine  bool
	pending  []byte
	done     bool
}

func (c *copyData) Read(p []byte) (int, error) {
	for len(c.pending) == 0 {
		if c.done {
			return 0, io.EOF
		}
		if err := c.fill(); err != nil {
			return 0, err
		}
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

func (c *copyData) fill() error {
	if c.skipRest {
		c.skipRest = false
		_, err := c.r.ReadString('\n')
		return unterminated("COPY data block", err)
	}
	line, err := c.r.ReadSlice('\n')
	whole := !errors.Is(err, bufio.ErrBufferFull)
	if err != nil && whole {
		return unterminated("COPY data block", err)
	}
	if !c.midLine && whole && string(bytes.TrimRight(line, "\r\n")) == `\.` {
		c.done = true
		return nil
	}
	c.midLine = !whole
	c.pending = append(c.pending[:0], line...)
	return nil
}
