package oss

import (
	"errors"
	"net/url"
	"strings"
)

// URL 校验错误。
var (
	// ErrURLInvalid URL 格式非法。
	ErrURLInvalid = errors.New("oss: URL 非法")
	// ErrURLHostNotAllowed URL 的 host 不在白名单内。
	ErrURLHostNotAllowed = errors.New("oss: URL host 不在白名单内")
	// ErrURLPrefixNotMatch URL 的路径前缀不匹配。
	ErrURLPrefixNotMatch = errors.New("oss: URL 路径前缀不匹配")
	// ErrURLExtNotAllowed URL 的后缀不在白名单内。
	ErrURLExtNotAllowed = errors.New("oss: URL 后缀不在白名单内")
)

// URLRules URL 白名单规则。
type URLRules struct {
	// AllowedHosts 允许的 host（不含 scheme 和端口），小写。
	// 留空表示只允许 bucket 默认域名。
	AllowedHosts []string
	// AllowedPrefixes 允许的路径前缀，如 "avatar/abc123/"。
	// 前缀会归一化成带首尾斜杠的形式。
	AllowedPrefixes []string
	// AllowedExts 允许的后缀白名单。
	AllowedExts []string
}

// ValidateURL 校验一个 URL 是不是本系统签发过的对象地址。
// raw 待校验的 URL
// rules 白名单规则
//
// 校验通过返回去掉 query 和 fragment 的干净 URL。
// 剥掉 query 是为了不把签名参数、追踪参数写进库里。
func (c *Client) ValidateURL(raw string, rules URLRules) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrURLInvalid
	}
	// 后缀白名单为空时一律拒绝 —— 不做「空 = 放行」。
	if len(rules.AllowedExts) == 0 {
		return "", ErrURLExtNotAllowed
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", ErrURLInvalid
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", ErrURLInvalid
	}
	if !c.hostAllowed(u.Host, rules.AllowedHosts) {
		return "", ErrURLHostNotAllowed
	}
	if u.Path == "" || u.Path == "/" {
		return "", ErrURLInvalid
	}
	if !prefixAllowed(u.Path, rules.AllowedPrefixes) {
		return "", ErrURLPrefixNotMatch
	}
	if !extAllowed(u.Path, rules.AllowedExts) {
		return "", ErrURLExtNotAllowed
	}

	// 剥掉 query 和 fragment 再返回。
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// hostAllowed 判断 host 在不在白名单内。
func (c *Client) hostAllowed(host string, allowed []string) bool {
	h := strings.ToLower(host)
	// 去掉端口。
	if i := strings.Index(h, ":"); i >= 0 {
		h = h[:i]
	}
	for _, a := range allowed {
		if strings.ToLower(strings.TrimSpace(a)) == h {
			return true
		}
	}
	// bucket 默认域名。
	if c.cfg.Bucket != "" && c.cfg.Endpoint != "" {
		if h == strings.ToLower(c.cfg.Bucket+"."+c.cfg.Endpoint) {
			return true
		}
	}
	return false
}

// prefixAllowed 判断路径前缀匹不匹配。
func prefixAllowed(path string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	for _, p := range prefixes {
		if strings.HasPrefix(path, normalizePrefix(p)) {
			return true
		}
	}
	return false
}

// normalizePrefix 归一化路径前缀：补上首尾斜杠。
func normalizePrefix(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

// extAllowed 判断路径后缀在不在白名单内。
func extAllowed(path string, allowed []string) bool {
	ext := NormalizeExt(pathExt(path))
	if ext == "" {
		return false
	}
	for _, a := range allowed {
		if NormalizeExt(a) == ext {
			return true
		}
	}
	return false
}

// pathExt 取路径的后缀（不含点）。
func pathExt(path string) string {
	if i := strings.LastIndex(path, "."); i >= 0 {
		return path[i+1:]
	}
	return ""
}
