package middleware

import "github.com/labstack/echo/v4"

// InstanceHeaderName names the process that answered, so "which instance
// served this" is visible from outside: the balancer verification reads it,
// and a stuck document can be traced to one port.
const InstanceHeaderName = "X-Doc-Instance"

// InstanceHeader stamps every HTTP response. The WebSocket 101 is written by
// the upgrader on the hijacked connection and bypasses this; package ws
// passes the same header to Upgrade.
func InstanceHeader(instanceID string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Response().Header().Set(InstanceHeaderName, instanceID)
			return next(c)
		}
	}
}
