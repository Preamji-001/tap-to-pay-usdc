package money

import (
	"errors"
	"math/big"
	"strings"
	"testing"
)

func TestParseDecimalValid(t *testing.T) {
	tests := map[string]int64{
		"12.50":     12500000,
		"1":         1000000,
		"0.000001":  1,
		"0012.50":   12500000,
		"999999999": 999999999000000,
	}
	for in, want := range tests {
		got, err := ParseDecimal(in, 6)
		if err != nil {
			t.Errorf("ParseDecimal(%q): %v", in, err)
			continue
		}
		if got.Cmp(big.NewInt(want)) != 0 {
			t.Errorf("ParseDecimal(%q) = %s, want %d", in, got, want)
		}
	}
}

func TestParseDecimalInvalid(t *testing.T) {
	inputs := []string{
		"", "0", "0.000000", "-1", "+1", "1e3", " 12.5", "12.5 ", "12.", ".5",
		"0.0000001", "1,5", "１２", "0x10", strings.Repeat("9", 100),
	}
	for _, in := range inputs {
		got, err := ParseDecimal(in, 6)
		if !errors.Is(err, ErrInvalidAmount) {
			t.Errorf("ParseDecimal(%q) = %v, %v; want ErrInvalidAmount", in, got, err)
		}
	}
}

func TestFormatBaseUnits(t *testing.T) {
	tests := map[int64]string{
		12500000: "12.5",
		1:        "0.000001",
		1000000:  "1",
		0:        "0",
	}
	for in, want := range tests {
		if got := FormatBaseUnits(big.NewInt(in), 6); got != want {
			t.Errorf("FormatBaseUnits(%d) = %q, want %q", in, got, want)
		}
	}
}
