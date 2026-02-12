package geoip

import (
	"fmt"
	"net"

	"github.com/oschwald/maxminddb-golang"
)

// MMDBReader provides local GeoLite2 lookups from MMDB files.
type MMDBReader struct {
	asnDB  *maxminddb.Reader
	cityDB *maxminddb.Reader
}

// asnRecord matches the GeoLite2-ASN MMDB schema.
type asnRecord struct {
	AutonomousSystemNumber       uint   `maxminddb:"autonomous_system_number"`
	AutonomousSystemOrganization string `maxminddb:"autonomous_system_organization"`
}

// cityRecord matches a subset of the GeoLite2-City MMDB schema.
type cityRecord struct {
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
	City struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
	Subdivisions []struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"subdivisions"`
	Location struct {
		Latitude  float64 `maxminddb:"latitude"`
		Longitude float64 `maxminddb:"longitude"`
	} `maxminddb:"location"`
}

// NewMMDBReader opens GeoLite2 MMDB files. Either path can be empty to skip that database.
func NewMMDBReader(asnPath, cityPath string) (*MMDBReader, error) {
	r := &MMDBReader{}

	if asnPath != "" {
		db, err := maxminddb.Open(asnPath)
		if err != nil {
			return nil, fmt.Errorf("failed to open GeoLite2-ASN MMDB (%s): %w", asnPath, err)
		}
		r.asnDB = db
	}

	if cityPath != "" {
		db, err := maxminddb.Open(cityPath)
		if err != nil {
			if r.asnDB != nil {
				r.asnDB.Close()
			}
			return nil, fmt.Errorf("failed to open GeoLite2-City MMDB (%s): %w", cityPath, err)
		}
		r.cityDB = db
	}

	return r, nil
}

// LookupASN returns ASN number (as string like "AS15169") and organization name.
func (r *MMDBReader) LookupASN(ipStr string) (string, string, error) {
	if r.asnDB == nil {
		return "", "", fmt.Errorf("GeoLite2-ASN database not loaded")
	}

	ip := net.ParseIP(ipStr)
	if ip == nil {
		return "", "", fmt.Errorf("invalid IP address: %s", ipStr)
	}

	var record asnRecord
	if err := r.asnDB.Lookup(ip, &record); err != nil {
		return "", "", fmt.Errorf("ASN lookup failed for %s: %w", ipStr, err)
	}

	if record.AutonomousSystemNumber == 0 {
		return "", "", nil
	}

	asn := fmt.Sprintf("AS%d", record.AutonomousSystemNumber)
	return asn, record.AutonomousSystemOrganization, nil
}

// LookupCity returns geographic information for an IP address.
func (r *MMDBReader) LookupCity(ipStr string) (country, city, region string, lat, lon float64, err error) {
	if r.cityDB == nil {
		return "", "", "", 0, 0, fmt.Errorf("GeoLite2-City database not loaded")
	}

	ip := net.ParseIP(ipStr)
	if ip == nil {
		return "", "", "", 0, 0, fmt.Errorf("invalid IP address: %s", ipStr)
	}

	var record cityRecord
	if err := r.cityDB.Lookup(ip, &record); err != nil {
		return "", "", "", 0, 0, fmt.Errorf("city lookup failed for %s: %w", ipStr, err)
	}

	country = record.Country.ISOCode
	if names := record.City.Names; names != nil {
		city = names["en"]
	}
	if len(record.Subdivisions) > 0 {
		if names := record.Subdivisions[0].Names; names != nil {
			region = names["en"]
		}
	}
	lat = record.Location.Latitude
	lon = record.Location.Longitude

	return country, city, region, lat, lon, nil
}

// Close closes all open MMDB readers.
func (r *MMDBReader) Close() {
	if r.asnDB != nil {
		r.asnDB.Close()
	}
	if r.cityDB != nil {
		r.cityDB.Close()
	}
}
