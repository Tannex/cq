package zosmf

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tannex/cq/internal/zowe"
)

func sessionForServer(t *testing.T, server *httptest.Server) zowe.Session {
	t.Helper()
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	host, portText, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	return zowe.Session{
		Profile:            "test",
		Protocol:           u.Scheme,
		Host:               host,
		Port:               port,
		User:               "IBMUSER",
		Password:           "secret-password",
		RejectUnauthorized: true,
	}
}

func TestListDataSetsSendsExactBoundedRequestAndParsesPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		if r.URL.Path != "/gateway/zosmf/restfiles/ds" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("dslevel"); got != "IBMUSER.*" {
			t.Errorf("dslevel = %q", got)
		}
		if got := r.URL.Query().Get("start"); got != "IBMUSER.A" {
			t.Errorf("start = %q", got)
		}
		if got := r.Header.Get("X-IBM-Max-Items"); got != "2" {
			t.Errorf("X-IBM-Max-Items = %q, want 2", got)
		}
		if got := r.Header.Get("X-IBM-Attributes"); got != "base" {
			t.Errorf("X-IBM-Attributes = %q", got)
		}
		if _, ok := r.Header[http.CanonicalHeaderKey("X-CSRF-ZOSMF-HEADER")]; !ok {
			t.Error("missing X-CSRF-ZOSMF-HEADER")
		}
		if got := r.Header.Get("X-IBM-Migrated-Recall"); got != "error" {
			t.Errorf("X-IBM-Migrated-Recall = %q", got)
		}
		user, password, ok := r.BasicAuth()
		if !ok || user != "IBMUSER" || password != "secret-password" {
			t.Errorf("basic auth = %q/%q/%v", user, password, ok)
		}
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, `{
			"items":[
				{"dsname":"IBMUSER.A","dsorg":"PS"},
				{"dsname":"IBMUSER.B","dsorg":"PO","recfm":"FB","lrecl":80,"vol":"VOL001","rdate":"2026/07/18"},
				{"dsname":"IBMUSER.C","dsorg":"VS"}
			],
			"returnedRows":"3","totalRows":"9","moreRows":"true","JSONversion":"1"
		}`)
	}))
	defer server.Close()
	session := sessionForServer(t, server)
	session.BasePath = "/gateway/"
	client := New(session, nil)

	page, err := client.ListDataSets(context.Background(), ListDataSetsRequest{
		Prefix: " ibmuser. ", Start: "ibmuser.a", MaxItems: 2,
	})
	if err != nil {
		t.Fatalf("ListDataSets() error = %v", err)
	}
	if page.ReturnedRows != 3 || len(page.Items) != 2 {
		t.Fatalf("returned rows = %d, items = %d", page.ReturnedRows, len(page.Items))
	}
	if page.Items[0].Name != "IBMUSER.B" || page.Items[1].Name != "IBMUSER.C" {
		t.Fatalf("items = %#v", page.Items)
	}
	if page.Items[0].Organization != "PO" || page.Items[0].RecordLength != "80" || page.Items[0].ReferenceDate != "2026/07/18" {
		t.Fatalf("typed data set = %#v", page.Items[0])
	}
	if page.TotalRows == nil || *page.TotalRows != 9 || !page.MoreRows || page.JSONVersion != 1 {
		t.Fatalf("metadata = %#v", page)
	}
}

func TestListDataSetsPreservesWildcardsAndInfersMoreRows(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/zosmf/restfiles/ds" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("dslevel"); got != "IBMUSER.%" {
			t.Errorf("dslevel = %q", got)
		}
		if got := r.Header.Get("X-IBM-Max-Items"); got != "2" {
			t.Errorf("X-IBM-Max-Items = %q, want 2", got)
		}
		_, _ = io.WriteString(w, `{"items":[{"dsname":"IBMUSER.A"},{"dsname":"IBMUSER.B"}],"returnedRows":2}`)
	}))
	defer server.Close()
	session := sessionForServer(t, server)
	session.BasePath = "/"
	client := New(session, nil)

	page, err := client.ListDataSets(context.Background(), ListDataSetsRequest{Prefix: "ibmuser.%", MaxItems: 2})
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 || !page.MoreRows {
		t.Fatalf("requests = %d, page = %#v", requests.Load(), page)
	}
}

