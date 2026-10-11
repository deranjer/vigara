package synology

import (
	"context"
	"encoding/asn1"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
)

type fixtureClient struct {
	values map[string]any
	rows   map[string]map[string]any
	getErr error
}
type snmpFixtureOptions struct {
	drop        bool
	wrongPDU    bool
	errorStatus gosnmp.SNMPError
	diskCount   int
}

func (f fixtureClient) Get(context.Context, []string) (map[string]any, error) {
	return f.values, f.getErr
}
func (f fixtureClient) Walk(_ context.Context, oid string) (map[string]any, error) {
	return f.rows[oid], nil
}

func TestCollectUsesFixedSchemaAndNASLabels(t *testing.T) {
	rows := map[string]map[string]any{
		DiskStatusOID: {DiskStatusOID + ".2": 1, DiskStatusOID + ".1": 5, DiskStatusOID + ".3": 6, DiskStatusOID + ".4": 999},
		DiskTempOID:   {DiskTempOID + ".1": 34, DiskTempOID + ".2": 41, DiskTempOID + ".3": 151},
	}
	c := NewWithClient("nas.example", fixtureClient{values: map[string]any{SystemStatusOID: 1, TemperatureOID: 42}, rows: rows})
	got, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Metrics) != 6 {
		t.Fatalf("got %d metrics, want system status/temp, two disk statuses and two disk temperatures: %+v", len(got.Metrics), got.Metrics)
	}
	for _, m := range got.Metrics {
		if m.Labels["nas_target"] != "nas.example" {
			t.Fatalf("remote target missing: %+v", m)
		}
		if strings.Contains(m.Name, "disk_") && m.Labels["disk_index"] == "" {
			t.Fatalf("disk index missing: %+v", m)
		}
	}
	if len(got.Capabilities) != 2 || got.Capabilities[0] != "synology_system_monitoring" || got.Capabilities[1] != "synology_disk_monitoring" {
		t.Fatalf("unexpected capabilities: %v", got.Capabilities)
	}
}

func TestCollectFailureDoesNotExposeSNMPSecretOrInventMetrics(t *testing.T) {
	c := NewWithClient("nas.example", fixtureClient{getErr: errors.New("community=private-secret timeout")})
	got, err := c.Collect(context.Background())
	if err == nil || strings.Contains(err.Error(), "private-secret") || len(got.Metrics) != 0 {
		t.Fatalf("unsafe SNMP failure result: metrics=%v err=%v", got.Metrics, err)
	}
}

func TestNewValidatesTargetAndKeepsCommunityOutOfErrors(t *testing.T) {
	if c, err := New(Config{}); err != nil || c != nil {
		t.Fatalf("disabled configuration returned %v, %v", c, err)
	}
	_, err := New(Config{Enabled: true, Target: "bad", Community: "secret-community"})
	if err == nil || strings.Contains(err.Error(), "secret-community") {
		t.Fatalf("unsafe configuration error: %v", err)
	}
}

// This small loopback SNMP responder exercises gosnmp's actual BER encoding,
// returned OID spelling, integer decoding, and GetBulk iteration.
func TestCollectAgainstLocalSNMPFixture(t *testing.T) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	stop := make(chan struct{})
	defer close(stop)
	go serveSNMPFixture(conn, stop, snmpFixtureOptions{diskCount: 2})
	c, err := New(Config{Enabled: true, Target: net.JoinHostPort("127.0.0.1", strconv.Itoa(conn.LocalAddr().(*net.UDPAddr).Port)), Community: "local-fixture", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.client.Get(context.Background(), []string{SystemStatusOID, TemperatureOID}); err != nil {
		t.Fatalf("fixture GET failed: %v", err)
	}
	got, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Metrics) != 6 {
		t.Fatalf("fixture returned %d metrics, want six: %+v", len(got.Metrics), got.Metrics)
	}
	for _, m := range got.Metrics {
		if m.Labels["nas_target"] != "127.0.0.1" {
			t.Fatalf("returned OID or target label mismatch: %+v", m)
		}
	}
}

func TestSNMPWireRejectsErrorAndMismatchedResponses(t *testing.T) {
	for _, options := range []snmpFixtureOptions{{diskCount: 2, wrongPDU: true}, {diskCount: 2, errorStatus: gosnmp.NoAccess}} {
		conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
		if err != nil {
			t.Fatal(err)
		}
		stop := make(chan struct{})
		go serveSNMPFixture(conn, stop, options)
		t.Cleanup(func() { close(stop); _ = conn.Close() })
		c, err := New(Config{Enabled: true, Target: net.JoinHostPort("127.0.0.1", strconv.Itoa(conn.LocalAddr().(*net.UDPAddr).Port)), Community: "local-fixture", Timeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.client.Get(context.Background(), []string{SystemStatusOID, TemperatureOID}); err == nil {
			t.Fatal("accepted error-status or mismatched-PDU response")
		}
	}
}

func TestSNMPWireCancellationIsPrompt(t *testing.T) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop); _ = conn.Close() })
	go serveSNMPFixture(conn, stop, snmpFixtureOptions{drop: true})
	c, err := New(Config{Enabled: true, Target: net.JoinHostPort("127.0.0.1", strconv.Itoa(conn.LocalAddr().(*net.UDPAddr).Port)), Community: "local-fixture", Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := c.client.Get(ctx, []string{SystemStatusOID}); done <- err }()
	time.Sleep(20 * time.Millisecond)
	started := time.Now()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled SNMP request succeeded")
		}
		if time.Since(started) > 500*time.Millisecond {
			t.Fatal("SNMP request did not stop promptly after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("SNMP request ignored context cancellation")
	}
}

