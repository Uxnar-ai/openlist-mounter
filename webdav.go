package main

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// normalizeURL 把用户随手输入的地址整理成规范的 WebDAV 端点。
func normalizeURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("请先填写 OpenList 的 WebDAV 地址")
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", errors.New("地址格式不正确，示例：http://192.168.1.10:5244/dav")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("只支持 http / https 地址")
	}
	if u.Host == "" {
		return "", errors.New("地址里缺少主机名或 IP")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if u.Path == "" {
		u.Path = "/dav"
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

type davProp struct {
	ResourceType struct {
		Collection *struct{} `xml:"collection"`
	} `xml:"resourcetype"`
	DisplayName   string `xml:"displayname"`
	ContentLength string `xml:"getcontentlength"`
}

type davEntry struct {
	Href     string `xml:"href"`
	Propstat []struct {
		Status string  `xml:"status"`
		Prop   davProp `xml:"prop"`
	} `xml:"propstat"`
}

type multiStatus struct {
	XMLName   xml.Name   `xml:"multistatus"`
	Responses []davEntry `xml:"response"`
}

func (e davEntry) isCollection() bool {
	for _, ps := range e.Propstat {
		if ps.Prop.ResourceType.Collection != nil {
			return true
		}
	}
	return false
}

func (e davEntry) displayName() string {
	for _, ps := range e.Propstat {
		if n := strings.TrimSpace(ps.Prop.DisplayName); n != "" {
			return n
		}
	}
	return ""
}

func doPropfind(client *http.Client, target, depth, user, pass string) (*multiStatus, error) {
	body := `<?xml version="1.0" encoding="utf-8"?>` +
		`<d:propfind xmlns:d="DAV:"><d:prop><d:resourcetype/><d:displayname/>` +
		`<d:getcontentlength/></d:prop></d:propfind>`

	req, err := http.NewRequest("PROPFIND", target, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Depth", depth)
	req.Header.Set("Content-Type", `application/xml; charset="utf-8"`)
	if user != "" || pass != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, errors.New("认证失败（401）：用户名或密码不正确；如果启用了二次验证，请改用 OpenList 的 WebDAV 专用密码")
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, errors.New("路径不存在（404）：确认地址以 /dav 结尾")
	}
	if resp.StatusCode == http.StatusMethodNotAllowed {
		return nil, errors.New("该地址不是 WebDAV 端点（405）")
	}
	if resp.StatusCode != http.StatusMultiStatus && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("服务器返回 %s", resp.Status)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var ms multiStatus
	if err := xml.Unmarshal(data, &ms); err != nil {
		return nil, errors.New("无法解析 WebDAV 响应，该地址可能不是标准 WebDAV 服务")
	}
	return &ms, nil
}

// listTopFolders 列出 WebDAV 根下一级的目录，也就是 OpenList 里的一个个存储。
func listTopFolders(base, user, pass string, timeout time.Duration) ([]string, error) {
	root := strings.TrimRight(base, "/") + "/"
	client := &http.Client{Timeout: timeout}
	ms, err := doPropfind(client, root, "1", user, pass)
	if err != nil {
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			return nil, fmt.Errorf("连接超时（%s）：确认地址和端口可达", timeout)
		}
		if strings.Contains(err.Error(), "connect") {
			return nil, fmt.Errorf("无法连接：%v", err)
		}
		return nil, err
	}

	basePath := ""
	if u, err := url.Parse(root); err == nil {
		basePath = strings.TrimRight(u.Path, "/")
	}

	seen := map[string]bool{}
	var out []string
	for _, e := range ms.Responses {
		if !e.isCollection() {
			continue
		}
		p := e.Href
		if u, err := url.Parse(p); err == nil && u.Path != "" {
			p = u.Path
		}
		p = strings.TrimRight(p, "/")
		if basePath != "" {
			if strings.EqualFold(p, basePath) {
				continue // 根自己
			}
			if strings.HasPrefix(p, basePath+"/") {
				p = p[len(basePath)+1:]
			}
		}
		p = strings.TrimPrefix(p, "/")
		if p == "" || strings.Contains(p, "/") {
			continue // 只要一级
		}
		name := p
		if decoded, err := url.PathUnescape(p); err == nil {
			name = decoded
		}
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil, errors.New("该 WebDAV 根下没有子目录，或者账号无权限读取")
	}
	return out, nil
}

// testConnection 供管理页面做诊断用。
func testConnection(base, user, pass string, timeout time.Duration) (string, error) {
	root := strings.TrimRight(base, "/") + "/"
	client := &http.Client{Timeout: timeout}
	ms, err := doPropfind(client, root, "0", user, pass)
	if err != nil {
		return "", err
	}
	if len(ms.Responses) == 0 {
		return "", errors.New("服务器响应为空")
	}
	if !ms.Responses[0].isCollection() {
		return "", errors.New("该路径不是一个目录集合")
	}
	return fmt.Sprintf("连接正常：%s", ms.Responses[0].Href), nil
}
