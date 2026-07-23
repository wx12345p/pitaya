package game

import (
	"math/rand"
)

// RollDice 掷一颗骰子, 返回 1..6
func RollDice() int {
	return rand.Intn(6) + 1
}
