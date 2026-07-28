// Package game 实现四子棋的棋盘、AI 与房间状态机(纯 Go, 不依赖 pitaya)。
package game

// 棋盘与对局规则常量
const (
	Cols       = 7 // 列数
	Rows       = 6 // 行数
	WinLen     = 4 // 连成几子获胜
	MaxPlayers = 2 // 对局人数
)

// Piece 格子取值(与 protos.Piece 枚举同值)
type Piece int8

// 棋子颜色
const (
	Empty  Piece = 0
	Red    Piece = 1 // 座位0, 先手
	Yellow Piece = 2 // 座位1
)

// PieceOfSeat 座位对应的棋子颜色
func PieceOfSeat(seat int) Piece { return Piece(seat + 1) }

// SeatOfPiece 棋子颜色对应的座位, 空格返回 -1
func SeatOfPiece(p Piece) int {
	if p == Empty {
		return -1
	}
	return int(p) - 1
}

// Opponent 对手棋色
func Opponent(p Piece) Piece {
	if p == Red {
		return Yellow
	}
	return Red
}

// Cell 棋盘坐标; row=0 为最底行, col=0 为最左列
type Cell struct {
	Col int
	Row int
}

// Board 棋盘。cells 按 index = row*Cols + col 存放, heights 记录每列已落子数。
type Board struct {
	cells   [Cols * Rows]Piece
	heights [Cols]int
	moves   int
}

// NewBoard 创建空棋盘
func NewBoard() *Board { return &Board{} }

// At 读取指定格; 越界返回 Empty
func (b *Board) At(col, row int) Piece {
	if col < 0 || col >= Cols || row < 0 || row >= Rows {
		return Empty
	}
	return b.cells[row*Cols+col]
}

// Height 指定列已落子数(即下一颗子的落点行号)
func (b *Board) Height(col int) int { return b.heights[col] }

// ColValid 列号是否合法
func ColValid(col int) bool { return col >= 0 && col < Cols }

// ColFull 指定列是否已满
func (b *Board) ColFull(col int) bool { return b.heights[col] >= Rows }

// Full 棋盘是否已下满
func (b *Board) Full() bool { return b.moves >= Cols*Rows }

// MoveCount 已落子数
func (b *Board) MoveCount() int { return b.moves }

// Drop 在指定列落子, 返回落点行号; 列号非法或该列已满时 ok=false
func (b *Board) Drop(col int, p Piece) (row int, ok bool) {
	if !ColValid(col) || b.ColFull(col) {
		return 0, false
	}
	row = b.heights[col]
	b.cells[row*Cols+col] = p
	b.heights[col]++
	b.moves++
	return row, true
}

// Undo 撤销指定列最后一次落子(供 AI 搜索回溯)
func (b *Board) Undo(col int) {
	if !ColValid(col) || b.heights[col] == 0 {
		return
	}
	b.heights[col]--
	b.cells[b.heights[col]*Cols+col] = Empty
	b.moves--
}

// LegalCols 返回可落子的列
func (b *Board) LegalCols() []int {
	res := make([]int, 0, Cols)
	for c := 0; c < Cols; c++ {
		if !b.ColFull(c) {
			res = append(res, c)
		}
	}
	return res
}

// 四个检查方向: 水平、垂直、右上斜、左上斜
var winDirs = [4][2]int{{1, 0}, {0, 1}, {1, 1}, {1, -1}}

// WinningLine 判定以 (col,row) 为落点是否形成连线; 未成线返回 nil。
// 只检查最后落子点所在的四条线, 复杂度 O(WinLen*4)。
func (b *Board) WinningLine(col, row int) []Cell {
	p := b.At(col, row)
	if p == Empty {
		return nil
	}
	for _, d := range winDirs {
		line := []Cell{{Col: col, Row: row}}
		// 正方向延伸
		for i := 1; ; i++ {
			c, r := col+d[0]*i, row+d[1]*i
			if b.At(c, r) != p {
				break
			}
			line = append(line, Cell{Col: c, Row: r})
		}
		// 反方向延伸
		for i := 1; ; i++ {
			c, r := col-d[0]*i, row-d[1]*i
			if b.At(c, r) != p {
				break
			}
			line = append([]Cell{{Col: c, Row: r}}, line...)
		}
		if len(line) >= WinLen {
			return line
		}
	}
	return nil
}

// Encode 序列化棋盘为字节数组(index = row*Cols + col), 用于协议与快照
func (b *Board) Encode() []byte {
	buf := make([]byte, Cols*Rows)
	for i, v := range b.cells {
		buf[i] = byte(v)
	}
	return buf
}

// DecodeBoard 由字节数组还原棋盘; 长度不匹配时返回空盘
func DecodeBoard(cells []byte) *Board {
	b := NewBoard()
	if len(cells) != Cols*Rows {
		return b
	}
	for i, v := range cells {
		p := Piece(v)
		if p != Red && p != Yellow {
			continue
		}
		b.cells[i] = p
		b.heights[i%Cols]++
		b.moves++
	}
	return b
}

// Clone 复制棋盘
func (b *Board) Clone() *Board {
	nb := *b
	return &nb
}
