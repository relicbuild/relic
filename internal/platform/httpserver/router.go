package httpserver

import (
	"net/http"
	"slices"

	"relic/internal/access/auth"
)

// Route is a registered pattern and the access it requires.
type Route struct {
	Pattern string
	Access  auth.Access
	// Audited routes report their writes to the router's audit hook; every
	// administrator route is audited.
	Audited bool
}

// AuditHook records a write to an audited route once it is answered.
type AuditHook func(r *http.Request, route Route, status int)

// Router is an http.ServeMux on which every route must declare its access
// level.
type Router struct {
	mux    *http.ServeMux
	routes []Route
	audit  AuditHook
}

// NewRouter returns an empty router.
func NewRouter() *Router {
	return &Router{
		mux: http.NewServeMux(),
	}
}

// Handle registers h for pattern (http.ServeMux syntax) behind access.
func (rt *Router) Handle(pattern string, access auth.Access, h http.Handler) {
	rt.handle(Route{
		Pattern: pattern,
		Access:  access,
		Audited: access == auth.Admin,
	}, h)
}

// HandleAudited is Handle for a route whose writes are audited whatever its
// access level.
func (rt *Router) HandleAudited(pattern string, access auth.Access, h http.Handler) {
	rt.handle(Route{
		Pattern: pattern,
		Access:  access,
		Audited: true,
	}, h)
}

func (rt *Router) handle(route Route, h http.Handler) {
	rt.routes = append(rt.routes, route)

	h = auth.Require(route.Access, h)
	if route.Audited {
		h = rt.audited(route, h)
	}

	rt.mux.Handle(route.Pattern, routed(h))
}

// SetAuditHook makes the router report writes to audited routes to hook.
func (rt *Router) SetAuditHook(hook AuditHook) { rt.audit = hook }

func (rt *Router) audited(route Route, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rt.audit == nil || r.Method == http.MethodGet || r.Method == http.MethodHead ||
			r.Method == http.MethodOptions {
			h.ServeHTTP(w, r)
			return
		}

		rec := &statusRecorder{
			ResponseWriter: w,
		}

		defer func() {
			if p := recover(); p != nil {
				rt.audit(r, route, http.StatusInternalServerError)
				panic(p)
			}
		}()

		h.ServeHTTP(rec, r)
		rt.audit(r, route, rec.code())
	})
}

// HandleFunc is Handle for a function.
func (rt *Router) HandleFunc(pattern string, access auth.Access, h http.HandlerFunc) {
	rt.Handle(pattern, access, h)
}

// Routes returns the registered routes in registration order.
func (rt *Router) Routes() []Route { return slices.Clone(rt.routes) }

// ServeHTTP implements http.Handler.
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) { rt.mux.ServeHTTP(w, r) }
