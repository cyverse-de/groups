package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	_ "github.com/cyverse-de/groups/docs"
	"github.com/cyverse-de/groups/eventing"
	"github.com/cyverse-de/groups/permissions"
	"github.com/cyverse-de/groups/store"
	"github.com/cyverse-de/groups/store/pgstore"
	"github.com/cyverse-de/groups/userinfo"
	"github.com/knadh/koanf"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/sirupsen/logrus"
	echoSwagger "github.com/swaggo/echo-swagger"
)

// version is the reported service version.
const version = "0.1.0"

// App ties together the HTTP API and the clients that back it.
type App struct {
	config      *koanf.Koanf
	router      *echo.Echo
	store       store.Store
	userinfo    userinfo.Client
	permissions permissions.Client
	events      eventing.Publisher
	adminUsers  map[string]struct{}
}

//	@title			groups
//	@version		0.1.0
//	@description	Group management API for the CyVerse Discovery Environment. Groups live in the permissions schema of the DE database; user attributes come from Keycloak.
//	@BasePath		/
//
// NewApp constructs the application, wires up its clients, and registers routes.
func NewApp(ctx context.Context, config *koanf.Koanf) (*App, error) {
	groupStore, err := storeFromConfig(ctx, config)
	if err != nil {
		return nil, err
	}

	users, err := userinfoFromConfig(config)
	if err != nil {
		closeStore(groupStore)
		return nil, err
	}

	app := &App{
		config:      config,
		router:      echo.New(),
		store:       groupStore,
		userinfo:    users,
		permissions: permissions.NewClient(permissionsBaseURL(config)),
		events:      eventingFromConfig(config),
		adminUsers:  adminUsersFromConfig(config),
	}

	app.ensureResourceType()
	app.registerRoutes()

	return app, nil
}

