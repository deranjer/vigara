// Package inventory reconciles independent observations without conflating a
// device with an agent credential, discovery source, or network address.
package inventory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/deranjer/vigara/internal/protocol"
	"github.com/jackc/pgx/v5"
)

var ErrAmbiguousIdentity = errors.New("device identifiers point to different devices")

type Address struct {
	Value      string `json:"value"`
	Type       string `json:"type"`
	Provenance string `json:"provenance"`
}
type Observation struct {
	DisplayName string
	Identifiers []protocol.DeviceIdentifier
	Addresses   []Address
	Provenance  string
	SourceType  string
	SourceID    string
}
type identifier struct{ kind, normalized, reported string }

// ResolveDevice selects one device using stable identifiers, or creates one.
// Host names and addresses are deliberately never considered identifiers.
func ResolveDevice(ctx context.Context, tx pgx.Tx, o Observation) (string, error) {
	if strings.TrimSpace(o.DisplayName) == "" || len(o.DisplayName) > 120 || len(o.Provenance) == 0 || len(o.Provenance) > 128 {
		return "", errors.New("invalid device observation")
	}
	keys, err := normalize(o.Identifiers)
	if err != nil {
		return "", err
	}
	// Lock the complete key set in sorted order to serialize overlapping reports.
	for _, key := range keys {
		digest := sha256.Sum256([]byte(key.kind + "\x00" + key.normalized))
		var lockID int64
		for _, b := range digest[:8] {
			lockID = (lockID << 8) | int64(b)
		}
		var locked string
		if err = tx.QueryRow(ctx, `SELECT pg_advisory_xact_lock($1)::text`, lockID).Scan(&locked); err != nil {
			return "", err
		}
	}
	deviceIDs := map[string]bool{}
	for _, key := range keys {
		var id string
		err = tx.QueryRow(ctx, `SELECT device_id::text FROM device_identifiers WHERE kind=$1 AND normalized_value=$2`, key.kind, key.normalized).Scan(&id)
		if err == nil {
			deviceIDs[id] = true
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
	}
	if len(deviceIDs) > 1 {
		return "", ErrAmbiguousIdentity
	}
	var deviceID string
	for id := range deviceIDs {
		deviceID = id
	}
	if deviceID == "" {
		if o.SourceType != "" && o.SourceID != "" {
			err = tx.QueryRow(ctx, `SELECT device_id::text FROM device_sources WHERE source_type=$1 AND source_id=$2`, o.SourceType, o.SourceID).Scan(&deviceID)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return "", err
			}
		}
	}
	if deviceID == "" {
		if err = tx.QueryRow(ctx, `INSERT INTO devices(display_name) VALUES($1) RETURNING id::text`, o.DisplayName).Scan(&deviceID); err != nil {
			return "", err
		}
	} else if _, err = tx.Exec(ctx, `UPDATE devices SET display_name=$1,last_seen_at=now() WHERE id=$2`, o.DisplayName, deviceID); err != nil {
		return "", err
	}
	for _, key := range keys {
		if _, err = tx.Exec(ctx, `INSERT INTO device_identifiers(device_id,kind,normalized_value,reported_value,provenance) VALUES($1,$2,$3,$4,$5) ON CONFLICT(kind,normalized_value) DO NOTHING`, deviceID, key.kind, key.normalized, key.reported, o.Provenance); err != nil {
			return "", err
		}
		var owner, identifierID string
		if err = tx.QueryRow(ctx, `SELECT id::text,device_id::text FROM device_identifiers WHERE kind=$1 AND normalized_value=$2`, key.kind, key.normalized).Scan(&identifierID, &owner); err != nil {
			return "", err
		}
		if owner != deviceID {
			return "", ErrAmbiguousIdentity
		}
		if _, err = tx.Exec(ctx, `INSERT INTO device_identifier_sources(identifier_id,provenance,reported_value) VALUES($1,$2,$3) ON CONFLICT(identifier_id,provenance) DO UPDATE SET last_seen_at=now(),reported_value=EXCLUDED.reported_value`, identifierID, o.Provenance, key.reported); err != nil {
			return "", err
		}
	}
	for _, address := range o.Addresses {
		parsed, parseErr := netip.ParseAddr(strings.TrimSpace(address.Value))
		if parseErr != nil || address.Type == "" || len(address.Type) > 32 || address.Provenance == "" || len(address.Provenance) > 128 {
			return "", fmt.Errorf("invalid address observation")
		}
		if _, err = tx.Exec(ctx, `INSERT INTO device_addresses(device_id,address,address_type,provenance) VALUES($1,$2,$3,$4) ON CONFLICT(device_id,address,address_type,provenance) DO UPDATE SET last_seen_at=now()`, deviceID, parsed.String(), address.Type, address.Provenance); err != nil {
			return "", err
		}
	}
	return deviceID, nil
}

