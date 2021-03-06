package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/ARGOeu/argo-messaging/auth"
	"github.com/ARGOeu/argo-messaging/brokers"
	"github.com/ARGOeu/argo-messaging/config"
	"github.com/ARGOeu/argo-messaging/projects"
	oldPush "github.com/ARGOeu/argo-messaging/push"
	push "github.com/ARGOeu/argo-messaging/push/grpc/client"
	"github.com/ARGOeu/argo-messaging/stores"
	"github.com/ARGOeu/argo-messaging/validation"
	"github.com/ARGOeu/argo-messaging/version"
	gorillaContext "github.com/gorilla/context"
	"github.com/gorilla/mux"
	log "github.com/sirupsen/logrus"
	"net/http"
	"sort"
	"time"
)

// WrapValidate handles validation
func WrapValidate(hfn http.HandlerFunc) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		urlVars := mux.Vars(r)

		// sort keys
		keys := []string(nil)
		for key := range urlVars {
			keys = append(keys, key)
		}
		sort.Strings(keys)

		// Iterate alphabetically
		for _, key := range keys {
			if validation.ValidName(urlVars[key]) == false {
				err := APIErrorInvalidName(key)
				respondErr(w, err)
				return
			}
		}
		hfn.ServeHTTP(w, r)

	})
}

// WrapMockAuthConfig handle wrapper is used in tests were some auth context is needed
func WrapMockAuthConfig(hfn http.HandlerFunc, cfg *config.APICfg, brk brokers.Broker, str stores.Store, mgr *oldPush.Manager, c push.Client, roles ...string) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		urlVars := mux.Vars(r)

		userRoles := []string{"publisher", "consumer"}
		if len(roles) > 0 {
			userRoles = roles
		}

		nStr := str.Clone()
		defer nStr.Close()

		projectUUID := projects.GetUUIDByName(urlVars["project"], nStr)
		gorillaContext.Set(r, "auth_project_uuid", projectUUID)
		gorillaContext.Set(r, "brk", brk)
		gorillaContext.Set(r, "str", nStr)
		gorillaContext.Set(r, "mgr", mgr)
		gorillaContext.Set(r, "apsc", c)
		gorillaContext.Set(r, "auth_resource", cfg.ResAuth)
		gorillaContext.Set(r, "auth_user", "UserA")
		gorillaContext.Set(r, "auth_user_uuid", "uuid1")
		gorillaContext.Set(r, "auth_roles", userRoles)
		gorillaContext.Set(r, "push_worker_token", cfg.PushWorkerToken)
		gorillaContext.Set(r, "push_enabled", cfg.PushEnabled)
		hfn.ServeHTTP(w, r)

	})
}

// WrapConfig handle wrapper to retrieve kafka configuration
func WrapConfig(hfn http.HandlerFunc, cfg *config.APICfg, brk brokers.Broker, str stores.Store, mgr *oldPush.Manager, c push.Client) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		nStr := str.Clone()
		defer nStr.Close()
		gorillaContext.Set(r, "brk", brk)
		gorillaContext.Set(r, "str", nStr)
		gorillaContext.Set(r, "mgr", mgr)
		gorillaContext.Set(r, "apsc", c)
		gorillaContext.Set(r, "auth_resource", cfg.ResAuth)
		gorillaContext.Set(r, "auth_service_token", cfg.ServiceToken)
		gorillaContext.Set(r, "push_worker_token", cfg.PushWorkerToken)
		gorillaContext.Set(r, "push_enabled", cfg.PushEnabled)
		hfn.ServeHTTP(w, r)

	})
}

// WrapLog handle wrapper to apply Logging
func WrapLog(hfn http.Handler, name string) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		start := time.Now()

		hfn.ServeHTTP(w, r)

		log.WithFields(
			log.Fields{
				"type":            "request_log",
				"method":          r.Method,
				"path":            r.RequestURI,
				"action":          name,
				"requester":       gorillaContext.Get(r, "auth_user_uuid"),
				"processing_time": time.Since(start).String(),
			},
		).Info("")
	})
}

// WrapAuthenticate handle wrapper to apply authentication
func WrapAuthenticate(hfn http.Handler) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		urlVars := mux.Vars(r)
		urlValues := r.URL.Query()

		// if the url parameter 'key' is empty or absent, end the request with an unauthorized response
		if urlValues.Get("key") == "" {
			err := APIErrorUnauthorized()
			respondErr(w, err)
			return
		}

		refStr := gorillaContext.Get(r, "str").(stores.Store)
		serviceToken := gorillaContext.Get(r, "auth_service_token").(string)

		projectName := urlVars["project"]
		projectUUID := projects.GetUUIDByName(urlVars["project"], refStr)

		// In all cases instead of project create
		if "projects:create" != mux.CurrentRoute(r).GetName() {
			// Check if given a project name the project wasn't found
			if projectName != "" && projectUUID == "" {
				apiErr := APIErrorNotFound("project")
				respondErr(w, apiErr)
				return
			}
		}

		// Check first if service token is used
		if serviceToken != "" && serviceToken == urlValues.Get("key") {
			gorillaContext.Set(r, "auth_roles", []string{"service_admin"})
			gorillaContext.Set(r, "auth_user", "")
			gorillaContext.Set(r, "auth_user_uuid", "")
			gorillaContext.Set(r, "auth_project_uuid", projectUUID)
			hfn.ServeHTTP(w, r)
			return
		}

		roles, user := auth.Authenticate(projectUUID, urlValues.Get("key"), refStr)

		if len(roles) > 0 {
			userUUID := auth.GetUUIDByName(user, refStr)
			gorillaContext.Set(r, "auth_roles", roles)
			gorillaContext.Set(r, "auth_user", user)
			gorillaContext.Set(r, "auth_user_uuid", userUUID)
			gorillaContext.Set(r, "auth_project_uuid", projectUUID)
			hfn.ServeHTTP(w, r)
		} else {
			err := APIErrorUnauthorized()
			respondErr(w, err)
		}

	})
}

