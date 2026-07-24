package main

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// StatusResponse describes the service-information payload returned by GET /.
type StatusResponse struct {
	Service string `json:"service"`
	Version string `json:"version"`
	// Database reports whether group storage is reachable. False means the
	// service cannot serve any group request.
	Database bool `json:"database"`
	// Keycloak reports whether user attributes are reachable. False degrades
	// names and email addresses but leaves group membership working.
	Keycloak bool `json:"keycloak"`
}

// StatusHandler reports basic service information and backend connectivity, and
// serves as the liveness/readiness probe target.
//
//	@Summary	Service information
//	@Description	Returns the service name, version, and backend connectivity.
//	@Produce	json
//	@Success	200	{object}	StatusResponse
//	@Router	/ [get]
func (a *App) StatusHandler(c echo.Context) error {
	ctx := c.Request().Context()

	return c.JSON(http.StatusOK, &StatusResponse{
		Service:  serviceName,
		Version:  version,
		Database: a.store.Ping(ctx) == nil,
		Keycloak: a.userinfo.Ping(ctx) == nil,
	})
}
