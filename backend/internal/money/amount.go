// Package money converts between human decimal amounts ("12.50") and
// integer token base units (12500000 for a 6-decimal token). It never uses
// floating point, so no amount is ever rounded.
package money

import (
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
)

var ErrInvalidAmount = errors.New("invalid amount")

// maxInputLen bounds the work done on untrusted input.
const maxInputLen = 40

// ParseDecimal parses a plain positive decimal such as "12.50" into base
// units for a token with the given decimals. It rejects signs, exponents,
// whitespace, missing digits on either side of the point, more fractional
// digits than the token has, and zero.
func ParseDecimal(s string, decimals uint8) (*big.Int, error) {
	if len(s) > maxInputLen {
		return nil, fmt.Errorf("%w: longer than %d characters", ErrInvalidAmount, maxInputLen)
	}
	pattern := fmt.Sprintf(`^[0-9]+(\.[0-9]{1,%d})?$`, decimals)
	if decimals == 0 {
		pattern = `^[0-9]+$`
	}
	if !regexp.MustCompile(pattern).MatchString(s) {
		return nil, fmt.Errorf("%w: %q is not a decimal with at most %d places", ErrInvalidAmount, s, decimals)
	}

	whole, frac, _ := strings.Cut(s, ".")
	frac += strings.Repeat("0", int(decimals)-len(frac))
	v, ok := new(big.Int).SetString(whole+frac, 10)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrInvalidAmount, s)
	}
	if v.Sign() == 0 {
		return nil, fmt.Errorf("%w: must be greater than zero", ErrInvalidAmount)
	}
	return v, nil
}

// FormatBaseUnits renders base units as a decimal with trailing zeros
// trimmed: 12500000 with 6 decimals is "12.5".
func FormatBaseUnits(v *big.Int, decimals uint8) string {
	s := v.String()
	if decimals == 0 {
		return s
	}
	if pad := int(decimals) + 1 - len(s); pad > 0 {
		s = strings.Repeat("0", pad) + s
	}
	whole, frac := s[:len(s)-int(decimals)], strings.TrimRight(s[len(s)-int(decimals):], "0")
	if frac == "" {
		return whole
	}
	return whole + "." + frac
}