func TestCollectionOverallDeadlineBoundsSilentSNMPAgent(t *testing.T) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop); _ = conn.Close() })
	go serveSNMPFixture(conn, stop, snmpFixtureOptions{drop: true})
	c, err := New(Config{Enabled: true, Target: net.JoinHostPort("127.0.0.1", strconv.Itoa(conn.LocalAddr().(*net.UDPAddr).Port)), Community: "local-fixture", Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = c.Collect(ctx)
	if err == nil {
		t.Fatal("silent SNMP target returned success")
	}
	if time.Since(started) > 500*time.Millisecond {
		t.Fatal("collector exceeded the overall deadline")
	}
}

func TestSNMPWalkOrdersOIDArcsNumericallyAcrossTwelveDisks(t *testing.T) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	stop := make(chan struct{})
	defer close(stop)
	go serveSNMPFixture(conn, stop, snmpFixtureOptions{diskCount: 12})
	c, err := New(Config{Enabled: true, Target: net.JoinHostPort("127.0.0.1", strconv.Itoa(conn.LocalAddr().(*net.UDPAddr).Port)), Community: "local-fixture", Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Metrics) != 26 {
		t.Fatalf("12-disk fixture returned %d metrics, want 26", len(got.Metrics))
	}
	seen := map[string]bool{}
	for _, metric := range got.Metrics {
		if metric.Labels["disk_index"] != "" {
			seen[metric.Labels["disk_index"]] = true
		}
	}
	if !seen["9"] || !seen["10"] || !seen["12"] {
		t.Fatalf("numeric OID walk omitted indexes: %v", seen)
	}
}

func serveSNMPFixture(conn *net.UDPConn, stop <-chan struct{}, options snmpFixtureOptions) {
	buf := make([]byte, 65535)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			select {
			case <-stop:
				return
			default:
				continue
			}
		}
		var msg struct {
			Version   int
			Community asn1.RawValue
			Data      asn1.RawValue
		}
		if _, err = asn1.Unmarshal(buf[:n], &msg); err != nil {
			continue
		}
		var req struct {
			ID       int
			Error    int
			Index    int
			Bindings asn1.RawValue
		}
		rest, decodeErr := asn1.Unmarshal(msg.Data.Bytes, &req.ID)
		if decodeErr == nil {
			rest, decodeErr = asn1.Unmarshal(rest, &req.Error)
		}
		if decodeErr == nil {
			rest, decodeErr = asn1.Unmarshal(rest, &req.Index)
		}
		if decodeErr == nil {
			_, decodeErr = asn1.Unmarshal(rest, &req.Bindings)
		}
		if decodeErr != nil {
			continue
		}
		var requested []string
		for rest := req.Bindings.Bytes; len(rest) > 0; {
			var binding struct {
				Name  asn1.ObjectIdentifier
				Value asn1.RawValue
			}
			var tail []byte
			tail, err = asn1.Unmarshal(rest, &binding)
			if err != nil {
				break
			}
			requested = append(requested, "."+binding.Name.String())
			rest = tail
		}
		if err != nil || len(requested) == 0 {
			continue
		}
		if options.drop {
			continue
		}
		response := &gosnmp.SnmpPacket{Version: gosnmp.Version2c, Community: string(msg.Community.Bytes), PDUType: gosnmp.GetResponse, RequestID: uint32(req.ID)}
		if options.wrongPDU {
			response.PDUType = gosnmp.GetNextRequest
		}
		response.Error = options.errorStatus
		if msg.Data.Class == 2 && msg.Data.Tag == 0 {
			for _, oid := range requested {
				value := 1
				if oid == TemperatureOID {
					value = 42
				}
				response.Variables = append(response.Variables, gosnmp.SnmpPDU{Name: oid, Type: gosnmp.Integer, Value: value})
			}
		}
		if msg.Data.Class == 2 && msg.Data.Tag == 5 {
			root := requested[0]
			var candidates []string
			var value int
			switch {
			case strings.HasPrefix(root, DiskStatusOID):
				for i := 1; i <= options.diskCount; i++ {
					candidates = append(candidates, DiskStatusOID+"."+strconv.Itoa(i))
				}
			case strings.HasPrefix(root, DiskTempOID):
				for i := 1; i <= options.diskCount; i++ {
					candidates = append(candidates, DiskTempOID+"."+strconv.Itoa(i))
				}
			}
			next := root
			typ := gosnmp.EndOfMibView
			for _, candidate := range candidates {
				if oidLess(root, candidate) {
					if strings.HasPrefix(root, DiskStatusOID) && strings.HasPrefix(candidate, DiskStatusOID+".") {
						idx, _ := strconv.Atoi(strings.TrimPrefix(candidate, DiskStatusOID+"."))
						if idx > options.diskCount {
							continue
						}
					}
					if strings.HasPrefix(root, DiskTempOID) && strings.HasPrefix(candidate, DiskTempOID+".") {
						idx, _ := strconv.Atoi(strings.TrimPrefix(candidate, DiskTempOID+"."))
						if idx > options.diskCount {
							continue
						}
					}
					next = candidate
					typ = gosnmp.Integer
					break
				}
			}
			if strings.HasPrefix(root, DiskStatusOID) {
				value = 1
				if strings.HasSuffix(next, ".2") {
					value = 5
				}
			} else {
				value = 35
			}
			response.Variables = append(response.Variables, gosnmp.SnmpPDU{Name: next, Type: typ, Value: value})
		}
		packet, err := response.MarshalMsg()
		if err == nil {
			_, _ = conn.WriteToUDP(packet, addr)
		}
	}
}
