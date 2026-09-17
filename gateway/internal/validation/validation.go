package validation

import (
	"errors"
	"net/mail"
	"unicode"
)

func ValidateEmail(email string) error {
	if len(email) > 255 {
		return errors.New("email must be at most 255 characters")
	}

	if _, err := mail.ParseAddress(email); err != nil {
		return errors.New("invalid email format")
	}

	return nil
}

// ValidatePassword enforces the README policy: at least 8 characters, one
// uppercase letter and one digit. The 128 upper bound keeps bcrypt input
// bounded (bcrypt silently truncates at 72 bytes; refusing is more honest).
func ValidatePassword(password string) error {
	if len(password) < 8 {
		return errors.New("password must be at least 8 characters")
	}

	if len(password) > 128 {
		return errors.New("password must be at most 128 characters")
	}

	var hasUpper, hasDigit bool
	for _, r := range password {
		switch {
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsDigit(r):
			hasDigit = true
		}
	}

	if !hasUpper {
		return errors.New("password must contain at least one uppercase letter")
	}
	if !hasDigit {
		return errors.New("password must contain at least one digit")
	}

	return nil
}
