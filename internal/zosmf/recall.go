package zosmf

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// Recaller requests recall of a migrated data set. It is deliberately separate
// from Browser: recall changes where a data set resides, never its contents,
// and clients that cannot recall simply do not implement it.
type Recaller interface {
	RecallDataSet(ctx context.Context, dsn string) error
}

var (
	_ Recaller = (*Client)(nil)
	_ Recaller = (*LazyClient)(nil)
)

// recallBody asks z/OSMF not to wait for the recall to complete: the request
// queues on the host and callers watch the catalog for completion.
const recallBody = `{"request":"hrecall","wait":false}`

// RecallDataSet submits an HRECALL request for a migrated data set and returns
// once z/OSMF has accepted it. Completion is observed through the catalog:
// re-list the data set and check IsMigrated.
func (z *Client) RecallDataSet(ctx context.Context, dsn string) error {
	name, err := normalizeDataSetName(dsn)
	if err != nil {
		return err
	}
	path := "/zosmf/restfiles/ds/" + url.PathEscape(name)
	req, err := z.newAPIRequest(ctx, http.MethodPut, path, nil, strings.NewReader(recallBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := z.doRequest(req, name, http.StatusOK)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	// The accepted response carries no useful body; drain it so the
	// connection can be reused.
	_, _, _ = readAtMost(res.Body, maxErrorBody)
	z.logf("z/OSMF recall %s: accepted", name)
	return nil
}

// RecallDataSet lazily initializes the client and submits an HRECALL request.
func (l *LazyClient) RecallDataSet(ctx context.Context, dsn string) error {
	if err := browserContextError(ctx); err != nil {
		return err
	}
	client, err := l.get()
	if err != nil {
		return err
	}
	return client.RecallDataSet(ctx, dsn)
}

// IsMigrated reports whether the catalog says a data set is on migration
// storage. z/OSMF exposes this as migr=YES, and migrated entries also carry
// the pseudo volume names MIGRAT (DFSMShsm) or ARCIVE (CA Disk).
func IsMigrated(d DataSet) bool {
	if strings.EqualFold(strings.TrimSpace(d.Migrated), "YES") {
		return true
	}
	switch strings.ToUpper(strings.TrimSpace(d.Volume)) {
	case "MIGRAT", "ARCIVE":
		return true
	}
	return false
}
