package game

import "testing"

func TestAITakesImmediateWin(t *testing.T) {
	b := NewBoard()
	dropAll(t, b, Red, 0, 1, 2)    // 红方底行三连, 落列3 即胜
	dropAll(t, b, Yellow, 5, 6, 5) // 黄方无立即威胁

	if col := BestMove(b, Red, AIDepth); col != 3 {
		t.Fatalf("AI 应立即取胜(列3), 实际选择列%d", col)
	}
}

func TestAIBlocksImmediateLoss(t *testing.T) {
	b := NewBoard()
	dropAll(t, b, Yellow, 0, 1, 2) // 黄方底行三连, 若不封堵列3 则下一手告负
	dropAll(t, b, Red, 5, 6)       // 红方自己没有立即取胜的点

	if col := BestMove(b, Red, AIDepth); col != 3 {
		t.Fatalf("AI 应封堵列3, 实际选择列%d", col)
	}
}

func TestAIPrefersWinOverBlock(t *testing.T) {
	b := NewBoard()
	dropAll(t, b, Red, 0, 1, 2)    // 红方落列3 即胜
	dropAll(t, b, Yellow, 4, 5, 6) // 黄方落列3 也能成四

	if col := BestMove(b, Red, AIDepth); col != 3 {
		t.Fatalf("AI 应选择取胜点, 实际选择列%d", col)
	}
}

func TestBestMoveOnFullBoardReturnsMinusOne(t *testing.T) {
	b := NewBoard()
	for col := 0; col < Cols; col++ {
		for row := 0; row < Rows; row++ {
			if _, ok := b.Drop(col, Red); !ok {
				t.Fatalf("填充失败 col=%d row=%d", col, row)
			}
		}
	}
	if col := BestMove(b, Yellow, AIFastDepth); col != -1 {
		t.Fatalf("满盘应返回 -1, 实际=%d", col)
	}
}

func TestBestMoveAlwaysLegal(t *testing.T) {
	b := NewBoard()
	// 把中间三列填满, 迫使 AI 只能选择两侧
	for _, col := range []int{2, 3, 4} {
		for row := 0; row < Rows; row++ {
			p := Red
			if row%2 == 1 {
				p = Yellow
			}
			if _, ok := b.Drop(col, p); !ok {
				t.Fatalf("填充失败 col=%d", col)
			}
		}
	}
	col := BestMove(b, Red, AIDepth)
	if !ColValid(col) || b.ColFull(col) {
		t.Fatalf("AI 返回了非法落点: %d", col)
	}
}
