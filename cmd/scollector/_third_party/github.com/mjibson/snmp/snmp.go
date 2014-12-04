// Package snmp provides an implementation of the SNMP specification.
package snmp

import (
	"encoding/asn1"
	"fmt"
	"math/rand"
	"net"
	"time"
)

// Reserved binding values.
var (
	null           = asn1.RawValue{Class: 0, Tag: 5}
	noSuchObject   = asn1.RawValue{Class: 2, Tag: 0}
	noSuchInstance = asn1.RawValue{Class: 2, Tag: 1}
	endOfMibView   = asn1.RawValue{Class: 2, Tag: 2}
)

// Binding represents an assignment to a variable, a.k.a. managed object.
type Binding struct {
	Name  asn1.ObjectIdentifier
	Value asn1.RawValue
}

// unmarshal stores in v the value part of binding b.
func (b *Binding) unmarshal(v interface{}) error {
	convertClass(&b.Value)
	_, err := asn1.Unmarshal(b.Value.FullBytes, v)
	if err != nil {
		if _, ok := err.(asn1.StructuralError); ok {
			return fmt.Errorf("type mismatch: {class:%d tag:%d} vs. %T: %v",
				b.Value.Class, b.Value.Tag, v, err)
		}
		return err
	}
	v = convertType(v)
	return nil
}

// convertClass converts the encoding of values in SNMP response from
// "custom" class to the corresponding "universal" class, thus enabling
// use of the asn1 parser from the encoding/asn1 package.
func convertClass(v *asn1.RawValue) {
	if v.Class != 1 {
		// Not a custom type.
		return
	}
	switch v.Tag {
	case 0, 4:
		// IpAddress ::= [APPLICATION 0] IMPLICIT OCTET STRING (SIZE (4))
		// Opaque ::= [APPLICATION 4] IMPLICIT OCTET STRING
		v.FullBytes[0] = 0x04
		v.Class = 0
		v.Tag = 4
	case 1, 2, 3, 6:
		// Counter32 ::= [APPLICATION 1] IMPLICIT INTEGER (0..4294967295)
		// Unsigned32 ::= [APPLICATION 2] IMPLICIT INTEGER (0..4294967295)
		// TimeTicks ::= [APPLICATION 3] IMPLICIT INTEGER (0..4294967295)
		// Counter64 ::= [APPLICATION 6] IMPLICIT INTEGER (0..18446744073709551615)
		v.FullBytes[0] = 0x02
		v.Class = 0
		v.Tag = 2
	}
}

// convertType converts value in SNMP response to a Go type that is
// easier to manipulate.
func convertType(v interface{}) interface{} {
	switch v := v.(type) {
	case []byte:
		s, ok := toString(v)
		if !ok {
			return v
		}
		return s
	default:
		return v
	}
}

// less checks if a precedes b in the MIB tree.
func (a Binding) less(b Binding) bool {
	switch {
	case len(a.Name) < len(b.Name):
		for i := 0; i < len(a.Name); i++ {
			switch {
			case a.Name[i] < b.Name[i]:
				return true
			case a.Name[i] == b.Name[i]:
				continue
			case a.Name[i] > b.Name[i]:
				return false
			}
		}
		return true

	case len(a.Name) == len(b.Name):
		for i := 0; i < len(a.Name); i++ {
			switch {
			case a.Name[i] < b.Name[i]:
				return true
			case a.Name[i] == b.Name[i]:
				continue
			case a.Name[i] > b.Name[i]:
				return false
			}
		}
		// Identical, so not less.
		return false

	case len(a.Name) > len(b.Name):
		for i := 0; i < len(b.Name); i++ {
			switch {
			case a.Name[i] < b.Name[i]:
				return true
			case a.Name[i] == b.Name[i]:
				continue
			case a.Name[i] > b.Name[i]:
				return false
			}
		}
		return false

	}
	panic("unreached")
}

