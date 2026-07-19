package zosmf

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReadTextRequestsETagAndReturnsContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		if r.URL.EscapedPath() != "/zosmf/restfiles/ds/IBMUSER.PARMLIB%28MEMBER%29" {
			t.Errorf("path = %s", r.URL.EscapedPath())
		}
		if got := r.Header.Get("X-IBM-Data-Type"); got != "text" {
			t.Errorf("X-IBM-Data-Type = %q", got)
		}
		if got := r.Header.Get("X-IBM-Return-Etag"); got != "true" {
			t.Errorf("X-IBM-Return-Etag = %q", got)
		}
		w.Header().Set("Etag", "E123")
		_, _ = io.WriteString(w, "LINE ONE\nLINE TWO\n")
	}))
	defer server.Close()
	client := New(sessionForServer(t, server), nil)

	content, err := client.ReadText(context.Background(), "IBMUSER.PARMLIB(MEMBER)")
	if err != nil {
		t.Fatalf("ReadText() error = %v", err)
	}
	if string(content.Text) != "LINE ONE\nLINE TWO\n" {
		t.Fatalf("text = %q", content.Text)
	}
	if content.ETag != "E123" {
		t.Fatalf("etag = %q", content.ETag)
	}
}

func TestWriteTextSendsConditionalPut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s", r.Method)
		}
		if r.URL.EscapedPath() != "/zosmf/restfiles/ds/IBMUSER.PARMLIB%28MEMBER%29" {
			t.Errorf("path = %s", r.URL.EscapedPath())
		}
		if got := r.Header.Get("X-IBM-Data-Type"); got != "text" {
			t.Errorf("X-IBM-Data-Type = %q", got)
		}
		if got := r.Header.Get("If-Match"); got != "E123" {
			t.Errorf("If-Match = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "NEW CONTENT\n" {
			t.Errorf("body = %q", body)
		}
		w.Header().Set("Etag", "E124")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := New(sessionForServer(t, server), nil)

	etag, err := client.WriteText(context.Background(), WriteTextRequest{
		Target: "ibmuser.parmlib(member)", Body: []byte("NEW CONTENT\n"), ETag: "E123",
	})
	if err != nil {
		t.Fatalf("WriteText() error = %v", err)
	}
	if etag != "E124" {
		t.Fatalf("etag = %q", etag)
	}
}

func TestWriteTextWithoutETagOmitsIfMatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Header[http.CanonicalHeaderKey("If-Match")]; ok {
			t.Error("If-Match sent for empty ETag")
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	client := New(sessionForServer(t, server), nil)

	if _, err := client.WriteText(context.Background(), WriteTextRequest{Target: "IBMUSER.NEW", Body: []byte("X\n")}); err != nil {
		t.Fatalf("WriteText() error = %v", err)
	}
}

func TestWriteTextConflictIsDetectable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPreconditionFailed)
		_, _ = io.WriteString(w, `{"message":"The specified ETag does not match"}`)
	}))
	defer server.Close()
	client := New(sessionForServer(t, server), nil)

	_, err := client.WriteText(context.Background(), WriteTextRequest{
		Target: "IBMUSER.PARMLIB(MEMBER)", Body: []byte("X\n"), ETag: "stale",
	})
	if err == nil {
		t.Fatal("expected conflict error")
	}
	if !IsConflict(err) {
		t.Fatalf("IsConflict(%v) = false", err)
	}
}

func TestIsConflictRejectsOtherErrors(t *testing.T) {
	if IsConflict(nil) {
		t.Fatal("nil error reported as conflict")
	}
	if IsConflict(&HTTPError{StatusCode: http.StatusInternalServerError}) {
		t.Fatal("HTTP 500 reported as conflict")
	}
}

func TestNormalizeWriteTargetValidatesForms(t *testing.T) {
	if got, err := normalizeWriteTarget(" ibmuser.parmlib(member) "); err != nil || got != "IBMUSER.PARMLIB(MEMBER)" {
		t.Fatalf("member form = %q, %v", got, err)
	}
	if got, err := normalizeWriteTarget("ibmuser.data"); err != nil || got != "IBMUSER.DATA" {
		t.Fatalf("plain form = %q, %v", got, err)
	}
	for _, invalid := range []string{"", "IBMUSER.DATA()", "IBMUSER.*", "IBMUSER.DATA(TOOLONGNAME)"} {
		if _, err := normalizeWriteTarget(invalid); err == nil {
			t.Errorf("normalizeWriteTarget(%q) accepted", invalid)
		}
	}
}