// Close releases the application's resources.
func (a *App) Close() error {
	var errs []error
	if a.events != nil {
		if err := a.events.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if a.store != nil {
		if err := a.store.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// closeStore releases the store during a failed startup, where the original
// error is the one worth returning.
func closeStore(s store.Store) {
	if err := s.Close(); err != nil {
		log.WithField("context", "startup").
			Warnf("could not close the group store while aborting startup: %s", err)
	}
}

// storeFromConfig opens the group store, validating that the database is
// configured. A missing or unreachable database is fatal: unlike Keycloak or
// AMQP, nothing the service does works without it.
func storeFromConfig(ctx context.Context, config *koanf.Koanf) (store.Store, error) {
	uri := config.String("db.uri")
	if uri == "" {
		return nil, fmt.Errorf("db.uri must be set in the configuration")
	}

	cfg, err := poolFromConfig(config)
	if err != nil {
		return nil, err
	}
	cfg.URI = uri
	cfg.Schema = config.String("db.schema")
	cfg.UserSuffix = config.String("users.suffix")

	return pgstore.Open(ctx, cfg)
}

// poolFromConfig reads the optional connection-pool settings, leaving each one
// zero when it is absent so the store applies its own default. A setting that is
// present has to be usable: silently falling back to the default would run the
// pool at a size nobody asked for.
func poolFromConfig(config *koanf.Koanf) (pgstore.Config, error) {
	var cfg pgstore.Config
	var err error

	if cfg.MaxOpenConns, err = positiveInt(config, "db.max-open-conns"); err != nil {
		return cfg, err
	}
	if cfg.MaxIdleConns, err = positiveInt(config, "db.max-idle-conns"); err != nil {
		return cfg, err
	}

	if config.Exists("db.conn-max-lifetime") {
		lifetime := config.Duration("db.conn-max-lifetime")
		if lifetime <= 0 {
			return cfg, fmt.Errorf("db.conn-max-lifetime must be a positive duration, such as 5m")
		}
		cfg.ConnMaxLifetime = lifetime
	}

	maxOpen := cfg.MaxOpenConns
	if maxOpen == 0 {
		maxOpen = pgstore.DefaultMaxOpenConns
	}
	if cfg.MaxIdleConns > maxOpen {
		return cfg, fmt.Errorf("db.max-idle-conns (%d) cannot exceed db.max-open-conns (%d)",
			cfg.MaxIdleConns, maxOpen)
	}
	return cfg, nil
}

// positiveInt reads an integer setting, returning 0 when it is absent and an
// error when it is present but unusable.
func positiveInt(config *koanf.Koanf, key string) (int, error) {
	if !config.Exists(key) {
		return 0, nil
	}
	v := config.Int(key)
	if v < 1 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return v, nil
}

// eventingFromConfig builds the change-event publisher. When amqp.uri is not
// configured, eventing is disabled and a no-op publisher is returned.
//
// A broker that cannot be reached is not fatal, matching how a failed publish is
// treated: events are a reindex hint, and refusing to start would take group
// management down with the broker.
func eventingFromConfig(config *koanf.Koanf) eventing.Publisher {
	uri := config.String("amqp.uri")
	if uri == "" {
		log.WithField("context", "startup").Info("AMQP eventing disabled: amqp.uri is not configured")
		return eventing.NoopPublisher{}
	}

	exchange := config.String("amqp.exchange")
	if exchange == "" {
		exchange = "de"
	}

	publisher, err := eventing.NewAMQPPublisher(uri, exchange)
	if err != nil {
		log.WithField("context", "startup").
			Errorf("could not connect to the AMQP broker, so change events will not be published "+
				"until the service is restarted; downstream search indexes will go stale "+
				"(check amqp.uri and that the broker is reachable): %s", err)
		return eventing.NoopPublisher{}
	}
	return publisher
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
		"A CyVerse Discovery Environment group.")
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
	a.router.GET("/healthz", a.HealthHandler).Name = "health"
	a.router.GET("/docs/*", echoSwagger.WrapHandler)

	groups := a.router.Group("/groups")
	groups.Use(requireUser)
	groups.GET("", a.ListGroupsHandler)
	groups.POST("", a.AddGroupHandler)
	// Registered before /:id so the literal path wins over the parameter.
	groups.GET("/lookup", a.LookupGroupHandler)
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
	subjects.GET("/:subject-id/permissions", a.SubjectPermissionsHandler)
}

// Router returns the underlying echo router so it can be served.
func (a *App) Router() *echo.Echo {
	return a.router
}

// userinfoFromConfig builds the user-attribute client from configuration,
// validating that the required settings are present. Keycloak no longer stores
// groups; it is only the source of user names, emails, and institutions.
func userinfoFromConfig(config *koanf.Koanf) (userinfo.Client, error) {
	cfg := userinfo.Config{
		BaseURL:      config.String("keycloak.base-url"),
		Realm:        config.String("keycloak.realm"),
		ClientID:     config.String("keycloak.client-id"),
		ClientSecret: config.String("keycloak.client-secret"),
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

	return userinfo.NewKeycloakClient(cfg), nil
}

// errorHandler renders errors as a consistent JSON body. Anything that is not
// an *echo.HTTPError may quote a downstream response body or hostname, so it is
// logged for operators and replaced with a generic message.
func (a *App) errorHandler(err error, c echo.Context) {
	if c.Response().Committed {
		return
	}

	code := http.StatusInternalServerError
	message := "internal error"
	var he *echo.HTTPError
	if errors.As(err, &he) {
		code = he.Code
		message = fmt.Sprintf("%v", he.Message)
	} else {
		log.WithFields(logrus.Fields{
			"method": c.Request().Method,
			"path":   c.Request().URL.Path,
		}).Errorf("unhandled error: %s", err)
	}

	if jsonErr := c.JSON(code, map[string]string{"error": message}); jsonErr != nil {
		c.Logger().Error(jsonErr)
	}
}
