package main

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxZOSMFErrorBody = 16 * 1024

func elapsed(start time.Time) time.Duration {
	return time.Since(start).Round(time.Millisecond)
}

// zosmfTransport talks to z/OSMF in-process.
type zosmfTransport struct {
	session zoweSession
	client  *http.Client
}

func newZOSMFTransport(session zoweSession) *zosmfTransport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	skipCertificateVerification := session.Protocol == "https" && !session.RejectUnauthorized
	if skipCertificateVerification {
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{}
		}
		// Zowe defines rejectUnauthorized=false as an explicit opt-out from
		// server certificate verification. Keep the opt-out scoped to this
		// cloned transport so it cannot affect other HTTP clients.
		transport.TLSClientConfig.InsecureSkipVerify = true // #nosec G402 -- explicitly requested by the selected Zowe profile.
	}
	return &zosmfTransport{session: session, client: &http.Client{Transport: transport}}
}

func (z *zosmfTransport) fetchCopybook(ctx context.Context, dsn string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	start := time.Now()
	res, err := z.openRequest(ctx, dsn, "text", "")
	if err != nil {
		debugLog.Printf("z/OSMF view %s: failed after %s", dsn, elapsed(start))
		return nil, fmt.Errorf("z/OSMF data set %q: %w", dsn, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("z/OSMF data set %q: read response: %w", dsn, err)
	}
	debugLog.Printf("z/OSMF view %s: %d bytes in %s", dsn, len(body), elapsed(start))
	return body, nil
}

func (z *zosmfTransport) openDataSet(dsn string, hint downloadHint) (io.ReadCloser, error) {
	ctx, cancel := context.WithCancel(context.Background())
	if hint.Records > 0 && hint.RecordLength > 0 {
		res, err := z.openRequest(ctx, dsn, "record", fmt.Sprintf("0,%d", hint.Records))
		if err == nil {
			debugLog.Printf("z/OSMF download %s: streaming (up to %d records of %d bytes)", dsn, hint.Records, hint.RecordLength)
			return &rangedDataSetStream{
				transport: z, context: ctx, cancel: cancel, body: res.Body,
				dsn: dsn, recordLength: hint.RecordLength,
			}, nil
		}
		debugLog.Printf("z/OSMF download %s: ranged request failed (%v); retrying without record range", dsn, err)
	}

	res, err := z.openRequest(ctx, dsn, "binary", "")
	if err != nil {
		cancel()
		return nil, fmt.Errorf("z/OSMF data set %q: %w", dsn, err)
	}
	debugLog.Printf("z/OSMF download %s: streaming", dsn)
	return &cancelingReadCloser{ReadCloser: res.Body, cancel: cancel}, nil
}

func (z *zosmfTransport) openRequest(ctx context.Context, dsn, dataType, recordRange string) (*http.Response, error) {
	host := strings.Trim(z.session.Host, "[]")
	hostPort := net.JoinHostPort(host, strconv.Itoa(z.session.Port))
	endpoint := fmt.Sprintf("%s://%s%s/zosmf/restfiles/ds/%s",
		z.session.Protocol, hostPort, z.session.BasePath, url.PathEscape(dsn))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build z/OSMF request: %w", err)
	}
	req.Header.Set("X-CSRF-ZOSMF-HEADER", "")
	req.Header.Set("X-IBM-Migrated-Recall", "error")
	req.Header.Set("X-IBM-Data-Type", dataType)
	if recordRange != "" {
		req.Header.Set("X-IBM-Record-Range", recordRange)
	}
	if z.session.TokenValue != "" {
		cookie := z.session.TokenType
		if cookie == "" {
			cookie = "apimlAuthenticationToken"
		}
		req.Header.Set("Cookie", cookie+"="+z.session.TokenValue)
	} else {
		req.SetBasicAuth(z.session.User, z.session.Password)
	}

	res, err := z.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("data set %s: %w", dsn, err)
	}
	if res.StatusCode == http.StatusOK {
		return res, nil
	}
	defer res.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(res.Body, maxZOSMFErrorBody))
	if readErr != nil {
		return nil, fmt.Errorf("z/OSMF %d for %s (read error response: %v)", res.StatusCode, dsn, readErr)
	}
	message := strings.TrimSpace(string(body))
	var parsed struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &parsed) == nil && strings.TrimSpace(parsed.Message) != "" {
		message = strings.TrimSpace(parsed.Message)
	}
	if message == "" {
		message = http.StatusText(res.StatusCode)
	}
	return nil, fmt.Errorf("z/OSMF %d for %s: %s", res.StatusCode, dsn, message)
}

type cancelingReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (r *cancelingReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.cancel()
	return err
}

// rangedDataSetStream removes z/OSMF's four-byte record lengths. If the server
// returns an unexpected record size or a partial ranged response, it switches
// to a normal binary request and skips bytes already delivered. The hint can
// therefore improve transfer size without changing cq's output.
type rangedDataSetStream struct {
	transport    *zosmfTransport
	context      context.Context
	cancel       context.CancelFunc
	body         io.ReadCloser
	dsn          string
	recordLength int
	pending      []byte
	delivered    int64
	plain        bool
	closed       bool
	err          error
}

func (r *rangedDataSetStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	for len(r.pending) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		if r.plain {
			return r.body.Read(p)
		}

		var header [4]byte
		_, err := io.ReadFull(r.body, header[:])
		if errors.Is(err, io.EOF) {
			return 0, io.EOF
		}
		if err != nil {
			if fallbackErr := r.fallBack(fmt.Sprintf("ranged response ended inside a record header (%v)", err)); fallbackErr != nil {
				return 0, fallbackErr
			}
			continue
		}
		length := int(binary.BigEndian.Uint32(header[:]))
		if length != r.recordLength {
			if err := r.fallBack(fmt.Sprintf("data set record is %d bytes, cq record is %d", length, r.recordLength)); err != nil {
				return 0, err
			}
			continue
		}
		record := make([]byte, length)
		if _, err := io.ReadFull(r.body, record); err != nil {
			if fallbackErr := r.fallBack(fmt.Sprintf("ranged response ended inside a record (%v)", err)); fallbackErr != nil {
				return 0, fallbackErr
			}
			continue
		}
		r.pending = record
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	r.delivered += int64(n)
	return n, nil
}

func (r *rangedDataSetStream) fallBack(reason string) error {
	_ = r.body.Close()
	debugLog.Printf("z/OSMF download %s: %s; retrying without record range", r.dsn, reason)
	res, err := r.transport.openRequest(r.context, r.dsn, "binary", "")
	if err != nil {
		r.err = fmt.Errorf("z/OSMF data set %q: ranged response invalid (%s), and fallback failed: %w", r.dsn, reason, err)
		return r.err
	}
	if r.delivered > 0 {
		if _, err := io.CopyN(io.Discard, res.Body, r.delivered); err != nil {
			_ = res.Body.Close()
			r.err = fmt.Errorf("z/OSMF data set %q: fallback response is shorter than the %d bytes already delivered: %w", r.dsn, r.delivered, err)
			return r.err
		}
	}
	r.body = res.Body
	r.plain = true
	return nil
}

func (r *rangedDataSetStream) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	r.cancel()
	return r.body.Close()
}

// lazyZOSMFTransport defers config parsing and keyring access until a DSN
// is actually used, preserving local-only cq behavior.
type lazyZOSMFTransport struct {
	load      func() (zoweSession, error)
	once      sync.Once
	transport *zosmfTransport
	err       error
}

func newLazyZOSMFTransport(load func() (zoweSession, error)) *lazyZOSMFTransport {
	return &lazyZOSMFTransport{load: load}
}

func (l *lazyZOSMFTransport) get() (*zosmfTransport, error) {
	l.once.Do(func() {
		var session zoweSession
		session, l.err = l.load()
		if l.err == nil {
			debugLog.Printf("Zowe profile %s: %s://%s:%d%s", session.Profile, session.Protocol, session.Host, session.Port, session.BasePath)
			l.transport = newZOSMFTransport(session)
		}
	})
	return l.transport, l.err
}

func (l *lazyZOSMFTransport) fetchCopybook(ctx context.Context, dsn string) ([]byte, error) {
	transport, err := l.get()
	if err != nil {
		return nil, err
	}
	return transport.fetchCopybook(ctx, dsn)
}

func (l *lazyZOSMFTransport) encoding() (string, error) {
	transport, err := l.get()
	if err != nil {
		return "", err
	}
	return transport.session.Encoding, nil
}

func (l *lazyZOSMFTransport) openDataSet(dsn string, hint downloadHint) (io.ReadCloser, error) {
	transport, err := l.get()
	if err != nil {
		return nil, err
	}
	return transport.openDataSet(dsn, hint)
}
