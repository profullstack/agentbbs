package arcade

import (
	"fmt"
	"math/rand"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/profullstack/agentbbs/internal/auth"
	"github.com/profullstack/agentbbs/internal/plugin"
)

// shedsnake is "Shedding Snake": a molting twist on Snake (inspired by
// cha.rlie.co/shedding-snake). The snake never really grows — instead, every
// time it eats it SHEDS its whole body as a permanent field of scales on the
// board, then slithers on a little longer and a little faster. The board fills
// with your own cast-off skins; you die by biting a shed scale or yourself.
// Wrapping walls (toroidal) make it Snake × Tron-trails.
//
// Score is the number of molts shed. It feeds its own global leaderboard under
// the "shedsnake" game id (see arcade.go).
type shedsnake struct {
	user auth.User
	ctx  plugin.Context

	w, h int // board size in cells

	body []pos       // head is body[0]
	dir  pos         // current heading
	next pos         // buffered heading (applied on tick, prevents 180° suicide)
	food pos         // apple
	skin map[pos]int // shed scales → the molt generation that left them

	gen     int   // molts shed so far == score
	speedMS int   // tick interval; drops as you shed
	dead    bool
	saved   bool
	flash   int // countdown of frames to show the "molt!" banner
}

type shedTickMsg time.Time

func shedTick(ms int) tea.Cmd {
	return tea.Tick(time.Duration(ms)*time.Millisecond, func(t time.Time) tea.Msg { return shedTickMsg(t) })
}

const (
	shedStartLen  = 6   // starting body length
	shedStartMS   = 130 // starting tick interval
	shedMinMS     = 55  // fastest it gets
	shedSpeedStep = 6   // ms shaved off per molt
	shedGrow      = 1   // body cells gained per molt
)

func newShedSnake(user auth.User, ctx plugin.Context, termW, termH int) *shedsnake {
	w, h := 34, 18
	if termW > 0 && termW/2-4 < w {
		w = termW/2 - 4
	}
	if termH > 0 && termH-8 < h {
		h = termH - 8
	}
	if w < 12 {
		w = 12
	}
	if h < 10 {
		h = 10
	}
	s := &shedsnake{
		user: user, ctx: ctx, w: w, h: h,
		dir: pos{1, 0}, next: pos{1, 0},
		skin:    map[pos]int{},
		speedMS: shedStartMS,
	}
	// Lay the body out to the left of a centered head so the first move is safe.
	hx, hy := w/2, h/2
	for i := 0; i < shedStartLen; i++ {
		s.body = append(s.body, pos{hx - i, hy})
	}
	s.placeFood()
	return s
}

// placeFood drops the apple on an empty cell (not on the body or any scale).
func (s *shedsnake) placeFood() {
	for {
		p := pos{rand.Intn(s.w), rand.Intn(s.h)}
		if s.onBody(p) {
			continue
		}
		if _, isSkin := s.skin[p]; isSkin {
			continue
		}
		s.food = p
		return
	}
}

func (s *shedsnake) onBody(p pos) bool {
	for _, b := range s.body {
		if b == p {
			return true
		}
	}
	return false
}

// wrap keeps a coordinate on the toroidal board.
func (s *shedsnake) wrap(p pos) pos {
	if p.x < 0 {
		p.x = s.w - 1
	} else if p.x >= s.w {
		p.x = 0
	}
	if p.y < 0 {
		p.y = s.h - 1
	} else if p.y >= s.h {
		p.y = 0
	}
	return p
}

func (s *shedsnake) Init() tea.Cmd { return shedTick(s.speedMS) }

func (s *shedsnake) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc":
			return s, back
		case "up", "w":
			if s.dir.y == 0 {
				s.next = pos{0, -1}
			}
		case "down", "s":
			if s.dir.y == 0 {
				s.next = pos{0, 1}
			}
		case "left", "a":
			if s.dir.x == 0 {
				s.next = pos{-1, 0}
			}
		case "right", "d":
			if s.dir.x == 0 {
				s.next = pos{1, 0}
			}
		case "r":
			if s.dead {
				ns := newShedSnake(s.user, s.ctx, 0, 0)
				ns.w, ns.h = s.w, s.h
				return ns, ns.Init()
			}
		}
	case shedTickMsg:
		if s.dead {
			return s, nil
		}
		if s.flash > 0 {
			s.flash--
		}
		s.dir = s.next
		head := s.wrap(pos{s.body[0].x + s.dir.x, s.body[0].y + s.dir.y})

		// Death: bite yourself, or bite a shed scale you're not already sitting on.
		if s.onBody(head) {
			return s, s.die()
		}
		if _, isSkin := s.skin[head]; isSkin && !s.onBody(head) {
			return s, s.die()
		}

		ate := head == s.food
		s.body = append([]pos{head}, s.body...)
		s.body = s.body[:len(s.body)-1] // fixed-length move; molt() is the only growth
		if ate {
			s.molt() // shed the whole body as scales, grow a touch, speed up
			s.placeFood()
		}
		return s, shedTick(s.speedMS)
	}
	return s, nil
}