// A Request represents an SNMP request to be sent over a Transport.
type Request struct {
	ID             int32
	Type           string // "Get", "GetNext", "GetBulk"
	Bindings       []Binding
	NonRepeaters   int
	MaxRepetitions int
}

// Response represents the response from an SNMP request.
type Response struct {
	ID          int32
	ErrorStatus int
	ErrorIndex  int
	Bindings    []Binding
}

// RoundTripper is an interface representing the ability to execute a
// single SNMP transaction, obtaining the Response for a given Request.
//
// A RoundTripper must be safe for concurrent use by multiple goroutines.
type RoundTripper interface {
	RoundTrip(*Request) (*Response, error)
}

// Transport is an implementation of RoundTripper that supports SNMPv2
// as defined by RFC 3416.
type Transport struct {
	Conn      net.Conn
	Community string
}

func newTransport(host, community string) (*Transport, error) {
	hostport := host
	if _, _, err := net.SplitHostPort(hostport); err != nil {
		hostport = host + ":161"
	}
	addr, err := net.ResolveUDPAddr("udp", hostport)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return nil, err
	}
	return &Transport{conn, community}, nil
}

// RoundTrip implements the RoundTripper interface.
func (tr *Transport) RoundTrip(req *Request) (*Response, error) {
	for i := range req.Bindings {
		req.Bindings[i].Value = null
	}
	var buf []byte
	var err error
	switch req.Type {
	case "Get":
		var p struct {
			Version   int
			Community []byte
			Data      struct {
				RequestID   int32
				ErrorStatus int
				ErrorIndex  int
				Bindings    []Binding
			} `asn1:"application,tag:0"`
		}
		p.Version = 1
		p.Community = []byte(tr.Community)
		p.Data.RequestID = req.ID
		p.Data.Bindings = req.Bindings
		buf, err = asn1.Marshal(p)
	case "GetNext":
		var p struct {
			Version   int
			Community []byte
			Data      struct {
				RequestID   int32
				ErrorStatus int
				ErrorIndex  int
				Bindings    []Binding
			} `asn1:"application,tag:1"`
		}
		p.Version = 1
		p.Community = []byte(tr.Community)
		p.Data.RequestID = req.ID
		p.Data.Bindings = req.Bindings
		buf, err = asn1.Marshal(p)
	case "GetBulk":
		var p struct {
			Version   int
			Community []byte
			Data      struct {
				RequestID      int32
				NonRepeaters   int
				MaxRepetitions int
				Bindings       []Binding
			} `asn1:"application,tag:5"`
		}
		p.Version = 1
		p.Community = []byte(tr.Community)
		p.Data.RequestID = req.ID
		p.Data.NonRepeaters = 0
		p.Data.MaxRepetitions = req.MaxRepetitions
		p.Data.Bindings = req.Bindings
		buf, err = asn1.Marshal(p)
	default:
		panic("unsupported type " + req.Type)
	}
	if err != nil {
		return nil, err
	}
	if _, err := tr.Conn.Write(buf); err != nil {
		return nil, err
	}
	buf = make([]byte, 10000, 10000)
	if err := tr.Conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return nil, err
	}
	n, err := tr.Conn.Read(buf)
	if err != nil {
		return nil, err
	}
	if n == len(buf) {
		return nil, fmt.Errorf("response too big")
	}
	var p struct {
		Version   int
		Community []byte
		Data      struct {
			RequestID   int32
			ErrorStatus int
			ErrorIndex  int
			Bindings    []Binding
		} `asn1:"tag:2"`
	}
	if _, err = asn1.Unmarshal(buf[:n], &p); err != nil {
		return nil, err
	}
	resp := &Response{p.Data.RequestID, p.Data.ErrorStatus, p.Data.ErrorIndex, p.Data.Bindings}
	return resp, nil
}

