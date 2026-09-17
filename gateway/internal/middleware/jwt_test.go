package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
)

func init() {
	InitJWT("test-secret-key")
}

func runJWT(t *testing.T, authHeader string) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	e := echo.New()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	reached := false
	handler := JWTMiddleware(func(c echo.Context) error {
		reached = true
		return c.NoContent(http.StatusOK)
	})
	if err := handler(c); err != nil {
		t.Fatalf("handler error = %v", err)
	}
	return rec, reached
}

func TestGenerateToken_Claims(t *testing.T) {
	tokenString, err := GenerateToken(1, "admin")
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}

	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		return jwtSecret, nil
	})
	if err != nil || !token.Valid {
		t.Fatalf("jwt.Parse() error = %v, valid = %v", err, token != nil && token.Valid)
	}

	claims := token.Claims.(jwt.MapClaims)
	if userID, _ := claims["user_id"].(float64); int64(userID) != 1 {
		t.Errorf("user_id = %v, want 1", claims["user_id"])
	}
	if role, _ := claims["role"].(string); role != "admin" {
		t.Errorf("role = %v, want admin", claims["role"])
	}
	exp, _ := claims["exp"].(float64)
	if remaining := time.Until(time.Unix(int64(exp), 0)); remaining < 23*time.Hour || remaining > TokenTTL {
		t.Errorf("exp is %v away, want ~24h", remaining)
	}
}

func TestJWTMiddleware_ValidToken(t *testing.T) {
	e := echo.New()
	tokenString, _ := GenerateToken(42, "user")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := JWTMiddleware(func(c echo.Context) error {
		if got := c.Get(UserIDKey).(int64); got != 42 {
			t.Errorf("user_id = %d, want 42", got)
		}
		if got := c.Get(RoleKey).(string); got != "user" {
			t.Errorf("role = %q, want user", got)
		}
		return c.NoContent(http.StatusOK)
	})

	if err := handler(c); err != nil {
		t.Fatalf("handler error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestJWTMiddleware_Rejects(t *testing.T) {
	expired := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id": float64(1), "role": "user", "exp": time.Now().Add(-time.Hour).Unix(),
	})
	expiredString, _ := expired.SignedString(jwtSecret)

	wrongKey := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id": float64(1), "role": "admin", "exp": time.Now().Add(time.Hour).Unix(),
	})
	wrongKeyString, _ := wrongKey.SignedString([]byte("not-the-secret"))

	// alg=none: the header claims no signature. Must not be accepted.
	noneToken := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{
		"user_id": float64(1), "role": "admin", "exp": time.Now().Add(time.Hour).Unix(),
	})
	noneString, _ := noneToken.SignedString(jwt.UnsafeAllowNoneSignatureType)

	noUserID := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"role": "admin", "exp": time.Now().Add(time.Hour).Unix(),
	})
	noUserIDString, _ := noUserID.SignedString(jwtSecret)

	tests := []struct {
		name   string
		header string
	}{
		{"missing header", ""},
		{"not bearer", "Basic some-token"},
		{"malformed", "Bearer not-a-valid-jwt-token"},
		{"expired", "Bearer " + expiredString},
		{"wrong key", "Bearer " + wrongKeyString},
		{"alg none", "Bearer " + noneString},
		{"no user_id claim", "Bearer " + noUserIDString},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, reached := runJWT(t, tt.header)
			if reached {
				t.Error("handler was reached, want rejection")
			}
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
			}
		})
	}
}
