package geopb

import "github.com/golang/protobuf/proto"

type GeoIPList struct {
	Entry []*GeoIP `protobuf:"bytes,1,rep,name=entry"`
}

func (m *GeoIPList) Reset()         { *m = GeoIPList{} }
func (m *GeoIPList) String() string { return proto.CompactTextString(m) }
func (*GeoIPList) ProtoMessage()    {}

func (m *GeoIPList) GetEntry() []*GeoIP {
	if m != nil {
		return m.Entry
	}
	return nil
}

type GeoIP struct {
	CountryCode string  `protobuf:"bytes,1,opt,name=country_code,json=countryCode"`
	Cidr        []*CIDR `protobuf:"bytes,2,rep,name=cidr"`
	Code        string  `protobuf:"bytes,5,opt,name=code"`
}

func (m *GeoIP) Reset()         { *m = GeoIP{} }
func (m *GeoIP) String() string { return proto.CompactTextString(m) }
func (*GeoIP) ProtoMessage()    {}

func (m *GeoIP) GetCountryCode() string {
	if m != nil {
		return m.CountryCode
	}
	return ""
}

func (m *GeoIP) GetCidr() []*CIDR {
	if m != nil {
		return m.Cidr
	}
	return nil
}

func (m *GeoIP) GetCode() string {
	if m != nil {
		return m.Code
	}
	return ""
}

type CIDR struct {
	Ip     []byte `protobuf:"bytes,1,opt,name=ip"`
	Prefix uint32 `protobuf:"varint,2,opt,name=prefix"`
}

func (m *CIDR) Reset()         { *m = CIDR{} }
func (m *CIDR) String() string { return proto.CompactTextString(m) }
func (*CIDR) ProtoMessage()    {}

func (m *CIDR) GetIp() []byte {
	if m != nil {
		return m.Ip
	}
	return nil
}

func (m *CIDR) GetPrefix() uint32 {
	if m != nil {
		return m.Prefix
	}
	return 0
}

type GeoSiteList struct {
	Entry []*GeoSite `protobuf:"bytes,1,rep,name=entry"`
}

func (m *GeoSiteList) Reset()         { *m = GeoSiteList{} }
func (m *GeoSiteList) String() string { return proto.CompactTextString(m) }
func (*GeoSiteList) ProtoMessage()    {}

func (m *GeoSiteList) GetEntry() []*GeoSite {
	if m != nil {
		return m.Entry
	}
	return nil
}

type GeoSite struct {
	CountryCode string    `protobuf:"bytes,1,opt,name=country_code,json=countryCode"`
	Domain      []*Domain `protobuf:"bytes,2,rep,name=domain"`
	Code        string    `protobuf:"bytes,4,opt,name=code"`
}

func (m *GeoSite) Reset()         { *m = GeoSite{} }
func (m *GeoSite) String() string { return proto.CompactTextString(m) }
func (*GeoSite) ProtoMessage()    {}

func (m *GeoSite) GetCountryCode() string {
	if m != nil {
		return m.CountryCode
	}
	return ""
}

func (m *GeoSite) GetDomain() []*Domain {
	if m != nil {
		return m.Domain
	}
	return nil
}

func (m *GeoSite) GetCode() string {
	if m != nil {
		return m.Code
	}
	return ""
}

type Domain_Type int32

const (
	Domain_Plain      Domain_Type = 0
	Domain_Regex      Domain_Type = 1
	Domain_RootDomain Domain_Type = 2
	Domain_Full       Domain_Type = 3
)

type Domain struct {
	Type  Domain_Type `protobuf:"varint,1,opt,name=type,enum=geopb.Domain_Type"`
	Value string      `protobuf:"bytes,2,opt,name=value"`
}

func (m *Domain) Reset()         { *m = Domain{} }
func (m *Domain) String() string { return proto.CompactTextString(m) }
func (*Domain) ProtoMessage()    {}

func (m *Domain) GetType() Domain_Type {
	if m != nil {
		return m.Type
	}
	return Domain_Plain
}

func (m *Domain) GetValue() string {
	if m != nil {
		return m.Value
	}
	return ""
}
