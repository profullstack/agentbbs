// Package ui is the shared TUI theme for AgentBBS: one palette and a small set
// of structural widgets (cards, menu rows, status badges, key bars) so every
// screen — the hub and each plugin — looks like part of the same product.
//
// Screens keep their own accent color for identity (the hub is green, the
// arcade amber, the newsreader purple) by constructing a Theme with that
// accent; the layout primitives are shared.
package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Palette — the only colors any screen should reach for.
const (
	Green  = lipgloss.Color("#4ade80")
	Cyan   = lipgloss.Color("#38bdf8")
	Blue   = lipgloss.Color("#60a5fa")
	Gold   = lipgloss.Color("#fbbf24")
	Purple = lipgloss.Color("#c084fc")
	Red    = lipgloss.Color("#f87171")

	white = lipgloss.Color("#e2e8f0")
	text  = lipgloss.Color("252")
	muted = lipgloss.Color("245")
	faint = lipgloss.Color("240")
)

// Structural styles shared by every screen.
var (
	// Frame is the outer padding every top-level View should wrap itself in.
	Frame = lipgloss.NewStyle().Padding(1, 2)
	// Dim is for secondary text (descriptions, metadata).
	Dim = lipgloss.NewStyle().Foreground(muted)
	// Body is primary readable text.
	Body = lipgloss.NewStyle().Foreground(text)
	// Danger is for errors and warnings.
	Danger = lipgloss.NewStyle().Foreground(Red)

	selStyle = lipgloss.NewStyle().Bold(true).Foreground(white)
	hintText = lipgloss.NewStyle().Foreground(faint)
	keyText  = lipgloss.NewStyle().Bold(true).Foreground(muted)
)

// Theme carries one screen's accent color and renders the shared widgets in it.
type Theme struct{ Accent lipgloss.Color }

// New returns a theme that tints titles, sections, card borders, cursors, and
// selected rows with accent (use a palette color).
func New(accent lipgloss.Color) Theme { return Theme{Accent: accent} }

func (t Theme) accentStyle() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(t.Accent)
}

// Title renders the screen's main heading.
func (t Theme) Title(s string) string { return t.accentStyle().Render(s) }

// Section renders an upper-cased sub-heading inside a screen.
func (t Theme) Section(s string) string { return t.accentStyle().Render(strings.ToUpper(s)) }

// Card frames body in a rounded border tinted with the accent. A non-empty
// title is rendered as a section header at the top of the card.
func (t Theme) Card(title, body string) string {
	if title != "" {
		body = t.Section(title) + "\n\n" + body
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Accent).
		Padding(1, 2).
		Render(body)
}

// Row renders one selectable menu line: an accent cursor and bold label when
// selected, with a dimmed description on the next line. An empty desc yields a
// single-line row. The returned string ends in a newline.
func (t Theme) Row(selected bool, label, desc string) string {
	cur := "  "
	if selected {
		cur = t.accentStyle().Render("❯ ")
		label = selStyle.Render(label)
	}
	row := cur + label + "\n"
	if desc != "" {
		row += "  " + Dim.Render(desc) + "\n"
	}
	return row
}

// MenuItem renders one polished menu line shared by the hub and the plugin
// menus (PRD §4.1): an accent cursor and bold label when selected, an optional
// status badge after the label, and — to keep long menus uncluttered — the
// description shown only for the focused row. The result ends in a newline.
func (t Theme) MenuItem(selected bool, label, badge, desc string) string {
	name := Body.Render(label)
	cur := "  "
	if selected {
		name = selStyle.Render(label)
		cur = t.accentStyle().Render("❯ ")
	}
	if badge != "" {
		name += "  " + badge
	}
	out := cur + name + "\n"
	if selected && desc != "" {
		out += "    " + Dim.Render(desc) + "\n"
	}
	return out
}

// Badge variants.
const (
	BadgeOK    = "ok"
	BadgeInfo  = "info"
	BadgeGold  = "gold"
	BadgeWarn  = "warn"
	BadgeMuted = "muted"
)

// Badge renders a small filled status tag, e.g. Badge(BadgeOK, "guests welcome").
func Badge(variant, label string) string {
	var fg, bg lipgloss.Color
	switch variant {
	case BadgeOK:
		fg, bg = lipgloss.Color("#052e16"), Green
	case BadgeInfo:
		fg, bg = lipgloss.Color("#082f49"), Cyan
	case BadgeGold:
		fg, bg = lipgloss.Color("#451a03"), Gold
	case BadgeWarn:
		fg, bg = lipgloss.Color("#450a0a"), Red
	default:
		fg, bg = lipgloss.Color("#0b1020"), muted
	}
	return lipgloss.NewStyle().Bold(true).Foreground(fg).Background(bg).Padding(0, 1).Render(label)
}

// KeyBar renders a footer hint, emphasizing the key token of each segment. It
// accepts the conventional " · "-separated form ("↑/↓ move · enter select ·
// q quit") so call sites read naturally; the first word of each segment is
// brightened as the key.
func KeyBar(s string) string {
	segs := strings.Split(s, "·")
	for i, seg := range segs {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		if parts := strings.SplitN(seg, " ", 2); len(parts) == 2 {
			segs[i] = keyText.Render(parts[0]) + hintText.Render(" "+parts[1])
		} else {
			segs[i] = keyText.Render(seg)
		}
	}
	return strings.Join(segs, hintText.Render("  ·  "))
}