func TestListMembersSendsStartAndPatternAndDeduplicatesBoundary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gateway/zosmf/restfiles/ds/HQ.PDS/member" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("start"); got != "MEM1" {
			t.Errorf("start = %q", got)
		}
		if got := r.URL.Query().Get("pattern"); got != "M*" {
			t.Errorf("pattern = %q", got)
		}
		if got := r.Header.Get("X-IBM-Max-Items"); got != "4" {
			t.Errorf("X-IBM-Max-Items = %q, want 4", got)
		}
		if got := r.Header.Get("X-IBM-Attributes"); got != "base" {
			t.Errorf("X-IBM-Attributes = %q", got)
		}
		if got := r.Header.Get("Cookie"); got != "LtpaToken2=token-value" {
			t.Errorf("Cookie = %q", got)
		}
		_, _ = io.WriteString(w, `{
			"items":[
				{"member":"MEM1"},
				{"member":"MEM2","vers":"2","mod":3,"c4date":"2026/01/02","m4date":"2026/07/18","cnorc":"12","inorc":10,"mnorc":2,"mtime":"11:22","msec":33,"user":"IBMUSER","sclm":"N"},
				{"member":"MEM3"},
				{"member":"MEM4"}
			],
			"returnedRows":4,"totalRows":7,"moreRows":false,"JSONversion":1
		}`)
	}))
	defer server.Close()
	session := sessionForServer(t, server)
	session.BasePath = "gateway"
	session.TokenType = "LtpaToken2"
	session.TokenValue = "token-value"
	client := New(session, nil)

	page, err := client.ListMembers(context.Background(), ListMembersRequest{
		DataSet: "hq.pds", Start: "mem1", Pattern: "m*", MaxItems: 4,
	})
	if err != nil {
		t.Fatalf("ListMembers() error = %v", err)
	}
	if len(page.Items) != 3 || page.Items[0].Name != "MEM2" || page.Items[2].Name != "MEM4" {
		t.Fatalf("items = %#v", page.Items)
	}
	member := page.Items[0]
	if member.Version != 2 || member.Modification != 3 || member.CurrentRecords != 12 || member.ModifiedSeconds != "33" || member.User != "IBMUSER" {
		t.Fatalf("typed member = %#v", member)
	}
	if page.ReturnedRows != 4 || page.TotalRows == nil || *page.TotalRows != 7 || page.MoreRows {
		t.Fatalf("page = %#v", page)
	}
}

