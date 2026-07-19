package zosmf

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Browser is the bounded, read-only z/OSMF surface used by interactive
// clients. Implementations must never replace a bounded request with an
// unbounded whole-data-set download.
type Browser interface {
	ListDataSets(context.Context, ListDataSetsRequest) (DataSetPage, error)
	ListMembers(context.Context, ListMembersRequest) (MemberPage, error)
	ReadRecords(context.Context, ReadRecordsRequest) (RecordPage, error)
	FetchText(context.Context, string) ([]byte, error)
	Encoding() (string, error)
}

// ListDataSetsRequest describes one bounded catalog search. MaxItems must be
// positive; zero is deliberately rejected because z/OSMF interprets it as
// "return all items". Start is sent as an inclusive cursor, and a matching
// leading boundary item is removed from the returned page.
type ListDataSetsRequest struct {
	Prefix   string
	Start    string
	MaxItems int
	// ExactName sends Prefix as dslevel without the implicit trailing
	// wildcard, so one specific catalog entry can be checked even when its
	// last qualifier is already eight characters or the name is 44 long.
	ExactName bool
}

// ListMembersRequest describes one bounded member search. Start is sent using
// z/OSMF's inclusive cursor semantics; a matching boundary item is removed
// from the returned page so callers can safely use the prior page's last name.
type ListMembersRequest struct {
	DataSet  string
	Start    string
	Pattern  string
	MaxItems int
}

// ReadRecordsRequest describes one bounded record-mode retrieval. Start is a
// zero-based logical record offset. Member is optional.
type ReadRecordsRequest struct {
	DataSet  string
	Member   string
	Start    int64
	MaxItems int
}

// DataSet contains the base attributes returned by the z/OSMF catalog API.
// z/OSMF represents these values as strings, including numeric attributes.
type DataSet struct {
	Name           string
	Organization   string
	Volume         string
	BlockSize      string
	RecordLength   string
	RecordFormat   string
	CatalogName    string
	CreationDate   string
	DeviceType     string
	DataSetType    string
	ExpirationDate string
	Extents        string
	Migrated       string
	MultiVolume    string
	Overflow       string
	ReferenceDate  string
	Size           string
	SpaceUnits     string
	Used           string
	Volumes        string
}

// UnmarshalJSON accepts the documented z/OSMF base attributes. Numeric JSON
// values are tolerated because local and older servers do not always encode
// every string-valued attribute consistently.
func (d *DataSet) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	var err error
	*d = DataSet{}
	assign := func(target *string, names ...string) {
		if err != nil {
			return
		}
		*target, err = jsonStringField(fields, names...)
	}
	assign(&d.Name, "dsname")
	assign(&d.Organization, "dsorg")
	assign(&d.Volume, "vol")
	assign(&d.BlockSize, "blksz")
	// The local development mock historically emitted lrectl. Accepting that
	// alias is harmless against IBM while keeping mock smoke tests useful.
	assign(&d.RecordLength, "lrecl", "lrectl")
	assign(&d.RecordFormat, "recfm")
	assign(&d.CatalogName, "catnm")
	assign(&d.CreationDate, "cdate")
	assign(&d.DeviceType, "dev")
	assign(&d.DataSetType, "dsntp")
	assign(&d.ExpirationDate, "edate")
	assign(&d.Extents, "extx")
	assign(&d.Migrated, "migr")
	assign(&d.MultiVolume, "mvol")
	assign(&d.Overflow, "ovf")
	assign(&d.ReferenceDate, "rdate")
	assign(&d.Size, "sizex")
	assign(&d.SpaceUnits, "spacu")
	assign(&d.Used, "used")
	assign(&d.Volumes, "vols")
	return err
}

// Member contains the commonly returned ISPF member statistics. All fields
// except Name are optional in z/OSMF responses.
type Member struct {
	Name            string
	Version         int
	Modification    int
	CreationDate    string
	ModifiedDate    string
	CurrentRecords  int
	InitialRecords  int
	ModifiedRecords int
	ModifiedTime    string
	ModifiedSeconds string
	User            string
	SCLM            string
}

// UnmarshalJSON tolerates quoted numeric member statistics while retaining
// typed integers for callers.
func (m *Member) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	var err error
	*m = Member{}
	assignString := func(target *string, names ...string) {
		if err != nil {
			return
		}
		*target, err = jsonStringField(fields, names...)
	}
	assignInt := func(target *int, name string) {
		if err != nil {
			return
		}
		*target, err = jsonIntField(fields, name)
	}
	assignString(&m.Name, "member")
	assignInt(&m.Version, "vers")
	assignInt(&m.Modification, "mod")
	assignString(&m.CreationDate, "c4date")
	assignString(&m.ModifiedDate, "m4date")
	assignInt(&m.CurrentRecords, "cnorc")
	assignInt(&m.InitialRecords, "inorc")
	assignInt(&m.ModifiedRecords, "mnorc")
	assignString(&m.ModifiedTime, "mtime")
	assignString(&m.ModifiedSeconds, "msec")
	assignString(&m.User, "user")
	assignString(&m.SCLM, "sclm")
	return err
}

// DataSetPage is one bounded data set search result. ReturnedRows is the
// server-reported count before inclusive-cursor de-duplication; len(Items) is
// the bounded count available to the caller.
type DataSetPage struct {
	Items        []DataSet
	ReturnedRows int
	TotalRows    *int
	MoreRows     bool
	JSONVersion  int
}

// MemberPage is one bounded member search result. ReturnedRows is the
// server-reported count before inclusive-cursor de-duplication; len(Items) is
// the bounded count available to the caller.
type MemberPage struct {
	Items        []Member
	ReturnedRows int
	TotalRows    *int
	MoreRows     bool
	JSONVersion  int
}

// Record is one logical record. Number is one-based for display; the request
// range and RecordPage.Start remain zero-based to match z/OSMF.
type Record struct {
	Number int64
	Data   []byte
}

// RecordPage is one bounded record-mode response.
type RecordPage struct {
	Records      []Record
	Start        int64
	ReturnedRows int
	MoreRows     bool
}

func jsonStringField(fields map[string]json.RawMessage, names ...string) (string, error) {
	var raw json.RawMessage
	var foundName string
	for _, name := range names {
		if value, ok := fields[name]; ok {
			raw = value
			foundName = name
			break
		}
	}
	if raw == nil || string(raw) == "null" {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return value, nil
	}
	var number json.Number
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err == nil {
		return number.String(), nil
	}
	return "", fmt.Errorf("z/OSMF field %q must be a string or number", foundName)
}

func jsonIntField(fields map[string]json.RawMessage, name string) (int, error) {
	raw, ok := fields[name]
	if !ok || string(raw) == "null" {
		return 0, nil
	}
	var value int
	if err := json.Unmarshal(raw, &value); err == nil {
		return value, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		parsed, parseErr := strconv.Atoi(strings.TrimSpace(text))
		if parseErr != nil {
			return 0, fmt.Errorf("z/OSMF field %q must be an integer", name)
		}
		return parsed, nil
	}
	return 0, fmt.Errorf("z/OSMF field %q must be an integer", name)
}
