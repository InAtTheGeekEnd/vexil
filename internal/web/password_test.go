package web

import (
	"errors"
	"strings"
	"testing"
)

// TestNewPasswordLength checks the limits of a new password. bcrypt takes at
// most 72 bytes, so a longer password is refused with a form error, not a
// failed hash.
func TestNewPasswordLength(t *testing.T) {
	tests := []struct {
		name     string
		password string
		want     error
	}{
		{"10 characters", strings.Repeat("a", 10), nil},
		{"9 characters", strings.Repeat("a", 9), ErrPasswordShort},
		{"72 bytes", strings.Repeat("a", 72), nil},
		{"73 bytes", strings.Repeat("a", 73), ErrPasswordLong},
		{"37 accented letters are 74 bytes", strings.Repeat("é", 37), ErrPasswordLong},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateNewPassword(tt.password, tt.password)
			if !errors.Is(err, tt.want) {
				t.Fatalf("ValidateNewPassword = %v, want %v", err, tt.want)
			}
			if err == nil {
				if _, err := HashPassword(tt.password); err != nil {
					t.Fatalf("HashPassword of a valid password: %v", err)
				}
			}
		})
	}
}
