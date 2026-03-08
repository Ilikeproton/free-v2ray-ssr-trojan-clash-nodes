package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"daxionglink/geopb"

	"github.com/golang/protobuf/proto"
)

var defaultGeoSiteTags = []string{
	"geolocation-cn",
	"geolocation-!cn",
	"cn",
	"tld-cn",
	"tld-!cn",
}

func findFirstExisting(paths []string) (string, error) {
	for _, p := range paths {
		if p == "" {
			continue
		}
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		} else if err != nil && !os.IsNotExist(err) {
			return "", err
		}
	}
	return "", os.ErrNotExist
}

type geoIPEntry struct {
	code string
	nets []*net.IPNet
}

func (e *geoIPEntry) contains(ip net.IP) bool {
	for _, n := range e.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

type geoIPDB struct {
	private *geoIPEntry
	country []*geoIPEntry
	other   []*geoIPEntry
}

func loadGeoIPDB(paths []string) (*geoIPDB, string, error) {
	path, err := findFirstExisting(paths)
	if err != nil {
		return nil, "", err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}

	var list geopb.GeoIPList
	if err := proto.Unmarshal(data, &list); err != nil {
		return nil, "", fmt.Errorf("parse geoip.dat failed: %w", err)
	}

	db := &geoIPDB{}
	for _, entry := range list.GetEntry() {
		code := strings.ToLower(strings.TrimSpace(entry.GetCountryCode()))
		if code == "" {
			code = strings.ToLower(strings.TrimSpace(entry.GetCode()))
		}
		if code == "" {
			continue
		}
		nets := make([]*net.IPNet, 0, len(entry.GetCidr()))
		for _, cidr := range entry.GetCidr() {
			ip := net.IP(cidr.GetIp())
			if ip == nil {
				continue
			}
			bits := 128
			if ip.To4() != nil {
				ip = ip.To4()
				bits = 32
			}
			mask := net.CIDRMask(int(cidr.GetPrefix()), bits)
			nets = append(nets, &net.IPNet{IP: ip.Mask(mask), Mask: mask})
		}
		if len(nets) == 0 {
			continue
		}
		e := &geoIPEntry{code: code, nets: nets}
		if code == "private" {
			db.private = e
			continue
		}
		if len(code) == 2 {
			db.country = append(db.country, e)
		} else {
			db.other = append(db.other, e)
		}
	}

	return db, path, nil
}

func (db *geoIPDB) match(ip net.IP) string {
	if db == nil || ip == nil {
		return ""
	}
	if db.private != nil && db.private.contains(ip) {
		return "private"
	}
	for _, e := range db.country {
		if e.contains(ip) {
			return e.code
		}
	}
	for _, e := range db.other {
		if e.contains(ip) {
			return e.code
		}
	}
	return ""
}

type domainRule struct {
	typ   geopb.Domain_Type
	value string
	re    *regexp.Regexp
}

type domainMatcher struct {
	rules []domainRule
}

func (m *domainMatcher) match(host string) bool {
	if m == nil || host == "" {
		return false
	}
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	for _, r := range m.rules {
		switch r.typ {
		case geopb.Domain_Plain:
			if strings.Contains(host, r.value) {
				return true
			}
		case geopb.Domain_Full:
			if host == r.value {
				return true
			}
		case geopb.Domain_RootDomain:
			if host == r.value || strings.HasSuffix(host, "."+r.value) {
				return true
			}
		case geopb.Domain_Regex:
			if r.re != nil && r.re.MatchString(host) {
				return true
			}
		}
	}
	return false
}

type geoSiteDB struct {
	order    []string
	matchers map[string]*domainMatcher
}

func loadGeoSiteDB(paths []string, tags []string) (*geoSiteDB, string, error) {
	path, err := findFirstExisting(paths)
	if err != nil {
		return nil, "", err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}

	var list geopb.GeoSiteList
	if err := proto.Unmarshal(data, &list); err != nil {
		return nil, "", fmt.Errorf("parse geosite.dat failed: %w", err)
	}

	tagSet := make(map[string]struct{}, len(tags))
	order := make([]string, 0, len(tags))
	for _, t := range tags {
		tt := strings.ToLower(strings.TrimSpace(t))
		if tt == "" {
			continue
		}
		if _, ok := tagSet[tt]; ok {
			continue
		}
		tagSet[tt] = struct{}{}
		order = append(order, tt)
	}

	db := &geoSiteDB{
		order:    order,
		matchers: make(map[string]*domainMatcher),
	}

	for _, site := range list.GetEntry() {
		code := strings.ToLower(strings.TrimSpace(site.GetCountryCode()))
		if code == "" {
			code = strings.ToLower(strings.TrimSpace(site.GetCode()))
		}
		if _, ok := tagSet[code]; !ok {
			continue
		}
		m := &domainMatcher{}
		for _, d := range site.GetDomain() {
			val := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d.GetValue()), "."))
			if val == "" {
				continue
			}
			rule := domainRule{typ: d.GetType(), value: val}
			if d.GetType() == geopb.Domain_Regex {
				re, err := regexp.Compile(val)
				if err != nil {
					continue
				}
				rule.re = re
			}
			m.rules = append(m.rules, rule)
		}
		if len(m.rules) > 0 {
			db.matchers[code] = m
		}
	}

	return db, path, nil
}

func (db *geoSiteDB) match(host string) string {
	if db == nil || host == "" {
		return ""
	}
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	for _, tag := range db.order {
		m := db.matchers[tag]
		if m != nil && m.match(host) {
			return tag
		}
	}
	return ""
}

func defaultGeoPaths(exeDir string, name string) []string {
	return []string{
		filepath.Join(exeDir, name),
		filepath.Join(".", name),
	}
}