// check checks the response PDU for basic correctness.
// Valid with all PDU types.
func check(resp *Response, req *Request) (err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("invalid response: %v", err)
		}
	}()

	if resp.ID != req.ID {
		return fmt.Errorf("id mismatch")
	}

	if e, i := resp.ErrorStatus, resp.ErrorIndex; e != 0 {
		err := fmt.Errorf("server error: %v", errorStatus(e))
		if i >= 0 && i < len(resp.Bindings) {
			err = fmt.Errorf("binding %+v: %v", req.Bindings[i], err)
		}
		return err
	}

	switch n := len(resp.Bindings); {
	case n == 0:
		return fmt.Errorf("no bindings")
	case n < len(req.Bindings):
		return fmt.Errorf("missing bindings")
	case n > len(req.Bindings) && req.Type != "GetBulk":
		return fmt.Errorf("extraneous bindings")
	}

	eq := func(a, b asn1.RawValue) bool {
		return a.Class == b.Class && a.Tag == b.Tag
	}
	for _, b := range resp.Bindings {
		switch v := b.Value; {
		case eq(v, noSuchObject):
			return fmt.Errorf("%v: no such object", b.Name)
		case eq(v, noSuchInstance):
			return fmt.Errorf("%v: no such instance", b.Name)
		case eq(v, endOfMibView):
			return fmt.Errorf("%v: end of mib view", b.Name)
		case eq(v, null):
			return fmt.Errorf("%v: unexpected null", b.Name)
		}
		if !hasPrefix(b.Name, []int{1, 3}) {
			return fmt.Errorf("%v: missing [1 3] prefix", b.Name)
		}
	}

	return nil
}

// hasPrefix tests if given object instance id falls within the mib subtree
// defined by the prefix.
func hasPrefix(instance, prefix []int) bool {
	if len(instance) < len(prefix) {
		return false
	}
	for i := range prefix {
		if instance[i] != prefix[i] {
			return false
		}
	}
	return true
}

// errorText is the set of response errors specified in RFC 3416.
var errorText = map[errorStatus]string{
	0:  "no error",
	1:  "too big",
	2:  "no such name",
	3:  "bad value",
	4:  "read only",
	5:  "gen err",
	6:  "no access",
	7:  "wrong type",
	8:  "wrong length",
	9:  "wrong encoding",
	10: "wrong value",
	11: "no creation",
	12: "inconsistent value",
	13: "resource unavailable",
	14: "commit failed",
	15: "undo failed",
	16: "authorization error",
	17: "not writable",
	18: "inconsistent name",
}

// errorStatus represents response error code.
type errorStatus int

// String returns the text form of error e.
func (e errorStatus) String() string {
	s := errorText[e]
	if s == "" {
		s = fmt.Sprintf("code %d", e)
	}
	return s
}

// toString attempts to convert a byte string to ascii string of
// printable characters.
func toString(x []byte) (string, bool) {
	if len(x) == 0 {
		return "", false
	}
	if int(x[0]) != len(x[1:]) {
		return "", false
	}
	buf := make([]byte, len(x[1:]))
	for i, c := range x[1:] {
		if c < 0x20 || c > 0x7e {
			return "", false
		}
		buf[i] = byte(c)
	}
	return string(buf), true
}

// toStringInt attempts to convert an int string to ascii string of
// printable characters.
func toStringInt(x []int) (string, bool) {
	if len(x) == 0 {
		return "", false
	}
	if int(x[0]) != len(x[1:]) {
		return "", false
	}
	buf := make([]byte, len(x[1:]))
	for i, c := range x[1:] {
		if c < 0x20 || c > 0x7e {
			return "", false
		}
		buf[i] = byte(c)
	}
	return string(buf), true
}

// nextID generates random request IDs. Randomness prevents eavesdroppers
// from inferring application startup time.
var nextID = make(chan int32)

func init() {
	rand.Seed(time.Now().UnixNano())
	go func() {
		for {
			nextID <- rand.Int31()
		}
	}()
}
