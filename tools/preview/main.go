// preview 用假数据把 web/index.html 渲染出来，仅用于开发期截图检查。
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
)

func main() {
	html, err := os.ReadFile("web/index.html")
	if err != nil {
		log.Fatal(err)
	}

	state := map[string]any{
		"url":      "http://192.168.1.10:5244/dav",
		"user":     "admin",
		"has_pass": true,
		"folders": []map[string]any{
			{"name": "影片", "drive": "Z:", "enabled": true, "mounted": true, "status": "已挂载", "backend": "rclone", "capacity": "4T"},
			{"name": "剧集", "drive": "Y:", "enabled": true, "mounted": true, "status": "已挂载", "backend": "rclone", "capacity": "2T"},
			{"name": "音乐", "drive": "X:", "enabled": true, "mounted": false, "status": "连接中…", "backend": "rclone", "capacity": ""},
			{"name": "文档资料", "drive": "W:", "enabled": true, "mounted": false, "status": "未挂载", "backend": "webdav", "capacity": "500G"},
			{"name": "手机备份", "drive": "V:", "enabled": false, "mounted": false, "status": "用户名或密码错误", "backend": "webdav", "capacity": ""},
		},
		"free_drives":  []string{"A:", "B:", "D:", "E:", "F:"},
		"elevated":     true,
		"webclient":    true,
		"rclone_ready": true,
		"winfsp_ready": true,
		"backend":      "rclone",
		"auto_start":  true,
		"port":        18234,
		"logs": []string{
			"21:02:11  启动恢复：http://192.168.1.10:5244/dav",
			"21:02:11  扫描到 5 个目录：影片、剧集、音乐、文档资料、手机备份",
			"21:02:12  挂载 影片 → Z:",
			"21:02:13  挂载 剧集 → Y:",
			"21:02:13  挂载 音乐 → X:",
			"21:02:14    网络错误（代码 59）",
			"21:02:14  手机备份 挂载失败：认证失败（401）",
			"21:02:14  挂载完成：4/5",
		},
	}

	http.HandleFunc("/dav/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
		w.WriteHeader(http.StatusMultiStatus)
		w.Write([]byte(davXML))
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/state" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			json.NewEncoder(w).Encode(state)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(html)
	})
	log.Println("preview on :18297")
	log.Fatal(http.ListenAndServe("127.0.0.1:18297", nil))
}

const davXML = `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
<D:response><D:href>/dav/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype><D:displayname>/</D:displayname></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
<D:response><D:href>/dav/影片/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype><D:displayname>影片</D:displayname></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
<D:response><D:href>/dav/剧集/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype><D:displayname>剧集</D:displayname></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
<D:response><D:href>/dav/音乐/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype><D:displayname>音乐</D:displayname></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
<D:response><D:href>/dav/文档资料/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype><D:displayname>文档资料</D:displayname></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
<D:response><D:href>/dav/readme.txt</D:href><D:propstat><D:prop><D:resourcetype/><D:displayname>readme.txt</D:displayname></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
</D:multistatus>`
