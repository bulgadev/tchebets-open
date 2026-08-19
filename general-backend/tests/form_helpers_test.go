package tests

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// doFormRequest posts/gets with a real HTML form body and carries session
// cookies the same way a browser would - auth_test.go/admin_test.go need
// this since those handlers bind form data and gate on session cookies,
// unlike the JSON API tested in integration_test.go.
func doFormRequest(t *testing.T, r *gin.Engine, method, path string, form url.Values, cookies []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()

	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}

	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	for _, c := range cookies {
		req.AddCookie(c)
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// registerAndLogin registers a fresh account through the real HTTP flow and
// returns its session cookies, ready to attach to further requests.
func registerAndLogin(t *testing.T, r *gin.Engine, email, password string) []*http.Cookie {
	t.Helper()
	w := doFormRequest(t, r, http.MethodPost, "/register", url.Values{
		"email":    {email},
		"password": {password},
	}, nil)
	if w.Code != http.StatusFound {
		t.Fatalf("expected register to redirect, got %d: %s", w.Code, w.Body.String())
	}
	return w.Result().Cookies()
}
