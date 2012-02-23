package nntpserver

import (
	"math"
	"testing"
)

type rangeExpectation struct {
	input string
	low   int64
	high  int64
}

var rangeExpectations = []rangeExpectation{
	rangeExpectation{"", 0, math.MaxInt64},
	rangeExpectation{"73-", 73, math.MaxInt64},
	rangeExpectation{"73-1845", 73, 1845},
}

func TestRangeEmpty(t *testing.T) {
	for _, e := range rangeExpectations {
		l, h := parseRange(e.input)
		if l != e.low {
			t.Fatalf("Error parsing %q, got low=%v, wanted %v",
				e.input, l, e.low)
		}
		if h != e.high {
			t.Fatalf("Error parsing %q, got high=%v, wanted %v",
				e.input, h, e.high)
		}
	}
}
