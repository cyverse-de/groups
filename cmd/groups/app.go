package main

import (
	"context"
	"fmt"
	"net/http"

	_ "github.com/cyverse-de/groups/docs"
	"github.com/cyverse-de/groups/eventing"
	"github.com/cyverse-de/groups/keycloak"
	"github.com/cyverse-de/groups/permissions"
	"github.com/knadh/koanf"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	echoSwagger "github.com/swaggo/echo-swagger"
)

// version is the reported service version.
const version = "0.1.0"

// App ties together the HTTP API and the clients that back it.
type App struct {
	config      *koanf.Koanf
	router      *echo.Echo
	keycloak    keycloak.Client
	permissions permissions.Client
	events      eventing.Publisher
	adminUsers  map[string]struct{}
}

//	@title			groups
//	@version		0.1.0
//	@description	Group management API for the CyVerse Discovery Environment, backed by Keycloak.
//	@BasePath		/
//
// NewApp constructs the application, wires up its clients, and registers routes.
func NewApp(config *koanf.Koanf) (*App, error) {
	kc, err := keycloakClientFromConfig(config)
	if err != nil {
		return nil, err
	}

	events, err := eventingFromConfig(config)
	if err != nil {
		return nil, err
	}

	app := &App{
		config:      config,
		router:      echo.New(),
		keycloak:    kc,
		permissions: permissions.NewClient(permissionsBaseURL(config)),
		events:      events,
		adminUsers:  adminUsersFromConfig(config),
	}

	app.ensureResourceType()
	app.registerRoutes()

	return app, nil
}

// eventingFromConfig builds the change-event publisher. When amqp.uri is not
// configured, eventing is disabled and a no-op publisher is returned.
func eventingFromConfig(config *koanf.Koanf) (eventing.Publisher, error) {
	uri := config.String("amqp.uri")
	if uri == "" {
		log.WithField("context", "startup").Info("AMQP eventing disabled: amqp.uri is not configured")
		return eventing.NoopPublisher{}, nil
	}

	exchange := config.String("amqp.exchange")
	if exchange == "" {
		exchange = "de"
	}
	return eventing.NewAMQPPublisher(uri, exchange)
}

// publishGroupChanged publishes a change event for a group. Failures are logged
// but not returned: the group operation has already succeeded, and the event is
// only a reindex hint.
func (a *App) publishGroupChanged(c echo.Context, groupID string) {
	if err := a.events.GroupChanged(c.Request().Context(), groupID); err != nil {
		log.WithField("context", "eventing").
			Warnf("could not publish a change event for group %s: %s", groupID, err)
	}
}

// permissionsBaseURL returns the configured permissions service URL, defaulting
// to the in-cluster service name.
func permissionsBaseURL(config *koanf.Koanf) string {
	if base := config.String("permissions.base"); base != "" {
		return base
	}
	return "http://permissions"
}

// adminUsersFromConfig builds the set of trusted service accounts (admin.users)
// that bypass per-group permission checks.
func adminUsersFromConfig(config *koanf.Koanf) map[string]struct{} {
	users := config.Strings("admin.users")
	admins := make(map[string]struct{}, len(users))
	for _, user := range users {
		admins[user] = struct{}{}
	}
	return admins
}

// ensureResourceType registers the "group" resource type with the permissions
// service. Failure is logged but not fatal: the permissions service may not be
// reachable at startup, and subsequent grants will surface a clear error.
func (a *App) ensureResourceType() {
	err := a.permissions.EnsureResourceType(context.Background(), resourceTypeGroup,
		"A CyVerse Discovery Environment group managed in Keycloak.")
	if err != nil {
		log.WithField("context", "startup").
			Warnf("could not register the %q resource type with the permissions service: %s", resourceTypeGroup, err)
	}
}

// registerRoutes wires up middleware, the error handler, and all HTTP routes.
func (a *App) registerRoutes() {
	a.router.Use(middleware.Recover())
	a.router.HTTPErrorHandler = a.errorHandler

	a.router.GET("/", a.StatusHandler).Name = "status"
	a.router.GET("/docs/*", echoSwagger.WrapHandler)

	groups := a.router.Group("/groups")
	groups.Use(requireUser)
	groups.GET("", a.SearchGroupsHandler)
	groups.POST("", a.AddGroupHandler)
	groups.GET("/:id", a.GetGroupHandler)
	groups.PUT("/:id", a.UpdateGroupHandler)
	groups.DELETE("/:id", a.DeleteGroupHandler)
	groups.GET("/:id/members", a.GetMembersHandler)
	groups.PUT("/:id/members", a.ReplaceMembersHandler)
	groups.POST("/:id/members", a.AddMembersHandler)
	groups.POST("/:id/members/deleter", a.RemoveMembersHandler)
	groups.PUT("/:id/members/:subject", a.AddMemberHandler)
	groups.DELETE("/:id/members/:subject", a.RemoveMemberHandler)
	groups.GET("/:id/permissions", a.ListPermissionsHandler)
	groups.PUT("/:id/permissions/:subject-type/:subject-id", a.GrantPermissionHandler)
	groups.DELETE("/:id/permissions/:subject-type/:subject-id", a.RevokePermissionHandler)

	subjects := a.router.Group("/subjects")
	subjects.Use(requireUser)
	subjects.GET("", a.SearchSubjectsHandler)
	subjects.POST("/lookup", a.LookupSubjectsHandler)
	subjects.GET("/:subject-id", a.GetSubjectHandler)
	subjects.GET("/:subject-id/groups", a.SubjectGroupsHandler)
}

// Router returns the underlying echo router so it can be served.
func (a *App) Router() *echo.Echo {
	return a.router
}

// keycloakClientFromConfig builds a Keycloak client from configuration,
// validating that the required settings are present.
func keycloakClientFromConfig(config *koanf.Koanf) (keycloak.Client, error) {
	cfg := keycloak.Config{
		BaseURL:      config.String("keycloak.base-url"),
		Realm:        config.String("keycloak.realm"),
		ClientID:     config.String("keycloak.client-id"),
		ClientSecret: config.String("keycloak.client-secret"),
		ParentGroup:  config.String("keycloak.parent-group"),
	}

	switch {
	case cfg.BaseURL == "":
		return nil, fmt.Errorf("keycloak.base-url must be set in the configuration")
	case cfg.Realm == "":
		return nil, fmt.Errorf("keycloak.realm must be set in the configuration")
	case cfg.ClientID == "":
		return nil, fmt.Errorf("keycloak.client-id must be set in the configuration")
	case cfg.ClientSecret == "":
		return nil, fmt.Errorf("keycloak.client-secret must be set in the configuration")
	}

	return keycloak.NewClient(cfg), nil
}

// errorHandler renders errors as a consistent JSON body.
func (a *App) errorHandler(err error, c echo.Context) {
	if c.Response().Committed {
		return
	}

	code := http.StatusInternalServerError
	message := err.Error()
	if he, ok := err.(*echo.HTTPError); ok {
		code = he.Code
		message = fmt.Sprintf("%v", he.Message)
	}

	if jsonErr := c.JSON(code, map[string]string{"error": message}); jsonErr != nil {
		c.Logger().Error(jsonErr)
	}
}