func TestInclusiveCursorNoProgressIsTyped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"items":[{"member":"MEM1"}],"returnedRows":1,"moreRows":true}`)
	}))
	defer server.Close()
	client := New(sessionForServer(t, server), nil)

	_, err := client.ListMembers(context.Background(), ListMembersRequest{DataSet: "HQ.PDS", Start: "MEM1", MaxItems: 1})
	if !errors.Is(err, ErrNoProgress) {
		t.Fatalf("error = %v, want ErrNoProgress", err)
	}
	var progressErr *NoProgressError
	if !errors.As(err, &progressErr) || progressErr.Start != "MEM1" {
		t.Fatalf("error = %#v", err)
	}
}

func TestListAndRecordMethodsAcceptNoContent(t *testing.T) {
	tests := []struct {
		name string
		call func(*Client) (int, error)
	}{
		{
			name: "data sets",
			call: func(client *Client) (int, error) {
				page, err := client.ListDataSets(context.Background(), ListDataSetsRequest{Prefix: "IBMUSER.", MaxItems: 2})
				return len(page.Items), err
			},
		},
		{
			name: "members",
			call: func(client *Client) (int, error) {
				page, err := client.ListMembers(context.Background(), ListMembersRequest{DataSet: "HQ.PDS", MaxItems: 2})
				return len(page.Items), err
			},
		},
		{
			name: "records",
			call: func(client *Client) (int, error) {
				page, err := client.ReadRecords(context.Background(), ReadRecordsRequest{DataSet: "HQ.DATA", MaxItems: 2})
				return len(page.Records), err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if test.name != "records" {
					if got := r.Header.Get("X-IBM-Max-Items"); got != "2" {
						t.Errorf("X-IBM-Max-Items = %q, want 2", got)
					}
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			count, err := test.call(New(sessionForServer(t, server), nil))
			if err != nil || count != 0 {
				t.Fatalf("count = %d, error = %v", count, err)
			}
		})
	}
}

func TestBrowserRequestsRejectUnboundedOrInvalidInputsBeforeHTTP(t *testing.T) {
	client := New(zowe.Session{}, nil)
	tests := []struct {
		name string
		call func() error
	}{
		{"empty prefix", func() error {
			_, err := client.ListDataSets(context.Background(), ListDataSetsRequest{MaxItems: 2})
			return err
		}},
		{"zero max items", func() error {
			_, err := client.ListDataSets(context.Background(), ListDataSetsRequest{Prefix: "IBMUSER.", MaxItems: 0})
			return err
		}},
		{"member-qualified data set", func() error {
			_, err := client.ListMembers(context.Background(), ListMembersRequest{DataSet: "HQ.PDS(MEM1)", MaxItems: 2})
			return err
		}},
		{"parenthesized member", func() error {
			_, err := client.ReadRecords(context.Background(), ReadRecordsRequest{DataSet: "HQ.PDS", Member: "M(EM)", MaxItems: 2})
			return err
		}},
		{"wildcard data set", func() error {
			_, err := client.ListMembers(context.Background(), ListMembersRequest{DataSet: "HQ.*", MaxItems: 2})
			return err
		}},
		{"wildcard member start", func() error {
			_, err := client.ListMembers(context.Background(), ListMembersRequest{DataSet: "HQ.PDS", Start: "M*", MaxItems: 2})
			return err
		}},
		{"negative record start", func() error {
			_, err := client.ReadRecords(context.Background(), ReadRecordsRequest{DataSet: "HQ.DATA", Start: -1, MaxItems: 2})
			return err
		}},
		{"overflowing record range", func() error {
			_, err := client.ReadRecords(context.Background(), ReadRecordsRequest{DataSet: "HQ.DATA", Start: math.MaxInt64, MaxItems: 2})
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.call()
			var requestErr *RequestError
			if !errors.As(err, &requestErr) {
				t.Fatalf("error = %v, want RequestError", err)
			}
		})
	}
}

func TestBrowserAcceptsLargeExactVisibleRowBudgets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-IBM-Max-Items"); got != "12000" {
			t.Errorf("X-IBM-Max-Items = %q, want 12000", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := New(sessionForServer(t, server), nil)

	if _, err := client.ListDataSets(context.Background(), ListDataSetsRequest{Prefix: "IBMUSER.", MaxItems: 12000}); err != nil {
		t.Fatalf("large exact row budget was rejected: %v", err)
	}
}

func TestListResponsesAreSizeBoundedAndMalformedJSONIsSafe(t *testing.T) {
	t.Run("size limit", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"items":[]}`)
			_, _ = io.WriteString(w, strings.Repeat(" ", maxListResponseBody))
		}))
		defer server.Close()
		client := New(sessionForServer(t, server), nil)
		_, err := client.ListDataSets(context.Background(), ListDataSetsRequest{Prefix: "IBMUSER.", MaxItems: 2})
		var limitErr *LimitError
		if !errors.As(err, &limitErr) {
			t.Fatalf("error = %v, want LimitError", err)
		}
	})

	for _, body := range []string{
		`{"items":`,
		`{"items":{},"returnedRows":1}`,
		`{"items":[],"returnedRows":"many"}`,
		`{"items":[{"dsname":""}],"returnedRows":1}`,
	} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()
			client := New(sessionForServer(t, server), nil)
			_, err := client.ListDataSets(context.Background(), ListDataSetsRequest{Prefix: "IBMUSER.", MaxItems: 2})
			var protocolErr *ProtocolError
			if !errors.As(err, &protocolErr) {
				t.Fatalf("error = %v, want ProtocolError", err)
			}
		})
	}
}

