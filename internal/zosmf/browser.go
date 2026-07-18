package zosmf

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

const (
	maxListResponseBody = 8 << 20
	maxRecordSize       = 16 << 20
	maxRecordPageBytes  = 64 << 20
)

// ListDataSets performs one bounded catalog search.
func (z *Client) ListDataSets(ctx context.Context, request ListDataSetsRequest) (DataSetPage, error) {
	maxItems, err := validateMaxItems(request.MaxItems)
	if err != nil {
		return DataSetPage{}, err
	}
	prefix, err := normalizeDataSetPrefix(request.Prefix)
	if err != nil {
		return DataSetPage{}, err
	}
	start, err := normalizeDataSetStart(request.Start)
	if err != nil {
		return DataSetPage{}, err
	}

	query := url.Values{"dslevel": []string{prefix}}
	if start != "" {
		query.Set("start", start)
	}
	req, err := z.newAPIRequest(ctx, http.MethodGet, "/zosmf/restfiles/ds", query)
	if err != nil {
		return DataSetPage{}, err
	}
	requestMaxItems, err := listRequestMaxItems(maxItems, start)
	if err != nil {
		return DataSetPage{}, err
	}
	req.Header.Set("X-IBM-Max-Items", strconv.Itoa(requestMaxItems))
	req.Header.Set("X-IBM-Attributes", "base")

	res, err := z.doRequest(req, prefix, http.StatusOK, http.StatusPartialContent, http.StatusNoContent)
	if err != nil {
		return DataSetPage{}, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNoContent {
		return DataSetPage{Items: []DataSet{}}, nil
	}

	body, err := readBoundedBody(res.Body, maxListResponseBody, "data set list response")
	if err != nil {
		return DataSetPage{}, err
	}
	items, metadata, err := decodeListEnvelope[DataSet](body, "data set list")
	if err != nil {
		return DataSetPage{}, err
	}
	items, moreRows, err := boundedNamePage(items, start, maxItems, requestMaxItems, metadata, res.StatusCode, "data set list", func(item DataSet) string {
		return item.Name
	})
	if err != nil {
		return DataSetPage{}, err
	}
	return DataSetPage{
		Items:        items,
		ReturnedRows: metadata.returnedRows,
		TotalRows:    metadata.totalRows,
		MoreRows:     moreRows,
		JSONVersion:  metadata.jsonVersion,
	}, nil
}

// ListMembers performs one bounded partitioned-data-set member search.
func (z *Client) ListMembers(ctx context.Context, request ListMembersRequest) (MemberPage, error) {
	maxItems, err := validateMaxItems(request.MaxItems)
	if err != nil {
		return MemberPage{}, err
	}
	dataSet, err := normalizeDataSetName(request.DataSet)
	if err != nil {
		return MemberPage{}, err
	}
	start, err := normalizeMemberValue("start", request.Start, false)
	if err != nil {
		return MemberPage{}, err
	}
	pattern, err := normalizeMemberValue("pattern", request.Pattern, true)
	if err != nil {
		return MemberPage{}, err
	}

	query := make(url.Values)
	if start != "" {
		query.Set("start", start)
	}
	if pattern != "" {
		query.Set("pattern", pattern)
	}
	path := "/zosmf/restfiles/ds/" + url.PathEscape(dataSet) + "/member"
	req, err := z.newAPIRequest(ctx, http.MethodGet, path, query)
	if err != nil {
		return MemberPage{}, err
	}
	requestMaxItems, err := listRequestMaxItems(maxItems, start)
	if err != nil {
		return MemberPage{}, err
	}
	req.Header.Set("X-IBM-Max-Items", strconv.Itoa(requestMaxItems))
	req.Header.Set("X-IBM-Attributes", "base")

	res, err := z.doRequest(req, dataSet, http.StatusOK, http.StatusPartialContent, http.StatusNoContent)
	if err != nil {
		return MemberPage{}, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNoContent {
		return MemberPage{Items: []Member{}}, nil
	}

	body, err := readBoundedBody(res.Body, maxListResponseBody, "member list response")
	if err != nil {
		return MemberPage{}, err
	}
	items, metadata, err := decodeListEnvelope[Member](body, "member list")
	if err != nil {
		return MemberPage{}, err
	}
	items, moreRows, err := boundedNamePage(items, start, maxItems, requestMaxItems, metadata, res.StatusCode, dataSet, func(item Member) string {
		return item.Name
	})
	if err != nil {
		return MemberPage{}, err
	}
	return MemberPage{
		Items:        items,
		ReturnedRows: metadata.returnedRows,
		TotalRows:    metadata.totalRows,
		MoreRows:     moreRows,
		JSONVersion:  metadata.jsonVersion,
	}, nil
}

// ReadRecords retrieves exactly one bounded zero-based record range. It never
// retries in binary mode or downloads the full data set.
func (z *Client) ReadRecords(ctx context.Context, request ReadRecordsRequest) (RecordPage, error) {
	maxItems, err := validateMaxItems(request.MaxItems)
	if err != nil {
		return RecordPage{}, err
	}
	if request.Start < 0 {
		return RecordPage{}, &RequestError{Field: "start", Message: "must be zero or greater"}
	}
	if request.Start > math.MaxInt64-int64(maxItems) {
		return RecordPage{}, &RequestError{Field: "start", Message: "record range exceeds the supported integer range"}
	}
	dataSet, err := normalizeDataSetName(request.DataSet)
	if err != nil {
		return RecordPage{}, err
	}
	member, err := normalizeMemberValue("member", request.Member, false)
	if err != nil {
		return RecordPage{}, err
	}
	target := dataSet
	if member != "" {
		target += "(" + member + ")"
	}

	path := "/zosmf/restfiles/ds/" + url.PathEscape(target)
	req, err := z.newAPIRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return RecordPage{}, err
	}
	req.Header.Set("X-IBM-Data-Type", "record")
	req.Header.Set("X-IBM-Record-Range", fmt.Sprintf("%d,%d", request.Start, maxItems))

	res, err := z.doRequest(req, target, http.StatusOK, http.StatusPartialContent, http.StatusNoContent)
	if err != nil {
		return RecordPage{}, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNoContent {
		return RecordPage{Records: []Record{}, Start: request.Start}, nil
	}

	records, err := decodeRecordPage(ctx, res.Body, request.Start, maxItems)
	if err != nil {
		return RecordPage{}, err
	}
	return RecordPage{
		Records:      records,
		Start:        request.Start,
		ReturnedRows: len(records),
		MoreRows:     len(records) == maxItems,
	}, nil
}

func decodeRecordPage(ctx context.Context, reader io.Reader, start int64, maxItems int) ([]Record, error) {
	records := make([]Record, 0, min(maxItems, 1024))
	var total int64
	for len(records) < maxItems {
		var header [4]byte
		n, err := io.ReadFull(reader, header[:])
		if errors.Is(err, io.EOF) && n == 0 {
			break
		}
		if err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return nil, contextErr
			}
			return nil, &ProtocolError{Operation: "record", Message: fmt.Sprintf("truncated four-byte record header after %d records", len(records))}
		}
		length := int64(binary.BigEndian.Uint32(header[:]))
		if length > maxRecordSize {
			return nil, &LimitError{Kind: "record", Limit: maxRecordSize}
		}
		if total+4+length > maxRecordPageBytes {
			return nil, &LimitError{Kind: "record page", Limit: maxRecordPageBytes}
		}
		total += 4 + length
		data := make([]byte, int(length))
		if _, err := io.ReadFull(reader, data); err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return nil, contextErr
			}
			return nil, &ProtocolError{Operation: "record", Message: fmt.Sprintf("record %d declares %d bytes but is truncated", start+int64(len(records))+1, length)}
		}
		records = append(records, Record{Number: start + int64(len(records)) + 1, Data: data})
	}
	return records, nil
}

