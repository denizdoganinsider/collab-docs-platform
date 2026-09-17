package middleware

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
)

var jwtSecret []byte

// Context keys the handlers read back out, named here rather than spelled as
// string literals at each use site.
const UserIDKey = "user_id"
const RoleKey = "role"

const TokenTTL = 24 * time.Hour

func InitJWT(secret string) {
	jwtSecret = []byte(secret)
}

func GenerateToken(userID int64, role string) (string, error) {
	claims := jwt.MapClaims{
		"user_id": userID,
		"role":    role,
		"exp":     time.Now().Add(TokenTTL).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	return token.SignedString(jwtSecret)
}

// JWTMiddleware is the only place in the whole system that validates a user
// token. Everything behind the gateway receives X-User-ID / X-User-Role
// instead, so this is where "who are you" is decided once.
func JWTMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		authHeader := c.Request().Header.Get("Authorization")
		if authHeader == "" {
			return c.JSON(http.StatusUnauthorized, map[string]string{
				"error": "missing authorization header",
			})
		}

		tokenString := strings.TrimPrefix(authHeader, "Bearer ")
		if tokenString == authHeader {
			return c.JSON(http.StatusUnauthorized, map[string]string{
				"error": "invalid authorization format",
			})
		}

		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			// Confirm the algorithm rather than trusting the token's own header:
			// without this, the keyfunc hands the HMAC secret to whatever method
			// the token asks for. Not exploitable while everything here is HMAC,
			// but it is the footgun that arms itself the day an asymmetric key
			// appears.
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}

			return jwtSecret, nil
		})
		if err != nil || !token.Valid {
			return c.JSON(http.StatusUnauthorized, map[string]string{
				"error": "invalid or expired token",
			})
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			return c.JSON(http.StatusUnauthorized, map[string]string{
				"error": "invalid token claims",
			})
		}

		userIDFloat, ok := claims["user_id"].(float64)
		if !ok {
			return c.JSON(http.StatusUnauthorized, map[string]string{
				"error": "invalid user id in token",
			})
		}

		role, _ := claims["role"].(string)

		c.Set(UserIDKey, int64(userIDFloat))
		c.Set(RoleKey, role)

		return next(c)
	}
}
