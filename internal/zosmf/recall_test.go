package zosmf

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRecallDataSetSendsHRecallRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s", r.Method)
		}
		if r.URL.Path != "/zosmf/restfiles/ds/IBMUSER.MIGR.DATA" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		if _, ok := r.Header[http.CanonicalHeaderKey("X-CSRF-ZOSMF-HEADER")]; !ok {
			t.Error("missing X-CSRF-ZOSMF-HEADER")
		}
		body, _ := io.ReadAll(r.Body)
		if got := string(body); got != `{"request":"hrecall","wait":false}` {
			t.Errorf("body = %s", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := New(sessionForServer(t, server), nil)

	if err := client.RecallDataSet(context.Background(), " ibmuser.migr.data "); err != nil {
		t.Fatalf("RecallDataSet() error = %v", err)
	}
}

func TestRecallDataSetReportsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"category":4,"rc":8,"message":"IKJ56709I INVALID DATA SET NAME"}`)
	}))
	defer server.Close()
	client := New(sessionForServer(t, server), nil)

	err := client.RecallDataSet(context.Background(), "IBMUSER.MIGR.DATA")
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("RecallDataSet() error = %v", err)
	}
}

func TestRecallDataSetRejectsInvalidName(t *testing.T) {
	client := New(sessionForServer(t, httptest.NewServer(http.NotFoundHandler())), nil)
	err := client.RecallDataSet(context.Background(), "IBMUSER.*")
	var requestErr *RequestError
	if !errors.As(err, &requestErr) {
		t.Fatalf("RecallDataSet() error = %v, want RequestError", err)
	}
}

func TestIsMigrated(t *testing.T) {
	cases := []struct {
		name string
		item DataSet
		want bool
	}{
		{"migr flag", DataSet{Migrated: "YES"}, true},
		{"migr flag lower", DataSet{Migrated: "yes"}, true},
		{"migrat volume", DataSet{Volume: "MIGRAT"}, true},
		{"arcive volume", DataSet{Volume: "ARCIVE"}, true},
		{"primary", DataSet{Migrated: "NO", Volume: "VOL001"}, false},
		{"empty", DataSet{}, false},
	}
	for _, tc := range cases {
		if got := IsMigrated(tc.item); got != tc.want {
			t.Errorf("%s: IsMigrated() = %v, want %v", tc.name, got, tc.want)
		}
	}
}
