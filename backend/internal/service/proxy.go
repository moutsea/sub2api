package service

import (
	"fmt"
	"time"
)

type Proxy struct {
	ID        int64
	Name      string
	Protocol  string
	Host      string
	Port      int
	Username  string
	Password  string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (p *Proxy) IsActive() bool {
	return p.Status == StatusActive
}

// 代理国家编码常量
// 990100 = 美国
// 后续可改为从配置或数据库字段读取
const defaultProxyCountryCode = "990100"

func (p *Proxy) URL() string {
	if p.Username != "" && p.Password != "" {
		// 格式: {protocol}://{username}:{password}:A{country_code}@{host}:{port}
		// 例如: socks5://authkey:authpwd:A990100@proxy.example.com:1080
		return fmt.Sprintf("%s://%s:%s:A%s@%s:%d", p.Protocol, p.Username, p.Password, defaultProxyCountryCode, p.Host, p.Port)
	}
	return fmt.Sprintf("%s://%s:%d", p.Protocol, p.Host, p.Port)
}

type ProxyWithAccountCount struct {
	Proxy
	AccountCount   int64
	LatencyMs      *int64
	LatencyStatus  string
	LatencyMessage string
	IPAddress      string
	Country        string
	CountryCode    string
	Region         string
	City           string
}

type ProxyAccountSummary struct {
	ID       int64
	Name     string
	Platform string
	Type     string
	Notes    *string
}
