package claudeweb

import (
	"errors"
	"testing"

	"google.golang.org/adk/v2/kernel"
)

func TestSettledIsDecidedByAnAnswerNotByANetworkError(t *testing.T) {
	old, oldW := convSettledFn, settleRetryWait
	defer func() { convSettledFn, settleRetryWait = old, oldW }()
	settleRetryWait = 0
	convSettledFn = func(string) (bool, kernel.ConvTail, error) { return false, kernel.ConvTail{}, errors.New("TLS handshake timeout") }
	if !settledNow("c") {
		t.Fatal("no answer at all: go by the stream's end_turn")
	}
	convSettledFn = func(string) (bool, kernel.ConvTail, error) { return false, kernel.ConvTail{}, nil }
	if settledNow("c") {
		t.Fatal("the server said not settled: not settled")
	}
}
