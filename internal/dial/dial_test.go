package dial

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLaunchAndState(t *testing.T) {
	var gotBody, gotOrigin, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/apps/YouTube":
			b, _ := io.ReadAll(r.Body)
			gotBody, gotOrigin, gotCT = string(b), r.Header.Get("Origin"), r.Header.Get("Content-Type")
			w.WriteHeader(201)
		case r.Method == http.MethodGet && r.URL.Path == "/apps/YouTube":
			if r.Header.Get("Origin") == "" {
				w.WriteHeader(403) // LG does this
				return
			}
			_, _ = w.Write([]byte(`<?xml version="1.0"?><service><name>YouTube</name><state>running</state></service>`))
		case r.Method == http.MethodPost && r.URL.Path == "/apps/Nope":
			w.WriteHeader(404)
		default:
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	apps := srv.URL + "/apps"
	ctx := context.Background()
	origin := map[string]string{"Origin": "https://www.youtube.com"}

	if err := Launch(ctx, apps, "YouTube", "pairingCode=abc", origin); err != nil {
		t.Fatal(err)
	}
	if gotBody != "pairingCode=abc" || gotOrigin != "https://www.youtube.com" || gotCT != "text/plain; charset=utf-8" {
		t.Errorf("launch sent body=%q origin=%q ct=%q", gotBody, gotOrigin, gotCT)
	}
	if err := Launch(ctx, apps, "Nope", "", nil); err == nil || err.Error() != "the TV wouldn't start Nope (HTTP 404)" {
		t.Errorf("refused launch: %v", err)
	}
	if st, err := State(ctx, apps, "YouTube", origin); err != nil || st != "running" {
		t.Errorf("state = %q, %v", st, err)
	}
	if _, err := State(ctx, apps, "YouTube", nil); err == nil {
		t.Error("a 403 should be an error")
	}
}

func TestAppsFromDescription(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Header().Set("Application-URL", "http://192.168.1.20:36866/apps/")
		}
		_, _ = w.Write([]byte("<root/>"))
	}))
	defer srv.Close()
	apps, err := appsFrom(context.Background(), srv.URL+"/")
	if err != nil || apps != "http://192.168.1.20:36866/apps" {
		t.Errorf("apps = %q, %v", apps, err)
	}
	if _, err := appsFrom(context.Background(), srv.URL+"/none"); err == nil {
		t.Error("a description without Application-URL should be an error")
	}
}