// AttachSource records which independent source observed a device. If a source
// key was already attached elsewhere, its ownership remains unchanged.
func AttachSource(ctx context.Context, tx pgx.Tx, deviceID, sourceType, sourceID, provenance string) error {
	if deviceID == "" || sourceType == "" || sourceID == "" || provenance == "" || len(sourceType) > 32 || len(sourceID) > 256 || len(provenance) > 128 {
		return errors.New("invalid device source")
	}
	command, err := tx.Exec(ctx, `INSERT INTO device_sources(device_id,source_type,source_id,provenance) VALUES($1,$2,$3,$4) ON CONFLICT(source_type,source_id) DO UPDATE SET last_seen_at=now() WHERE device_sources.device_id=EXCLUDED.device_id`, deviceID, sourceType, sourceID, provenance)
	if err == nil && command.RowsAffected() == 0 {
		return ErrAmbiguousIdentity
	}
	return err
}

func normalize(input []protocol.DeviceIdentifier) ([]identifier, error) {
	if len(input) > 32 {
		return nil, errors.New("too many device identifiers")
	}
	out := make([]identifier, 0, len(input))
	seen := map[string]bool{}
	for _, item := range input {
		kind := strings.ToLower(strings.TrimSpace(item.Kind))
		value := strings.TrimSpace(item.Value)
		switch kind {
		case "machine-id", "system-uuid":
		default:
			return nil, errors.New("unsupported device identifier kind")
		}
		if value == "" || len(value) > 256 {
			return nil, errors.New("invalid device identifier")
		}
		normalized := strings.ToLower(strings.Trim(value, "{}"))
		compact := strings.ReplaceAll(normalized, "-", "")
		if len(compact) != 32 && len(compact) != 64 {
			return nil, errors.New("invalid UUID device identifier")
		}
		if _, err := hex.DecodeString(compact); err != nil {
			return nil, errors.New("invalid UUID device identifier")
		}
		normalized = compact
		if kind == "system-uuid" && len(compact) == 32 {
			normalized = compact[:8] + "-" + compact[8:12] + "-" + compact[12:16] + "-" + compact[16:20] + "-" + compact[20:]
		}
		compound := kind + "\x00" + normalized
		sentinel := normalized == "unknown" || normalized == "none" || normalized == "default string" || normalized == "to be filled by o.e.m." || normalized == "system serial number" || normalized == "not specified"
		compactSentinel := strings.NewReplacer("-", "", "{", "", "}", "", " ", "").Replace(normalized)
		if sentinel || strings.Trim(compactSentinel, "0") == "" || strings.Trim(compactSentinel, "f") == "" {
			return nil, errors.New("placeholder device identifier")
		}
		if seen[compound] {
			return nil, errors.New("duplicate device identifier")
		}
		seen[compound] = true
		out = append(out, identifier{kind, normalized, value})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].kind+"\x00"+out[i].normalized < out[j].kind+"\x00"+out[j].normalized
	})
	return out, nil
}
