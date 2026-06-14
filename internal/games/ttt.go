package games

import "strconv"

// TTT is 3×3 tic-tac-toe. Player 0 is X, player 1 is O. Moves are cell indices
// "0".."8" in row-major order.
type TTT struct{}

func (TTT) ID() string    { return "ttt" }
func (TTT) Title() string { return "Tic-Tac-Toe" }
func (TTT) Start() State {
	return tttState{cells: [9]int{-1, -1, -1, -1, -1, -1, -1, -1, -1}, toMove: 0}
}

type tttState struct {
	cells  [9]int // -1 empty, else player 0/1
	toMove int
}

var tttLines = [8][3]int{
	{0, 1, 2}, {3, 4, 5}, {6, 7, 8}, // rows
	{0, 3, 6}, {1, 4, 7}, {2, 5, 8}, // cols
	{0, 4, 8}, {2, 4, 6}, // diagonals
}

func (s tttState) ToMove() int { return s.toMove }

func (s tttState) Legal() []string {
	if over, _ := s.Terminal(); over {
		return nil
	}
	var out []string
	for i, c := range s.cells {
		if c == -1 {
			out = append(out, strconv.Itoa(i))
		}
	}
	return out
}

func (s tttState) Apply(move string) (State, error) {
	if over, _ := s.Terminal(); over {
		return nil, ErrIllegalMove
	}
	i, err := strconv.Atoi(move)
	if err != nil || i < 0 || i > 8 || s.cells[i] != -1 {
		return nil, ErrIllegalMove
	}
	ns := s
	ns.cells[i] = s.toMove
	ns.toMove = 1 - s.toMove
	return ns, nil
}

func (s tttState) Terminal() (bool, int) {
	for _, ln := range tttLines {
		a := s.cells[ln[0]]
		if a != -1 && a == s.cells[ln[1]] && a == s.cells[ln[2]] {
			return true, a
		}
	}
	for _, c := range s.cells {
		if c == -1 {
			return false, 0 // moves remain
		}
	}
	return true, Draw
}

var tttGlyph = map[int]string{-1: ".", 0: "X", 1: "O"}

func (s tttState) Observe() map[string]any {
	board := make([]string, 9)
	for i, c := range s.cells {
		board[i] = tttGlyph[c]
	}
	return map[string]any{
		"board":  board, // row-major, "." / "X" / "O"
		"toMove": s.toMove,
		"legal":  s.Legal(),
	}
}

func (s tttState) Render() string {
	out := ""
	for r := 0; r < 3; r++ {
		for c := 0; c < 3; c++ {
			out += " " + tttGlyph[s.cells[r*3+c]] + " "
			if c < 2 {
				out += "|"
			}
		}
		if r < 2 {
			out += "\n-----------\n"
		}
	}
	return out
}