type listMetadata struct {
	returnedRows    int
	returnedRowsSet bool
	totalRows       *int
	moreRows        bool
	moreRowsSet     bool
	jsonVersion     int
}

func decodeListEnvelope[T any](body []byte, operation string) ([]T, listMetadata, error) {
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil, listMetadata{}, &ProtocolError{Operation: operation, Message: "empty JSON body"}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, listMetadata{}, &ProtocolError{Operation: operation, Message: "malformed JSON"}
	}

	var items []T
	if raw, ok := lookupJSONField(fields, "items"); ok && string(raw) != "null" {
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, listMetadata{}, &ProtocolError{Operation: operation, Message: "items must be an array"}
		}
	}
	metadata := listMetadata{}
	var err error
	metadata.returnedRows, metadata.returnedRowsSet, err = optionalJSONInt(fields, "returnedRows")
	if err != nil {
		return nil, listMetadata{}, &ProtocolError{Operation: operation, Message: err.Error()}
	}
	if metadata.returnedRows < 0 {
		return nil, listMetadata{}, &ProtocolError{Operation: operation, Message: "returnedRows must not be negative"}
	}
	if totalRows, set, err := optionalJSONInt(fields, "totalRows"); err != nil {
		return nil, listMetadata{}, &ProtocolError{Operation: operation, Message: err.Error()}
	} else if set {
		if totalRows < 0 {
			return nil, listMetadata{}, &ProtocolError{Operation: operation, Message: "totalRows must not be negative"}
		}
		metadata.totalRows = &totalRows
	}
	metadata.moreRows, metadata.moreRowsSet, err = optionalJSONBool(fields, "moreRows")
	if err != nil {
		return nil, listMetadata{}, &ProtocolError{Operation: operation, Message: err.Error()}
	}
	metadata.jsonVersion, _, err = optionalJSONInt(fields, "JSONversion")
	if err != nil {
		return nil, listMetadata{}, &ProtocolError{Operation: operation, Message: err.Error()}
	}
	if !metadata.returnedRowsSet {
		metadata.returnedRows = len(items)
	}
	if metadata.returnedRows > 0 {
		raw, ok := lookupJSONField(fields, "items")
		if !ok || string(raw) == "null" {
			return nil, listMetadata{}, &ProtocolError{Operation: operation, Message: "returnedRows is positive but items is missing"}
		}
	}
	return items, metadata, nil
}

