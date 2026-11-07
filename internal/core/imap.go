package core

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Just enough IMAP to find Bandcamp's download emails in your mailbox: log in, open the inbox
// read-only (EXAMINE, BODY.PEEK: nothing is marked as read, moved or deleted), search, fetch.

type imapConn struct {
	c   net.Conn
	r   *bufio.Reader
	tag int
}

func imapDial(ctx context.Context, hostport string) (*imapConn, error) {
	if !strings.Contains(hostport, ":") {
		hostport += ":993"
	}
	host, _, _ := net.SplitHostPort(hostport)
	d := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 20 * time.Second}, Config: &tls.Config{ServerName: host}}
	c, err := d.DialContext(ctx, "tcp", hostport)
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", hostport, err)
	}
	ic := &imapConn{c: c, r: bufio.NewReader(c)}
	c.SetDeadline(time.Now().Add(60 * time.Second))
	if _, err := ic.r.ReadString('\n'); err != nil { // greeting
		c.Close()
		return nil, err
	}
	return ic, nil
}

func (ic *imapConn) close() {
	ic.cmd("LOGOUT")
	ic.c.Close()
}

// imapQuote makes a quoted string.
func imapQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

type imapLine struct {
	text     string   // the line with literals replaced by {n}
	literals [][]byte // in order
}

// cmd sends a command and reads its untagged responses until the tagged status.
func (ic *imapConn) cmd(format string, args ...any) ([]imapLine, error) {
	ic.tag++
	tag := fmt.Sprintf("A%03d", ic.tag)
	ic.c.SetDeadline(time.Now().Add(90 * time.Second))
	if _, err := fmt.Fprintf(ic.c, "%s %s\r\n", tag, fmt.Sprintf(format, args...)); err != nil {
		return nil, err
	}
	var out []imapLine
	for {
		line, err := ic.readLine()
		if err != nil {
			return out, err
		}
		if strings.HasPrefix(line.text, tag+" ") {
			status := strings.TrimPrefix(line.text, tag+" ")
			if strings.HasPrefix(status, "OK") {
				return out, nil
			}
			return out, fmt.Errorf("mail server: %s", status)
		}
		out = append(out, line)
	}
}

var reLiteral = regexp.MustCompile(`\{(\d+)\}$`)

func (ic *imapConn) readLine() (imapLine, error) {
	var l imapLine
	var b strings.Builder
	for {
		s, err := ic.r.ReadString('\n')
		if err != nil {
			return l, err
		}
		s = strings.TrimRight(s, "\r\n")
		b.WriteString(s)
		m := reLiteral.FindStringSubmatch(s)
		if m == nil {
			l.text = b.String()
			return l, nil
		}
		n, _ := strconv.Atoi(m[1])
		if n > 50<<20 {
			return l, errors.New("message too big")
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(ic.r, buf); err != nil {
			return l, err
		}
		l.literals = append(l.literals, buf)
	}
}

// fetchBandcampMails returns the raw messages from Bandcamp received since the given day.
func fetchBandcampMails(ctx context.Context, hostport, user, pass string, since time.Time) ([][]byte, error) {
	ic, err := imapDial(ctx, hostport)
	if err != nil {
		return nil, err
	}
	defer ic.close()
	if _, err := ic.cmd("LOGIN %s %s", imapQuote(user), imapQuote(pass)); err != nil {
		return nil, fmt.Errorf("logging in to %s: %w (most mail services want an app password here; for Gmail choose \"Gmail: sign in with Google\")", hostport, err)
	}
	if _, err := ic.cmd("EXAMINE INBOX"); err != nil {
		return nil, err
	}
	lines, err := ic.cmd(`UID SEARCH SINCE %s FROM "bandcamp.com"`, since.Format("02-Jan-2006"))
	if err != nil {
		return nil, err
	}
	var uids []string
	for _, l := range lines {
		if rest, ok := strings.CutPrefix(l.text, "* SEARCH"); ok {
			uids = append(uids, strings.Fields(rest)...)
		}
	}
	if len(uids) == 0 {
		return nil, nil
	}
	if len(uids) > 50 {
		uids = uids[len(uids)-50:]
	}
	lines, err = ic.cmd("UID FETCH %s (BODY.PEEK[])", strings.Join(uids, ","))
	if err != nil {
		return nil, err
	}
	var msgs [][]byte
	for _, l := range lines {
		msgs = append(msgs, l.literals...)
	}
	return msgs, nil
}

var reBandcampDownload = regexp.MustCompile(`https?://(?:[a-z0-9-]+\.)?bandcamp\.com/download\?[^\s"'<>]+`)

// bandcampLinks finds the download links in an email.
func bandcampLinks(raw []byte) []string {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	text := mailText(msg.Header.Get("Content-Type"), msg.Header.Get("Content-Transfer-Encoding"), msg.Body)
	seen := map[string]bool{}
	var out []string
	for _, l := range reBandcampDownload.FindAllString(html.UnescapeString(text), -1) {
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

func mailText(ctype, encoding string, body io.Reader) string {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "quoted-printable":
		body = quotedprintable.NewReader(body)
	case "base64":
		body = base64.NewDecoder(base64.StdEncoding, &newlineStripper{r: body})
	}
	mt, params, _ := mime.ParseMediaType(ctype)
	if strings.HasPrefix(mt, "multipart/") {
		var b strings.Builder
		mr := multipart.NewReader(body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			b.WriteString(mailText(p.Header.Get("Content-Type"), p.Header.Get("Content-Transfer-Encoding"), p))
			b.WriteString("\n")
		}
		return b.String()
	}
	data, _ := io.ReadAll(io.LimitReader(body, 5<<20))
	return string(data)
}

type newlineStripper struct{ r io.Reader }

func (n *newlineStripper) Read(p []byte) (int, error) {
	for {
		k, err := n.r.Read(p)
		j := 0
		for _, c := range p[:k] {
			if c != '\r' && c != '\n' {
				p[j] = c
				j++
			}
		}
		if j > 0 || err != nil {
			return j, err
		}
	}
}
