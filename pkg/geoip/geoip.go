package geoip

import (
	"net/netip"
	"sync"

	"github.com/oschwald/geoip2-golang/v2"
	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/pkg/publicip"
)

// Region 由 IP 解析得到的地区信息（国家 / 城市 / 区县）。
type Region struct {
	Country string
	City    string
	County  string
}

var (
	mu     sync.RWMutex
	reader *geoip2.Reader
)

// Init 加载 MaxMind GeoIP2 数据库；dbPath 为空或加载失败时仅记录日志。
func Init(dbPath string) {
	if dbPath == "" {
		log.Warn("未指定 GeoIP2 数据库路径，将无法解析地理位置")
		return
	}
	db, err := geoip2.Open(dbPath)
	if err != nil {
		log.Warnf("无法加载 GeoIP2 数据库 %s: %v", dbPath, err)
		return
	}
	mu.Lock()
	reader = db
	mu.Unlock()
	log.Infof("成功加载 GeoIP2 数据库: %s", dbPath)
}

// Close 关闭 GeoIP2 数据库。
func Close() {
	mu.Lock()
	defer mu.Unlock()
	if reader != nil {
		reader.Close()
		reader = nil
	}
}

func getReader() *geoip2.Reader {
	mu.RLock()
	defer mu.RUnlock()
	return reader
}

// Lookup 根据 IP 解析国家、城市、区县；本地/私网 IP 会自动尝试公网 IP。
func Lookup(ip string) Region {
	record, _ := lookupCityRecord(ip)
	if record == nil || !record.HasData() {
		return Region{}
	}
	return regionFromRecord(record)
}

// LookupLocation 返回完整位置信息（供会话/在线历史等使用）；本地 IP 会自动尝试公网 IP。
func LookupLocation(ip string) entity.LocationInfoJSON {
	record, resolvedIP := lookupCityRecord(ip)
	if record == nil || !record.HasData() {
		return entity.LocationInfoJSON{IP: ip}
	}
	loc := locationFromRecord(record)
	loc.IP = resolvedIP
	return loc
}

func lookupCityRecord(rawIP string) (*geoip2.City, string) {
	if rawIP == "" {
		return nil, rawIP
	}
	r := getReader()
	if r == nil {
		return nil, rawIP
	}

	ipAddr, err := netip.ParseAddr(rawIP)
	if err != nil {
		log.Warnf("无效的IP地址: %s", rawIP)
		return nil, rawIP
	}

	record, err := r.City(ipAddr)
	if err == nil && record.HasData() {
		return record, rawIP
	}

	if ipAddr.Is6() {
		ipv4Addr := publicip.ConvertIPv6ToIPv4(ipAddr)
		if ipv4Addr.IsValid() {
			record, err = r.City(ipv4Addr)
			if err == nil && record.HasData() {
				return record, ipv4Addr.String()
			}
			ipAddr = ipv4Addr
		}
	}

	if publicip.IsLocal(ipAddr) {
		publicIP, err := publicip.Get()
		if err != nil {
			log.Warnf("本地地址 %s 获取公网IP失败: %v", rawIP, err)
			return nil, rawIP
		}
		publicAddr, err := netip.ParseAddr(publicIP)
		if err != nil {
			return nil, rawIP
		}
		record, err = r.City(publicAddr)
		if err == nil && record.HasData() {
			log.Debugf("本地地址 %s 使用公网IP %s 解析地理位置", rawIP, publicIP)
			return record, publicIP
		}
	}

	return nil, rawIP
}

func regionFromRecord(record *geoip2.City) Region {
	return Region{
		Country: pickName(record.Country.Names.SimplifiedChinese, record.Country.Names.English),
		City:    pickName(record.City.Names.SimplifiedChinese, record.City.Names.English),
		County:  countyFromRecord(record),
	}
}

func locationFromRecord(record *geoip2.City) entity.LocationInfoJSON {
	country := pickName(record.Country.Names.SimplifiedChinese, record.Country.Names.English)
	countryEn := record.Country.Names.English
	if countryEn == "" && record.Country.Names.HasData() {
		countryEn = firstNonEmpty(
			record.Country.Names.German,
			record.Country.Names.French,
			record.Country.Names.Spanish,
		)
	}

	region := ""
	regionEn := ""
	if len(record.Subdivisions) > 0 {
		sub := record.Subdivisions[0]
		region = pickName(sub.Names.SimplifiedChinese, sub.Names.English)
		regionEn = sub.Names.English
		if regionEn == "" && sub.Names.HasData() {
			regionEn = firstNonEmpty(sub.Names.German, sub.Names.French)
		}
	}

	city := pickName(record.City.Names.SimplifiedChinese, record.City.Names.English)
	cityEn := record.City.Names.English
	if cityEn == "" && record.City.Names.HasData() {
		cityEn = firstNonEmpty(record.City.Names.German, record.City.Names.French)
	}

	latitude, longitude := 0.0, 0.0
	if record.Location.HasCoordinates() {
		if record.Location.Latitude != nil {
			latitude = *record.Location.Latitude
		}
		if record.Location.Longitude != nil {
			longitude = *record.Location.Longitude
		}
	}

	return entity.LocationInfoJSON{
		Country:   country,
		CountryEn: countryEn,
		Region:    region,
		RegionEn:  regionEn,
		City:      city,
		CityEn:    cityEn,
		Latitude:  latitude,
		Longitude: longitude,
		Timezone:  record.Location.TimeZone,
	}
}

func pickName(zh, en string) string {
	if zh != "" {
		return zh
	}
	return en
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func countyFromRecord(record *geoip2.City) string {
	if len(record.Subdivisions) < 2 {
		return ""
	}
	sub := record.Subdivisions[1]
	return pickName(sub.Names.SimplifiedChinese, sub.Names.English)
}