func boundedNamePage[T any](items []T, start string, maxItems, requestMaxItems int, metadata listMetadata, statusCode int, resource string, name func(T) string) ([]T, bool, error) {
	moreRows := metadata.moreRows || statusCode == http.StatusPartialContent
	if !metadata.moreRowsSet {
		moreRows = moreRows || metadata.returnedRows >= requestMaxItems || len(items) >= requestMaxItems
	}
	if metadata.returnedRows > len(items) || len(items) > requestMaxItems {
		moreRows = true
	}

	bounded := make([]T, 0, min(len(items), maxItems))
	atBoundary := start != ""
	for _, item := range items {
		itemName := strings.ToUpper(strings.TrimSpace(name(item)))
		if itemName == "" {
			return nil, false, &ProtocolError{Operation: "list", Message: "an item has no name"}
		}
		if atBoundary && itemName == start {
			continue
		}
		atBoundary = false
		if len(bounded) == maxItems {
			moreRows = true
			break
		}
		bounded = append(bounded, item)
	}
	if start != "" && moreRows && len(bounded) == 0 {
		return nil, false, &NoProgressError{Resource: resource, Start: start}
	}
	return bounded, moreRows, nil
}

func lookupJSONField(fields map[string]json.RawMessage, name string) (json.RawMessage, bool) {
	if raw, ok := fields[name]; ok {
		return raw, true
	}
	for key, raw := range fields {
		if strings.EqualFold(key, name) {
			return raw, true
		}
	}
	return nil, false
}

