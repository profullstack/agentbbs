package games

import (
	"strconv"
	"strings"
)

// Connect4 is 7-column × 6-row Connect Four. Player 0 is X, player 1 is O.
// Moves are column indices "0".."6"; a piece falls to the lowest empty row.
type Connect4 struct{}

func (Connect4) ID() string    { return "c4" }
func (Connect4) Title() string { return "Connect 4" }
func (Connect4) Start() State  { return c4State{} }

const (
	c4Cols = 7
	c4Rows = 6
)

// c4State stores cells row-major with row 0 at the TOP. -1 empty, else 0/1.
type c4State struct {
	cells  [c4Rows * c4Cols]int
	filled int // number of pieces placed (for draw detection)
	toMove int
	zero   bool // marks an initialized empty board (cells default to 0, not -1)
}

func (s c4State) at(r, c int) int {
	if !s.zero {
		return -1 // fresh board: all empty
	}
	return s.cells[r*c4Cols+c]
}

func (s c4State) ToMove() int { return s.toMove }

func (s c4State) Legal() []string {
	if over, _ := s.Terminal(); over {
		return nil
	}
	var out []string
	for c := 0; c < c4Cols; c++ {
		if s.at(0, c) == -1 {
			out = append(out, strconv.Itoa(c))
		}
	}
	return out
}

func (s c4State) Apply(move string) (State, error) {
	if over, _ := s.Terminal(); over {
		return nil, ErrIllegalMove
	}
	c, err := strconv.Atoi(move)
	if err != nil || c < 0 || c >= c4Cols || s.at(0, c) != -1 {
		return nil, ErrIllegalMove
	}
	ns := s.materialize()
	// Drop to the lowest empty row.
	row := c4Rows - 1
	for row >= 0 && ns.cells[row*c4Cols+c] != -1 {
		row--
	}
	ns.cells[row*c4Cols+c] = s.toMove
	ns.filled = s.filled + 1
	ns.toMove = 1 - s.toMove
	return ns, nil
}

// materialize returns a copy whose backing array is explicitly filled with -1
// for empties, so Apply can write into it.
func (s c4State) materialize() c4State {
	if s.zero {
		return s
	}
	ns := s
	for i := range ns.cells {
		ns.cells[i] = -1
	}
	ns.zero = true
	return ns
}

var c4Dirs = [4][2]int{{0, 1}, {1, 0}, {1, 1}, {1, -1}} // →, ↓, ↘, ↙

func (s c4State) Terminal() (bool, int) {
	for r := 0; r < c4Rows; r++ {
		for c := 0; c < c4Cols; c++ {
			p := s.at(r, c)
			if p == -1 {
				continue
			}
			for _, d := range c4Dirs {
				if s.countRun(r, c, d[0], d[1], p) >= 4 {
					return true, p
				}
			}
		}
	}
	if s.filled >= c4Rows*c4Cols {
		return true, Draw
	}
	return false, 0
}

func (s c4State) countRun(r, c, dr, dc, p int) int {
	n := 0
	for r >= 0 && r < c4Rows && c >= 0 && c < c4Cols && s.at(r, c) == p {
		n++
		r += dr
		c += dc
	}
	return n
}

var c4Glyph = map[int]string{-1: ".", 0: "X", 1: "O"}

func (s c4State) Observe() map[string]any {
	board := make([][]string, c4Rows)
	for r := 0; r < c4Rows; r++ {
		board[r] = make([]string, c4Cols)
		for c := 0; c < c4Cols; c++ {
			board[r][c] = c4Glyph[s.at(r, c)]
		}
	}
	return map[string]any{
		"board":  board, // [row][col], row 0 is the top
		"toMove": s.toMove,
		"legal":  s.Legal(),
	}
}

func (s c4State) Render() string {
	var b strings.Builder
	for r := 0; r < c4Rows; r++ {
		for c := 0; c < c4Cols; c++ {
			b.WriteString(" " + c4Glyph[s.at(r, c)])
		}
		b.WriteString("\n")
	}
	b.WriteString(" 0 1 2 3 4 5 6")
	return b.String()
}