// WrapAuthorize handle wrapper to apply authorization
func WrapAuthorize(hfn http.Handler, routeName string) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		urlValues := r.URL.Query()

		refStr := gorillaContext.Get(r, "str").(stores.Store)
		refRoles := gorillaContext.Get(r, "auth_roles").([]string)
		serviceToken := gorillaContext.Get(r, "auth_service_token").(string)

		// Check first if service token is used
		if serviceToken != "" && serviceToken == urlValues.Get("key") {
			hfn.ServeHTTP(w, r)
			return
		}

		if auth.Authorize(routeName, refRoles, refStr) {
			hfn.ServeHTTP(w, r)
		} else {
			err := APIErrorForbidden()
			respondErr(w, err)
		}
	})
}

// HealthCheck returns an ok message to make sure the service is up and running
func HealthCheck(w http.ResponseWriter, r *http.Request) {

	var err error
	var bytes []byte

	apsc := gorillaContext.Get(r, "apsc").(push.Client)

	// Add content type header to the response
	contentType := "application/json"
	charset := "utf-8"
	w.Header().Add("Content-Type", fmt.Sprintf("%s; charset=%s", contentType, charset))

	healthMsg := HealthStatus{
		Status: "ok",
	}

	detailedStatus := false

	pwToken := gorillaContext.Get(r, "push_worker_token").(string)
	pushEnabled := gorillaContext.Get(r, "push_enabled").(bool)
	refStr := gorillaContext.Get(r, "str").(stores.Store)

	// check for the right roles when accessing the details part of the api call
	if r.URL.Query().Get("details") == "true" {

		user, _ := auth.GetUserByToken(r.URL.Query().Get("key"), refStr)

		// if the user has a name, the token is valid
		if user.Name == "" {
			respondErr(w, APIErrorForbidden())
			return
		}

		if !auth.IsAdminViewer(user.ServiceRoles) && !auth.IsServiceAdmin(user.ServiceRoles) {
			respondErr(w, APIErrorUnauthorized())
			return
		}

		// set uuid for logging
		gorillaContext.Set(r, "auth_user_uuid", user.UUID)

		detailedStatus = true
	}

	if pushEnabled {
		_, err := auth.GetPushWorker(pwToken, refStr)
		if err != nil {
			healthMsg.Status = "warning"
		}

		healthMsg.PushServers = []PushServerInfo{
			{
				Endpoint: apsc.Target(),
				Status:   apsc.HealthCheck(context.TODO()).Result(detailedStatus),
			},
		}

	} else {
		healthMsg.PushFunctionality = "disabled"
	}

	if bytes, err = json.MarshalIndent(healthMsg, "", " "); err != nil {
		err := APIErrGenericInternal(err.Error())
		respondErr(w, err)
		return
	}

	respondOK(w, bytes)
}

// ListVersion displays version information about the service
func ListVersion(w http.ResponseWriter, r *http.Request) {

	// Add content type header to the response
	contentType := "application/json"
	charset := "utf-8"
	w.Header().Add("Content-Type", fmt.Sprintf("%s; charset=%s", contentType, charset))

	v := version.Model{
		Release:   version.Release,
		Commit:    version.Commit,
		BuildTime: version.BuildTime,
		GO:        version.GO,
		Compiler:  version.Compiler,
		OS:        version.OS,
		Arch:      version.Arch,
	}

	output, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		err := APIErrGenericInternal(err.Error())
		respondErr(w, err)
		return
	}

	respondOK(w, output)
}

// respondOK is used to finalize response writer with proper code and output
func respondOK(w http.ResponseWriter, output []byte) {
	w.WriteHeader(http.StatusOK)
	w.Write(output)
}

// respondErr is used to finalize response writer with proper error codes and error output
func respondErr(w http.ResponseWriter, apiErr APIErrorRoot) {
	log.Error(apiErr.Body.Code, "\t", apiErr.Body.Message)
	// set the response code
	w.WriteHeader(apiErr.Body.Code)
	// Output API Erorr object to JSON
	output, _ := json.MarshalIndent(apiErr, "", "   ")
	w.Write(output)
}

type HealthStatus struct {
	Status            string           `json:"status,omitempty"`
	PushServers       []PushServerInfo `json:"push_servers,omitempty"`
	PushFunctionality string           `json:"push_functionality,omitempty"`
}

type PushServerInfo struct {
	Endpoint string `json:"endpoint"`
	Status   string `json:"status"`
}