func TestListDataSetsTrimsServerOverReturn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"items":[{"dsname":"A"},{"dsname":"B"},{"dsname":"C"}],"returnedRows":3,"moreRows":false}`)
	}))
	defer server.Close()
	client := New(sessionForServer(t, server), nil)

	page, err := client.ListDataSets(context.Background(), ListDataSetsRequest{Prefix: "A", MaxItems: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || !page.MoreRows {
		t.Fatalf("page = %#v", page)
	}
}

func TestReadRecordsSendsExactRangeAndParsesFraming(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gateway/zosmf/restfiles/ds/HQ.PDS(MEM1)" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("X-IBM-Data-Type"); got != "record" {
			t.Errorf("X-IBM-Data-Type = %q", got)
		}
		if got := r.Header.Get("X-IBM-Record-Range"); got != "100,3" {
			t.Errorf("X-IBM-Record-Range = %q", got)
		}
		w.WriteHeader(http.StatusPartialContent)
		writeRecords(t, w, []byte{}, []byte("ABC"), []byte("DE"))
	}))
	defer server.Close()
	session := sessionForServer(t, server)
	session.BasePath = "/gateway"
	client := New(session, nil)

	page, err := client.ReadRecords(context.Background(), ReadRecordsRequest{
		DataSet: "hq.pds", Member: "mem1", Start: 100, MaxItems: 3,
	})
	if err != nil {
		t.Fatalf("ReadRecords() error = %v", err)
	}
	if page.Start != 100 || page.ReturnedRows != 3 || !page.MoreRows {
		t.Fatalf("page = %#v", page)
	}
	if page.Records[0].Number != 101 || len(page.Records[0].Data) != 0 || page.Records[1].Number != 102 || string(page.Records[1].Data) != "ABC" || page.Records[2].Number != 103 {
		t.Fatalf("records = %#v", page.Records)
	}
}

func TestReadRecordsStopsAtRequestedCountWithoutFallback(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		writeRecords(t, w, []byte("A"), []byte("B"), []byte("C"))
	}))
	defer server.Close()
	client := New(sessionForServer(t, server), nil)

	page, err := client.ReadRecords(context.Background(), ReadRecordsRequest{DataSet: "HQ.DATA", MaxItems: 2})
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 || len(page.Records) != 2 || string(page.Records[1].Data) != "B" {
		t.Fatalf("requests = %d, page = %#v", requests.Load(), page)
	}
}

func TestReadRecordsHTTPFailureNeverFallsBackToBinary(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if got := r.Header.Get("X-IBM-Data-Type"); got != "record" {
			t.Errorf("unexpected fallback data type %q", got)
		}
		http.Error(w, "record mode unavailable", http.StatusBadRequest)
	}))
	defer server.Close()
	client := New(sessionForServer(t, server), nil)

	_, err := client.ReadRecords(context.Background(), ReadRecordsRequest{DataSet: "HQ.DATA", MaxItems: 2})
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || requests.Load() != 1 {
		t.Fatalf("error = %v, requests = %d", err, requests.Load())
	}
}

func TestReadRecordsRejectsOversizedAndTruncatedFrames(t *testing.T) {
	tests := []struct {
		name string
		body func(http.ResponseWriter)
		want any
	}{
		{
			name: "oversized record",
			body: func(w http.ResponseWriter) {
				_ = binary.Write(w, binary.BigEndian, uint32(maxRecordSize+1))
			},
			want: new(*LimitError),
		},
		{
			name: "truncated header",
			body: func(w http.ResponseWriter) {
				_, _ = w.Write([]byte{0, 0})
			},
			want: new(*ProtocolError),
		},
		{
			name: "truncated record",
			body: func(w http.ResponseWriter) {
				_ = binary.Write(w, binary.BigEndian, uint32(5))
				_, _ = io.WriteString(w, "AB")
			},
			want: new(*ProtocolError),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				test.body(w)
			}))
			defer server.Close()
			client := New(sessionForServer(t, server), nil)
			_, err := client.ReadRecords(context.Background(), ReadRecordsRequest{DataSet: "HQ.DATA", MaxItems: 2})
			switch test.want.(type) {
			case **LimitError:
				var target *LimitError
				if !errors.As(err, &target) {
					t.Fatalf("error = %v, want LimitError", err)
				}
			case **ProtocolError:
				var target *ProtocolError
				if !errors.As(err, &target) {
					t.Fatalf("error = %v, want ProtocolError", err)
				}
			}
		})
	}
}

func TestBrowserRequestCancellationPropagates(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	client := New(sessionForServer(t, server), nil)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := client.ListDataSets(ctx, ListDataSetsRequest{Prefix: "IBMUSER.", MaxItems: 2})
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request did not stop after cancellation")
	}
}

func TestRecordBodyCancellationPropagates(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = binary.Write(w, binary.BigEndian, uint32(5))
		_, _ = io.WriteString(w, "A")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	client := New(sessionForServer(t, server), nil)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := client.ReadRecords(ctx, ReadRecordsRequest{DataSet: "HQ.DATA", MaxItems: 2})
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("record response did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("record read did not stop after cancellation")
	}
}

func TestHTTPErrorParsesNestedMockShapeAndRedactsCredentials(t *testing.T) {
	basicCredential := base64.StdEncoding.EncodeToString([]byte("IBMUSER:secret-password"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":"IZUR110E","message":"token-value secret-password `+basicCredential+` not found"}}`)
	}))
	defer server.Close()
	session := sessionForServer(t, server)
	session.TokenValue = "token-value"
	client := New(session, nil)

	_, err := client.ListMembers(context.Background(), ListMembersRequest{DataSet: "HQ.MISSING", MaxItems: 2})
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error = %v, want HTTPError", err)
	}
	text := err.Error()
	if httpErr.StatusCode != http.StatusNotFound || httpErr.Code != "IZUR110E" || !strings.Contains(text, "[redacted]") {
		t.Fatalf("error = %#v (%s)", httpErr, text)
	}
	if strings.Contains(text, "token-value") || strings.Contains(text, "secret-password") || strings.Contains(text, basicCredential) {
		t.Fatalf("credentials leaked in error: %s", text)
	}
}