// molt is the heart of the game: cast the entire current body onto the board as
// a permanent field of scales, then grow a touch and quicken. The freshly-shed
// scales sit under the body and only turn lethal once the snake slithers off
// them (Update's death check skips scales still under the body).
func (s *shedsnake) molt() {
	s.gen++
	for _, b := range s.body {
		if _, exists := s.skin[b]; !exists {
			s.skin[b] = s.gen
		}
	}
	// Grow: keep the extra tail cells this turn instead of trimming.
	for i := 0; i < shedGrow; i++ {
		s.body = append(s.body, s.body[len(s.body)-1])
	}
	if s.speedMS > shedMinMS {
		s.speedMS -= shedSpeedStep
		if s.speedMS < shedMinMS {
			s.speedMS = shedMinMS
		}
	}
	s.flash = 6
}

func (s *shedsnake) die() tea.Cmd {
	s.dead = true
	// Guests play, members persist (PRD §5.1). Score is molts shed.
	if !s.saved && s.user.Kind != auth.Guest && s.user.StoreID > 0 && s.gen > 0 {
		_ = s.ctx.Store.AddScore(s.user.StoreID, "shedsnake", int64(s.gen))
		s.saved = true
	}
	return nil
}

var (
	shedHeadStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#bbf7d0")).Bold(true)
	shedBodyStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#4ade80"))
	shedFoodStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true)
	shedWallStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	shedFlashStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#facc15")).Bold(true)

	// Shed scales age from fresh teal-green to old dusty gray, so the board reads
	// as geological layers of cast-off skin. Indexed by (gen - moltGen) bucket.
	shedScaleAges = []lipgloss.Style{
		lipgloss.NewStyle().Foreground(lipgloss.Color("35")),  // freshest
		lipgloss.NewStyle().Foreground(lipgloss.Color("29")),
		lipgloss.NewStyle().Foreground(lipgloss.Color("23")),
		lipgloss.NewStyle().Foreground(lipgloss.Color("240")),
		lipgloss.NewStyle().Foreground(lipgloss.Color("237")), // oldest
	}
)

// scaleStyle picks a color for a scale based on how many molts ago it was shed.
func (s *shedsnake) scaleStyle(moltGen int) lipgloss.Style {
	age := s.gen - moltGen
	if age < 0 {
		age = 0
	}
	if age >= len(shedScaleAges) {
		age = len(shedScaleAges) - 1
	}
	return shedScaleAges[age]
}

func (s *shedsnake) View() string {
	out := fmt.Sprintf("%s  ·  molts %d  ·  length %d  ·  speed %d",
		shedHeadStyle.Render("Shedding Snake"), s.gen, len(s.body), shedStartMS-s.speedMS)
	switch {
	case s.dead:
		out += shedWallStyle.Render("   ") + "☠  shed your last (r restart · q back)"
	case s.flash > 0:
		out += "   " + shedFlashStyle.Render("✦ molt! ✦")
	}
	out += "\n" + shedWallStyle.Render("┌"+repeat("──", s.w)+"┐") + "\n"

	// Index the body once for O(1) head/body lookups per cell.
	headPos := s.body[0]
	bodySet := make(map[pos]struct{}, len(s.body))
	for _, b := range s.body {
		bodySet[b] = struct{}{}
	}

	for y := 0; y < s.h; y++ {
		row := shedWallStyle.Render("│")
		for x := 0; x < s.w; x++ {
			p := pos{x, y}
			switch {
			case p == headPos:
				row += shedHeadStyle.Render("██")
			case contains(bodySet, p):
				row += shedBodyStyle.Render("▓▓")
			case p == s.food:
				row += shedFoodStyle.Render("◆ ")
			default:
				if g, ok := s.skin[p]; ok {
					row += s.scaleStyle(g).Render("░░")
				} else {
					row += "  "
				}
			}
		}
		out += row + shedWallStyle.Render("│") + "\n"
	}
	out += shedWallStyle.Render("└"+repeat("──", s.w)+"┘") + "\n"
	out += shedWallStyle.Render("  eat to shed your skin — the board fills with scales you must not bite. walls wrap.")
	return lipgloss.NewStyle().Padding(1, 2).Render(out)
}

func contains(set map[pos]struct{}, p pos) bool {
	_, ok := set[p]
	return ok
}
