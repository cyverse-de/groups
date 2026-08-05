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
// serves as the liveness/readiness probe target: a failed database ping answers
// 503 so the pod leaves rotation. A failed Keycloak ping stays 200, because the
// service can serve most reads without it -- only names and emails degrade.
//
//	@Summary	Service information
//	@Description	Returns the service name, version, and backend connectivity. Responds 503 when the database is unreachable.
//	@Produce	json
//	@Success	200	{object}	StatusResponse
//	@Failure	503	{object}	StatusResponse
//	@Router	/ [get]
func (a *App) StatusHandler(c echo.Context) error {
	ctx := c.Request().Context()

	dbErr := a.store.Ping(ctx)
	if dbErr != nil {
		log.WithField("context", "status").
			Errorf("database ping failed; no group request can be served until it recovers (check db.uri and database health): %s", dbErr)
	}
	kcErr := a.userinfo.Ping(ctx)
	if kcErr != nil {
		log.WithField("context", "status").
			Warnf("keycloak ping failed; member names and emails will be degraded (check keycloak.* settings and connectivity): %s", kcErr)
	}

	code := http.StatusOK
	if dbErr != nil {
		code = http.StatusServiceUnavailable
	}
	return c.JSON(code, &StatusResponse{
		Service:  serviceName,
		Version:  version,
		Database: dbErr == nil,
		Keycloak: kcErr == nil,
	})
}
