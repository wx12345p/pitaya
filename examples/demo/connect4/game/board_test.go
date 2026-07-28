package game

import "testing"

// dropAll 依次在指定列落同色子, 便于构造测试局面
func dropAll(t *testing.T, b *Board, p Piece, cols ...int) {
	t.Helper()
	for _, c := range cols {
		if _, ok := b.Drop(c, p); !ok {
			t.Fatalf("在列%d落子失败", c)
		}
	}
}

func TestDropStacksAndHeights(t *testing.T) {
	b := NewBoard()
	for i := 0; i < Rows; i++ {
		row, ok := b.Drop(3, Red)
		if !ok || row != i {
			t.Fatalf("第%d次落子: row=%d ok=%v, 期望 row=%d", i+1, row, ok, i)
		}
	}
	if !b.ColFull(3) {
		t.Fatal("列3 应已满")
	}
	if _, ok := b.Drop(3, Red); ok {
		t.Fatal("已满的列不应允许落子")
	}
	if b.MoveCount() != Rows {
		t.Fatalf("落子数=%d, 期望%d", b.MoveCount(), Rows)
	}
}

func TestWinningLineAllDirections(t *testing.T) {
	cases := []struct {
		name  string
		build func(b *Board) (col, row int)
	}{
		{"水平", func(b *Board) (int, int) {
			dropAll(t, b, Red, 0, 1, 2)
			row, _ := b.Drop(3, Red)
			return 3, row
		}},
		{"垂直", func(b *Board) (int, int) {
			dropAll(t, b, Red, 2, 2, 2)
			row, _ := b.Drop(2, Red)
			return 2, row
		}},
		{"右上斜", func(b *Board) (int, int) {
			// 构造阶梯: (0,0) (1,1) (2,2) (3,3) 均为红
			dropAll(t, b, Red, 0)
			dropAll(t, b, Yellow, 1)
			dropAll(t, b, Red, 1)
			dropAll(t, b, Yellow, 2, 2)
			dropAll(t, b, Red, 2)
			dropAll(t, b, Yellow, 3, 3, 3)
			row, _ := b.Drop(3, Red)
			return 3, row
		}},
		{"左上斜", func(b *Board) (int, int) {
			// 构造阶梯: (3,0) (2,1) (1,2) (0,3) 均为红
			dropAll(t, b, Red, 3)
			dropAll(t, b, Yellow, 2)
			dropAll(t, b, Red, 2)
			dropAll(t, b, Yellow, 1, 1)
			dropAll(t, b, Red, 1)
			dropAll(t, b, Yellow, 0, 0, 0)
			row, _ := b.Drop(0, Red)
			return 0, row
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := NewBoard()
			col, row := tc.build(b)
			line := b.WinningLine(col, row)
			if len(line) < WinLen {
				t.Fatalf("%s 方向应判定为连四, 实际连线长度=%d", tc.name, len(line))
			}
			for _, c := range line {
				if b.At(c.Col, c.Row) != Red {
					t.Fatalf("连线包含非红子: %+v", c)
				}
			}
		})
	}
}

func TestWinningLineNeedsFourInARow(t *testing.T) {
	b := NewBoard()
	dropAll(t, b, Red, 0, 1)
	dropAll(t, b, Yellow, 2)
	row, _ := b.Drop(3, Red)
	if line := b.WinningLine(3, row); line != nil {
		t.Fatalf("被对手隔断时不应判定获胜, 实际=%v", line)
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	b := NewBoard()
	dropAll(t, b, Red, 3, 4, 3)
	dropAll(t, b, Yellow, 3, 5, 0)

	got := DecodeBoard(b.Encode())
	if got.MoveCount() != b.MoveCount() {
		t.Fatalf("落子数不一致: %d vs %d", got.MoveCount(), b.MoveCount())
	}
	for col := 0; col < Cols; col++ {
		if got.Height(col) != b.Height(col) {
			t.Fatalf("列%d 高度不一致: %d vs %d", col, got.Height(col), b.Height(col))
		}
		for row := 0; row < Rows; row++ {
			if got.At(col, row) != b.At(col, row) {
				t.Fatalf("格(%d,%d) 不一致: %v vs %v", col, row, got.At(col, row), b.At(col, row))
			}
		}
	}
}

func TestFullBoardIsDraw(t *testing.T) {
	b := NewBoard()
	// 交替填满整个棋盘: 每两列一组重复 RRYY 模式, 保证不出现连四
	pattern := []Piece{Red, Red, Yellow, Yellow}
	for col := 0; col < Cols; col++ {
		for row := 0; row < Rows; row++ {
			p := pattern[(row+2*col)%len(pattern)]
			if _, ok := b.Drop(col, p); !ok {
				t.Fatalf("填充失败 col=%d row=%d", col, row)
			}
		}
	}
	if !b.Full() {
		t.Fatal("棋盘应已填满")
	}
	if len(b.LegalCols()) != 0 {
		t.Fatal("满盘时不应有合法落点")
	}
}