func TestFetchTextPreservesLargeCopybookCompatibility(t *testing.T) {
	const size = (8 << 20) + 1
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("X", size))
	}))
	defer server.Close()
	client := New(sessionForServer(t, server), nil)

	body, err := client.FetchText(context.Background(), "HQ.COPYLIB(LARGE)")
	if err != nil {
		t.Fatalf("FetchText() rejected a copybook size accepted by cq before extraction: %v", err)
	}
	if len(body) != size {
		t.Fatalf("FetchText() bytes = %d, want %d", len(body), size)
	}
}

func TestErrorBodyIsBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, strings.Repeat("x", maxErrorBody+100))
	}))
	defer server.Close()
	client := New(sessionForServer(t, server), nil)

	_, err := client.ListDataSets(context.Background(), ListDataSetsRequest{Prefix: "IBMUSER.", MaxItems: 2})
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || !httpErr.Truncated {
		t.Fatalf("error = %#v", err)
	}
	if len(err.Error()) > maxErrorBody+200 {
		t.Fatalf("error text is unexpectedly large: %d", len(err.Error()))
	}
}

func TestCanceledLazyBrowserRequestDoesNotLoadSession(t *testing.T) {
	var loads atomic.Int32
	lazy := NewLazy(func() (zowe.Session, error) {
		loads.Add(1)
		return zowe.Session{}, nil
	}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := lazy.ListDataSets(ctx, ListDataSetsRequest{Prefix: "IBMUSER.", MaxItems: 2})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if loads.Load() != 0 {
		t.Fatalf("loads = %d, want 0", loads.Load())
	}
}

func TestLazyClientExposesBoundedBrowserMethods(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	var loads atomic.Int32
	lazy := NewLazy(func() (zowe.Session, error) {
		loads.Add(1)
		return sessionForServer(t, server), nil
	}, nil)

	if _, err := lazy.ListDataSets(context.Background(), ListDataSetsRequest{Prefix: "IBMUSER.", MaxItems: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := lazy.ReadRecords(context.Background(), ReadRecordsRequest{DataSet: "HQ.DATA", MaxItems: 2}); err != nil {
		t.Fatal(err)
	}
	if loads.Load() != 1 {
		t.Fatalf("loads = %d, want 1", loads.Load())
	}
}

func writeRecords(t *testing.T, writer io.Writer, records ...[]byte) {
	t.Helper()
	for _, record := range records {
		if err := binary.Write(writer, binary.BigEndian, uint32(len(record))); err != nil {
			t.Errorf("write record length: %v", err)
			return
		}
		if _, err := writer.Write(record); err != nil {
			t.Errorf("write record: %v", err)
			return
		}
	}
}

func TestRecordNumbersRemainExactAtLargeOffsets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeRecords(t, w, []byte("A"))
	}))
	defer server.Close()
	client := New(sessionForServer(t, server), nil)
	const start = int64(9_000_000_000)

	page, err := client.ReadRecords(context.Background(), ReadRecordsRequest{DataSet: "HQ.DATA", Start: start, MaxItems: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := page.Records[0].Number; got != start+1 {
		t.Fatalf("record number = %d, want %d", got, start+1)
	}
}
