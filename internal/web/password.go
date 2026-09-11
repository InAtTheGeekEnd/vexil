package web

import (
	"errors"

	"golang.org/x/crypto/bcrypt"
)

// MinPasswordLen is the minimum admin password length.
const MinPasswordLen = 10

// ErrPasswordShort and ErrPasswordMismatch describe invalid new passwords.
var (
	ErrPasswordShort    = errors.New("the password must have at least 10 characters")
	ErrPasswordMismatch = errors.New("the two passwords do not match")
)

// ValidateNewPassword checks a new password and its repeat.
func ValidateNewPassword(password, confirm string) error {
	if len(password) < MinPasswordLen {
		return ErrPasswordShort
	}
	if password != confirm {
		return ErrPasswordMismatch
	}
	return nil
}

// HashPassword returns a bcrypt hash of password.
func HashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// checkPassword reports whether password matches the bcrypt hash.
func checkPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
