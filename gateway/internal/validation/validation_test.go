package validation

import (
	"strings"
	"testing"
)

func TestValidateEmail(t *testing.T) {
	tests := []struct {
		name    string
		email   string
		wantErr string
	}{
		{"valid email", "user@example.com", ""},
		{"empty email", "", "invalid email format"},
		{"missing @", "userexample.com", "invalid email format"},
		{"missing domain", "user@", "invalid email format"},
		{"too long", "a" + strings.Repeat("b", 255) + "@example.com", "email must be at most 255 characters"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateEmail(tt.email)
			checkErr(t, err, tt.wantErr)
		})
	}
}

func TestValidatePassword(t *testing.T) {
	tests := []struct {
		name     string
		password string
		wantErr  string
	}{
		{"valid password", "Passw0rd1", ""},
		{"valid complex", "MyP@ssw0rd!", ""},
		{"exactly 8", "Abcdefg1", ""},
		{"too short", "Pass1", "password must be at least 8 characters"},
		{"too long", strings.Repeat("A", 128) + "1", "password must be at most 128 characters"},
		{"no uppercase", "password1", "password must contain at least one uppercase letter"},
		{"no digit", "Passwordd", "password must contain at least one digit"},
		{"empty", "", "password must be at least 8 characters"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePassword(tt.password)
			checkErr(t, err, tt.wantErr)
		})
	}
}

func checkErr(t *testing.T, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Errorf("got error %q, want nil", err)
		}
		return
	}
	if err == nil {
		t.Errorf("got nil, want error %q", want)
		return
	}
	if err.Error() != want {
		t.Errorf("got error %q, want %q", err.Error(), want)
	}
}