func optionalJSONInt(fields map[string]json.RawMessage, name string) (int, bool, error) {
	raw, ok := lookupJSONField(fields, name)
	if !ok || string(raw) == "null" {
		return 0, false, nil
	}
	var value int
	if err := json.Unmarshal(raw, &value); err == nil {
		return value, true, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		parsed, err := strconv.Atoi(strings.TrimSpace(text))
		if err == nil {
			return parsed, true, nil
		}
	}
	return 0, false, fmt.Errorf("%s must be an integer", name)
}

func optionalJSONBool(fields map[string]json.RawMessage, name string) (bool, bool, error) {
	raw, ok := lookupJSONField(fields, name)
	if !ok || string(raw) == "null" {
		return false, false, nil
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err == nil {
		return value, true, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		parsed, err := strconv.ParseBool(strings.TrimSpace(text))
		if err == nil {
			return parsed, true, nil
		}
	}
	var number int
	if err := json.Unmarshal(raw, &number); err == nil && (number == 0 || number == 1) {
		return number == 1, true, nil
	}
	return false, false, fmt.Errorf("%s must be a boolean", name)
}

func validateMaxItems(value int) (int, error) {
	if value <= 0 {
		return 0, &RequestError{Field: "max items", Message: "must be positive; zero would request an unbounded response"}
	}
	return value, nil
}

func listRequestMaxItems(maxItems int, start string) (int, error) {
	if maxItems <= 0 {
		return 0, &RequestError{Field: "max items", Message: "must be positive"}
	}
	return maxItems, nil
}

func invalidBrowseName(value string) bool {
	if strings.ContainsAny(value, "()/\\") {
		return true
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func normalizeDataSetPrefix(value string) (string, error) {
	prefix := strings.ToUpper(strings.TrimSpace(value))
	if prefix == "" {
		return "", &RequestError{Field: "prefix", Message: "must not be empty"}
	}
	if invalidBrowseName(prefix) {
		return "", &RequestError{Field: "prefix", Message: "contains invalid data set characters"}
	}
	if !strings.ContainsAny(prefix, "*%") {
		prefix += "*"
	}
	if len(prefix) > 44 {
		return "", &RequestError{Field: "prefix", Message: "must not exceed 44 characters including wildcards"}
	}
	return prefix, nil
}

func normalizeDataSetStart(value string) (string, error) {
	start := strings.ToUpper(strings.TrimSpace(value))
	if invalidBrowseName(start) {
		return "", &RequestError{Field: "start", Message: "contains invalid data set characters"}
	}
	if len(start) > 44 {
		return "", &RequestError{Field: "start", Message: "data set name must not exceed 44 characters"}
	}
	if strings.ContainsAny(start, "*%") {
		return "", &RequestError{Field: "start", Message: "must not contain wildcards"}
	}
	return start, nil
}

func normalizeDataSetName(value string) (string, error) {
	name := strings.ToUpper(strings.TrimSpace(value))
	if name == "" {
		return "", &RequestError{Field: "data set", Message: "must not be empty"}
	}
	if invalidBrowseName(name) {
		return "", &RequestError{Field: "data set", Message: "contains invalid data set characters"}
	}
	if len(name) > 44 {
		return "", &RequestError{Field: "data set", Message: "must not exceed 44 characters"}
	}
	if strings.ContainsAny(name, "*%") {
		return "", &RequestError{Field: "data set", Message: "must not contain wildcards"}
	}
	return name, nil
}

func normalizeMemberValue(field, value string, allowWildcards bool) (string, error) {
	member := strings.ToUpper(strings.TrimSpace(value))
	if invalidBrowseName(member) {
		return "", &RequestError{Field: field, Message: "contains invalid member characters"}
	}
	if len(member) > 8 {
		return "", &RequestError{Field: field, Message: "must not exceed 8 characters"}
	}
	if !allowWildcards && strings.ContainsAny(member, "*%") {
		return "", &RequestError{Field: field, Message: "must not contain wildcards"}
	}
	return member, nil
}
