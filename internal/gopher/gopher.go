// Package gopher serves AgentBBS content over the Gopher protocol (RFC 1436)
// and its SSH-authenticated sibling, "hedgehog".
//
// # Two surfaces, one engine
//
// Every AgentBBS protocol service exposes a native port for external clients
// plus a zero-install `ssh <name>@` TUI for members (see internal/news). Gopher
// follows the same shape — but with a twist forced by the protocol itself:
//
//   - Classic Gopher (RFC 1436) is a stateless, unauthenticated, one-shot TCP
//     protocol on port 70: the client sends a single selector line and reads one
//     response. There is no auth verb (no NNTP-style AUTHINFO), so SSH-key auth
//     cannot be bolted onto a port-70 gopher server without breaking every
//     gopher client. The public listener (Serve) therefore serves *public*
//     content only.
//
//   - "hedgehog" is the same gopher wire semantics carried over the SSH channel
//     (RunBrowser), with the member's SSH key as the credential. Because the
//     session is already authenticated, hedgehog additionally resolves
//     members-only selectors (e.g. private newsgroups). It is the home-grown,
//     gopher-like, SSH-authenticated surface — gopher where gopher can, our own
//     thing where it can't.
//
// Both surfaces share one Resolve engine (server.go); the only difference is the
// authed flag passed in. The engine is read-only: nothing is ever written to the
// BBS over gopher.
package gopher

import "strings"

// Gopher item types (RFC 1436 §3.8). Only the handful the BBS emits are named.
const (
	TypeText   = '0' // a plain text file
	TypeMenu   = '1' // a Gopher directory (submenu)
	TypeError  = '3' // an error / "does not exist" line
	TypeBinary = '9' // a generic binary file
	TypeHTML   = 'h' // an HTML file / URL: link
	TypeImage  = 'I' // an image
	TypeInfo   = 'i' // an informational line (not selectable)
)

// An Item is one gopher directory row. Info rows (TypeInfo) are display-only;
// every other type is a selectable link the client may follow to Selector on
// Host:Port. Keeping menus as structured Items (rather than pre-rendered text)
// lets the hedgehog browser TUI navigate them while the native listener renders
// the exact same rows to the wire.
type Item struct {
	Type     byte
	Display  string
	Selector string
	Host     string
	Port     string
}

// A Menu accumulates gopher directory rows and renders them in the wire format:
// each row is "<type><display>\t<selector>\t<host>\t<port>\r\n" and the whole
// menu is terminated by a lone "." line (the RFC 1436 last-line dot).
type Menu struct {
	host  string
	port  string
	Items []Item
}

// NewMenu starts a menu whose links advertise host:port as the server to dial
// for each selector (mirrored back to clients so link-following works).
func NewMenu(host, port string) *Menu {
	return &Menu{host: host, port: port}
}

// Info appends one or more informational lines (type 'i'), split on newlines so
// callers can pass a multi-line block.
func (m *Menu) Info(text string) *Menu {
	for _, line := range strings.Split(text, "\n") {
		m.Items = append(m.Items, Item{Type: TypeInfo, Display: line, Host: "error.host", Port: "1"})
	}
	return m
}

// Link appends a selectable row of the given item type pointing at selector on
// this server.
func (m *Menu) Link(itemType byte, display, selector string) *Menu {
	m.Items = append(m.Items, Item{Type: itemType, Display: display, Selector: selector, Host: m.host, Port: m.port})
	return m
}

// URL appends an "URL:" link (type 'h') to an external http(s) address — the
// widely-supported convention for linking off-gopher.
func (m *Menu) URL(display, url string) *Menu {
	m.Items = append(m.Items, Item{Type: TypeHTML, Display: display, Selector: "URL:" + url, Host: m.host, Port: m.port})
	return m
}

// Len reports how many rows have been added (informational + selectable).
func (m *Menu) Len() int { return len(m.Items) }

// Render returns the full menu including the terminating "." line, CRLF-framed.
func (m *Menu) Render() string {
	var b strings.Builder
	for _, it := range m.Items {
		b.WriteString(renderItem(it))
	}
	b.WriteString(".\r\n")
	return b.String()
}

// renderItem formats one gopher directory entry. Tabs and CRs in the display or
// selector would corrupt the tab-delimited framing, so they are stripped.
func renderItem(it Item) string {
	return string(it.Type) + clean(it.Display) + "\t" + clean(it.Selector) + "\t" +
		it.Host + "\t" + it.Port + "\r\n"
}

// clean removes the framing-significant bytes (tab, CR, LF) from a field.
func clean(s string) string {
	return strings.NewReplacer("\t", " ", "\r", "", "\n", " ").Replace(s)
}
