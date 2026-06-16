package main

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// StatusResponse describes the service-information payload returned by GET /.
type StatusResponse struct {
	Service  string `json:"service"`
	Version  string `json:"version"`
	Keycloak bool   `json:"keycloak"`
}

// StatusHandler reports basic service information and backend connectivity, and
// serves as the liveness/readiness probe target.
//
//	@Summary	Service information
//	@Description	Returns the service name, version, and Keycloak connectivity.
//	@Produce	json
//	@Success	200	{object}	StatusResponse
//	@Router	/ [get]
func (a *App) StatusHandler(c echo.Context) error {
	keycloakOK := a.keycloak.Ping(c.Request().Context()) == nil

	return c.JSON(http.StatusOK, &StatusResponse{
		Service:  serviceName,
		Version:  version,
		Keycloak: keycloakOK,
	})
}
