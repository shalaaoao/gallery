package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"html/template"
	"log"
	"math"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/disintegration/imaging"
)

// ================= 配置区 =================
const (
	PhotoRoot    = "/mnt/Fanxiang2T/canon/"
	Port         = ":8092"
	PageSize     = 20
	CertFile     = "/home/wwwroot/dnmp_linux/services/nginx/ssl/julyaoao.top/cert.crt"
	KeyFile      = "/home/wwwroot/dnmp_linux/services/nginx/ssl/julyaoao.top/cert.key"
	ThumbMaxSize = 600               // 缩略图最大边长（像素）
	JPEGQuality  = 82                // 缩略图 JPEG 质量（1-100）
)

// =========================================

type ApiResponse struct {
	Photos  []string `json:"photos"`
	HasNext bool     `json:"hasNext"`
	Page    int      `json:"page"`
}

type PageData struct {
	CurrentPath string
	ParentPath  string
	Folders     []string
	Photos      []string
	HasNext     bool
}

type photoWithTime struct {
	name    string
	modTime time.Time
}

func main() {
	if !fileExists(CertFile) || !fileExists(KeyFile) {
		fmt.Println("🔒 正在生成 TLS 证书...")
		if err := generateCert(CertFile, KeyFile); err != nil {
			log.Fatalf("生成证书失败: %v", err)
		}
	}

	fs := http.FileServer(http.Dir(PhotoRoot))
	http.Handle("/raw/", http.StripPrefix("/raw/", fs))
	http.HandleFunc("/thumb/", handleThumbnail)

	http.HandleFunc("/sw.js", handleServiceWorker)
	http.HandleFunc("/", handleGallery)

	fmt.Printf("\n🚀 Canon Gallery 相册 (v2.3)\n")
	fmt.Printf("👉 请访问: https://192.168.100.15%s\n", Port)

	err := http.ListenAndServeTLS(Port, CertFile, KeyFile, nil)
	if err != nil {
		log.Fatal(err)
	}
}

func handleGallery(w http.ResponseWriter, r *http.Request) {
	if r.TLS != nil {
		w.Header().Set("Protocol", "HTTP/2.0")
	}
	// 防止浏览器缓存旧版 HTML（确保 /thumb/ 改动生效）
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

	relPath := r.URL.Query().Get("path")
	pageStr := r.URL.Query().Get("page")
	format := r.URL.Query().Get("format")

	page, err := strconv.Atoi(pageStr)
	if err != nil || page < 1 {
		page = 1
	}

	if strings.Contains(relPath, "..") {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	fullPath := filepath.Join(PhotoRoot, relPath)
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		http.Error(w, "Directory not found", http.StatusNotFound)
		return
	}

	var folders []string
	var photosWithTime []photoWithTime

	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if entry.IsDir() {
			folders = append(folders, name)
		} else {
			lowerName := strings.ToLower(name)
			if strings.HasSuffix(lowerName, ".jpg") ||
				strings.HasSuffix(lowerName, ".jpeg") ||
				strings.HasSuffix(lowerName, ".png") ||
				strings.HasSuffix(lowerName, ".gif") {
				// 获取文件的修改时间
				info, err := entry.Info()
				if err != nil {
					continue
				}
				photosWithTime = append(photosWithTime, photoWithTime{
					name:    name,
					modTime: info.ModTime(),
				})
			}
		}
	}

	sort.Sort(sort.Reverse(sort.StringSlice(folders)))

	// 按照修改时间倒序排序（最新的在前）
	sort.Slice(photosWithTime, func(i, j int) bool {
		return photosWithTime[i].modTime.After(photosWithTime[j].modTime)
	})

	// 提取排序后的文件名列表
	var allPhotos []string
	for _, p := range photosWithTime {
		allPhotos = append(allPhotos, p.name)
	}

	totalPhotos := len(allPhotos)
	totalPages := int(math.Ceil(float64(totalPhotos) / float64(PageSize)))
	if page > totalPages && totalPages > 0 {
		page = totalPages
	}

	start := (page - 1) * PageSize
	end := start + PageSize
	if start < 0 {
		start = 0
	}
	if end > totalPhotos {
		end = totalPhotos
	}

	var displayPhotos []string
	if totalPhotos > 0 {
		displayPhotos = allPhotos[start:end]
	}

	hasNext := page < totalPages

	if format == "json" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ApiResponse{
			Photos:  displayPhotos,
			HasNext: hasNext,
			Page:    page,
		})
		return
	}

	parentPath := ""
	if relPath != "" {
		parentPath = filepath.Dir(relPath)
		if parentPath == "." {
			parentPath = ""
		}
	}

	data := PageData{
		CurrentPath: relPath,
		ParentPath:  parentPath,
		Folders:     folders,
		Photos:      displayPhotos,
		HasNext:     hasNext,
	}

	tmpl, err := template.New("index").Parse(htmlTemplate)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tmpl.Execute(w, data)
}

// handleThumbnail 生成并缓存缩略图，首次访问时从原图生成，之后直接返回缓存。
func handleThumbnail(w http.ResponseWriter, r *http.Request) {
	// 从 URL 中提取原图的相对路径: /thumb/path/to/photo.jpg -> path/to/photo.jpg
	relPath := strings.TrimPrefix(r.URL.Path, "/thumb/")

	log.Printf("📷 /thumb/ 请求: %s", relPath)

	if strings.Contains(relPath, "..") {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	origPath := filepath.Join(PhotoRoot, relPath)
	if _, err := os.Stat(origPath); os.IsNotExist(err) {
		log.Printf("❌ 原图不存在: %s", origPath)
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	// 缩略图缓存路径: PhotoRoot/.thumbnails/ + 相对路径
	thumbPath := filepath.Join(PhotoRoot, ".thumbnails", relPath)

	// 如果缓存已存在，直接返回
	if info, err := os.Stat(thumbPath); err == nil && !info.IsDir() {
		log.Printf("✅ 缓存命中: %s", relPath)
		w.Header().Set("Cache-Control", "public, max-age=604800, immutable")
		http.ServeFile(w, r, thumbPath)
		return
	}

	// 确保缓存目录存在
	thumbDir := filepath.Dir(thumbPath)
	if err := os.MkdirAll(thumbDir, 0755); err != nil {
		log.Printf("❌ 创建缓存目录失败: %s (err: %v)", thumbDir, err)
		// 无法创建缓存目录，回退到原图
		http.ServeFile(w, r, origPath)
		return
	}

	// 解码原图并生成缩略图（imaging 自动处理 EXIF 方向）
	log.Printf("🔧 正在生成缩略图: %s", relPath)
	img, err := imaging.Open(origPath, imaging.AutoOrientation(true))
	if err != nil {
		log.Printf("❌ 解码失败: %s (err: %v)", origPath, err)
		// 解码失败，回退到原图
		http.ServeFile(w, r, origPath)
		return
	}

	// 等比缩放，适应 ThumbMaxSize 范围
	thumb := imaging.Fit(img, ThumbMaxSize, ThumbMaxSize, imaging.Lanczos)

	// 先写到临时文件，再原子重命名，避免并发写入问题
	// 注意: 临时文件必须是 .jpg 后缀，否则 imaging.Save 无法识别格式
	ext := filepath.Ext(thumbPath) // 如 .JPG
	tmpPath := strings.TrimSuffix(thumbPath, ext) + ".tmp" + ext
	if err := imaging.Save(thumb, tmpPath, imaging.JPEGQuality(JPEGQuality)); err != nil {
		log.Printf("❌ 保存缩略图失败: %s (err: %v)", tmpPath, err)
		http.ServeFile(w, r, origPath)
		return
	}
	if err := os.Rename(tmpPath, thumbPath); err != nil {
		log.Printf("⚠️  重命名失败 (可能并发): %s -> %s (err: %v)", tmpPath, thumbPath, err)
		// 重命名失败（可能被其他 goroutine 抢先创建），直接用临时文件响应
		http.ServeFile(w, r, tmpPath)
		return
	}

	log.Printf("✅ 缩略图生成完毕: %s", relPath)
	w.Header().Set("Cache-Control", "public, max-age=604800, immutable")
	http.ServeFile(w, r, thumbPath)
}

func handleServiceWorker(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write([]byte(serviceWorkerScript))
}

func fileExists(filename string) bool {
	info, err := os.Stat(filename)
	return !os.IsNotExist(err) && !info.IsDir()
}

func generateCert(certPath, keyPath string) error {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{Organization: []string{"My Photo Gallery"}},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	addrs, _ := net.InterfaceAddrs()
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok {
			if ip := ipnet.IP; ip != nil {
				template.IPAddresses = append(template.IPAddresses, ip)
			}
		}
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return err
	}
	certOut, _ := os.Create(certPath)
	pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	certOut.Close()
	keyOut, _ := os.Create(keyPath)
	b, _ := x509.MarshalECPrivateKey(priv)
	pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: b})
	keyOut.Close()
	return nil
}

// ---------------- Service Worker 脚本 ----------------
// 轻量 Service Worker：不缓存任何内容，仅用于激活后快速清理旧缓存
const serviceWorkerScript = `
self.addEventListener('install', (event) => {
    self.skipWaiting();
});

self.addEventListener('activate', (event) => {
    event.waitUntil(
        Promise.all([
            // 删除所有旧版本缓存，释放空间
            caches.keys().then((cacheNames) => {
                return Promise.all(
                    cacheNames.map((cacheName) => caches.delete(cacheName))
                );
            }),
            self.clients.claim()
        ])
    );
});
`

// ---------------- HTML 模板 ----------------
// 注意：这里的 JS 代码已去除反引号，改为单引号拼接，以兼容 Go 的字符串语法
const htmlTemplate = `
<!DOCTYPE html>
<html lang="zh">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Canon Gallery</title>
    <style>
        :root { --bg: #1a1a1a; --card: #2d2d2d; --text: #e0e0e0; --accent: #3498db; }
        body { margin: 0; font-family: sans-serif; background: var(--bg); color: var(--text); padding-bottom: 50px; }
        
        header { padding: 15px 20px; background: #000; position: sticky; top: 0; z-index: 10; display: flex; align-items: center; box-shadow: 0 2px 10px rgba(0,0,0,0.5); }
        h1 { margin: 0; font-size: 1.1rem; flex: 1; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
        .nav-btn { text-decoration: none; color: #fff; padding: 6px 12px; background: #444; border-radius: 4px; margin-right: 15px; font-size: 0.9rem;}
        
        .size-control { display: flex; align-items: center; gap: 10px; margin-left: 15px; }
        .size-control-label { font-size: 0.85rem; color: #aaa; white-space: nowrap; }
        .size-slider { width: 120px; height: 4px; background: #444; border-radius: 2px; outline: none; -webkit-appearance: none; }
        .size-slider::-webkit-slider-thumb { -webkit-appearance: none; appearance: none; width: 14px; height: 14px; background: #3498db; border-radius: 50%; cursor: pointer; }
        .size-slider::-moz-range-thumb { width: 14px; height: 14px; background: #3498db; border-radius: 50%; cursor: pointer; border: none; }
        .size-value { font-size: 0.85rem; color: #fff; min-width: 35px; text-align: center; }
        @media (max-width: 480px) {
            .size-control-label { display: none; }
            .size-slider { width: 80px; }
            .size-value { min-width: 30px; font-size: 0.75rem; }
        }
        
        .folder-grid { display: flex; flex-wrap: wrap; gap: 10px; padding: 20px; border-bottom: 1px solid #333; }
        .folder { background: #252525; padding: 10px 15px; border-radius: 6px; text-decoration: none; color: #aaa; font-size: 0.9rem; border: 1px solid #333; }
        .folder:hover { background: #333; color: #fff; border-color: #555; }

        .photo-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(var(--photo-size, 200px), 1fr)); gap: 10px; padding: 20px; transition: grid-template-columns 0.3s ease; }
        @media (min-width: 768px) {
            .photo-grid { grid-template-columns: repeat(auto-fill, minmax(var(--photo-size, 150px), 1fr)); }
        }
        .photo-card { background: #000; border-radius: 4px; overflow: hidden; aspect-ratio: 3/2; position: relative; border: 1px solid #333; cursor: pointer;}
        .photo-card img { width: 100%; height: 100%; object-fit: cover; opacity: 1; transition: opacity 0.3s; display: block; }
        .photo-card img:not([src]), .photo-card img[src=""] { opacity: 0; }
        .photo-name { position: absolute; bottom: 0; width: 100%; background: rgba(0,0,0,0.7); font-size: 10px; padding: 4px 0; text-align: center; color: #ccc; }

        #sentinel { height: 50px; display: flex; justify-content: center; align-items: center; color: #666; font-size: 0.9rem; margin-top: 20px;}
        .spinner { width: 20px; height: 20px; border: 2px solid #444; border-top: 2px solid #fff; border-radius: 50%; animation: spin 1s linear infinite; display: none; margin-right: 10px;}
        @keyframes spin { 0% { transform: rotate(0deg); } 100% { transform: rotate(360deg); } }
        .loading .spinner { display: block; }

        #lightbox { display: none; position: fixed; top: 0; left: 0; width: 100%; height: 100%; background: rgba(0,0,0,0.95); z-index: 100; }
        #lb-close { position: absolute; top: 20px; right: 30px; color: #fff; font-size: 40px; cursor: pointer; z-index: 101; }
        .lb-nav { position: absolute; top: 50%; color: #fff; font-size: 50px; cursor: pointer; padding: 20px; transform: translateY(-50%); opacity: 0.5; z-index: 101; user-select: none; }
        .lb-nav:hover { opacity: 1; }
        .lb-prev { left: 10px; }
        .lb-next { right: 10px; }
        #lb-slider { position: absolute; top: 0; left: 0; width: 100%; height: 100%; overflow: hidden; }
        .lb-img { position: absolute; top: 0; left: 0; width: 100%; height: 100%; object-fit: contain; }
    </style>
</head>
<body>

<header>
    {{if .CurrentPath}}
        <a href="/?path={{.ParentPath}}" class="nav-btn">⬅ 上一级</a>
    {{end}}
    <h1>📂 {{if .CurrentPath}}{{.CurrentPath}}{{else}}根目录{{end}}</h1>
    <div class="size-control">
        <span class="size-control-label">大小</span>
        <input type="range" id="size-slider" class="size-slider" min="100" max="640" value="150" step="10">
        <span class="size-value" id="size-value">150px</span>
    </div>
</header>

{{if .Folders}}
<div class="folder-grid">
    {{$curr := .CurrentPath}}
    {{range .Folders}}
        <a href="/?path={{if $curr}}{{$curr}}/{{end}}{{.}}" class="folder">📁 {{.}}</a>
    {{end}}
</div>
{{end}}

<div class="photo-grid" id="gallery">
    {{$curr := .CurrentPath}}
    {{range .Photos}}
        <div class="photo-card" data-raw-src="/raw/{{if $curr}}{{$curr}}/{{end}}{{.}}" onclick="openLightbox('/raw/{{if $curr}}{{$curr}}/{{end}}{{.}}')">
            <img src="/thumb/{{if $curr}}{{$curr}}/{{end}}{{.}}" alt="{{.}}">
            <div class="photo-name">{{.}}</div>
        </div>
    {{end}}
</div>

<div id="sentinel">
    <div class="spinner"></div>
    <span id="status-text"></span>
</div>

<div id="lightbox">
    <span id="lb-close" onclick="closeLightbox()">&times;</span>
    <div class="lb-nav lb-prev" onclick="changeImage(-1)">&#10094;</div>
    <div class="lb-nav lb-next" onclick="changeImage(1)">&#10095;</div>
    <div id="lb-slider">
        <img id="lb-img-a" class="lb-img" src="">
        <img id="lb-img-b" class="lb-img" src="">
    </div>
</div>

<script>
    // 注册 Service Worker 实现图片缓存
    if ('serviceWorker' in navigator) {
        window.addEventListener('load', () => {
            navigator.serviceWorker.register('/sw.js')
                .then((registration) => {
                    console.log('Service Worker 注册成功:', registration.scope);
                })
                .catch((error) => {
                    console.log('Service Worker 注册失败:', error);
                });
        });
    }

    let page = 1;
    let hasNext = {{.HasNext}};
    let isLoading = false;
    
    const urlParams = new URLSearchParams(window.location.search);
    const currentPath = urlParams.get('path') || "";

    const sentinel = document.getElementById('sentinel');
    const statusText = document.getElementById('status-text');
    const gallery = document.getElementById('gallery');
    const sizeSlider = document.getElementById('size-slider');
    const sizeValue = document.getElementById('size-value');

    // 照片大小调节功能
    function initSizeControl() {
        // 从localStorage读取保存的大小，默认值根据屏幕宽度
        const savedSize = localStorage.getItem('photoSize');
        const defaultSize = window.innerWidth >= 768 ? 150 : 200;
        const initialSize = savedSize ? parseInt(savedSize) : defaultSize;
        
        sizeSlider.value = initialSize;
        updatePhotoSize(initialSize);
        
        sizeSlider.addEventListener('input', function() {
            const size = parseInt(this.value);
            updatePhotoSize(size);
            localStorage.setItem('photoSize', size);
        });
    }
    
    function updatePhotoSize(size) {
        document.documentElement.style.setProperty('--photo-size', size + 'px');
        sizeValue.textContent = size + 'px';
    }
    
    initSizeControl();

    if (!hasNext) {
        statusText.innerText = "没有更多照片了";
    }

    // 检查页面是否可以滚动（PC端优化）
    function canScroll() {
        return document.documentElement.scrollHeight > window.innerHeight + 100;
    }

    // PC端自动加载更多，直到可以滚动
    async function ensureScrollable() {
        // 等待页面初次渲染完成
        await new Promise(resolve => setTimeout(resolve, 100));

        while (!canScroll() && hasNext && !isLoading) {
            await loadMore();
            // 给 DOM 一点时间更新（卡片已有 aspect-ratio，不需要等图片加载）
            await new Promise(resolve => setTimeout(resolve, 100));
        }
    }

    const observer = new IntersectionObserver((entries) => {
        if (entries[0].isIntersecting && hasNext && !isLoading) {
            loadMore();
        }
    }, { rootMargin: "200px" });

    observer.observe(sentinel);

    // PC端初始化时自动加载直到可滚动
    if (window.innerWidth >= 768) {
        ensureScrollable();
    }

    async function loadMore() {
        isLoading = true;
        sentinel.classList.add('loading');
        statusText.innerText = "正在加载...";

        const nextPage = page + 1;
        
        try {
            // FIX: 使用字符串拼接代替模板字符串，避免 Go 语法错误
            const res = await fetch('/?path=' + encodeURIComponent(currentPath) + '&page=' + nextPage + '&format=json');
            const data = await res.json();

            if (data.photos && data.photos.length > 0) {
                data.photos.forEach(photoName => {
                    var rawSrc = '/raw/';
                    var thumbSrc = '/thumb/';
                    if (currentPath) {
                        rawSrc += currentPath + '/';
                        thumbSrc += currentPath + '/';
                    }
                    rawSrc += photoName;
                    thumbSrc += photoName;

                    const card = document.createElement('div');
                    card.className = 'photo-card';
                    card.dataset.rawSrc = rawSrc;
                    card.onclick = function() { openLightbox(rawSrc); };

                    const img = document.createElement('img');
                    img.alt = photoName;
                    img.src = thumbSrc;

                    const nameDiv = document.createElement('div');
                    nameDiv.className = 'photo-name';
                    nameDiv.innerText = photoName;

                    card.appendChild(img);
                    card.appendChild(nameDiv);
                    gallery.appendChild(card);
                });

                updateLightboxList();

                page = nextPage;
                hasNext = data.hasNext;

                if (!hasNext) {
                    statusText.innerText = "—— 已加载全部 ——";
                }
            } else {
                hasNext = false;
                statusText.innerText = "—— 已加载全部 ——";
            }

        } catch (err) {
            console.error(err);
            statusText.innerText = "加载失败，请刷新重试";
        } finally {
            isLoading = false;
            sentinel.classList.remove('loading');
            if (!hasNext) statusText.innerText = "—— 已加载全部 ——";
        }
    }

    const lightbox = document.getElementById('lightbox');
    const lbImgA = document.getElementById('lb-img-a');
    const lbImgB = document.getElementById('lb-img-b');
    let currentImages = [];
    let currentIndex = 0;
    let lbActive = 'a';     // 当前可见的 slot: 'a' 或 'b'
    let lbBusy = false;     // 动画进行中或正在加载图片，阻止并发切换

    function lbSlot(s) { return s === 'a' ? lbImgA : lbImgB; }
    function lbOther(s) { return s === 'a' ? 'b' : 'a'; }

    function updateLightboxList() {
        const cards = document.querySelectorAll('.photo-card');
        currentImages = Array.from(cards).map(card => {
            return new URL(card.dataset.rawSrc, window.location.origin).href;
        });
    }

    // 预加载图片（Promise），超时 5 秒兜底
    function preloadImage(src) {
        return new Promise(function(resolve) {
            var img = new Image();
            img.onload = img.onerror = function() { resolve(); };
            img.src = src;
            setTimeout(resolve, 5000);
        });
    }

    function openLightbox(src) {
        var targetSrc = new URL(src, window.location.origin).href;
        currentIndex = currentImages.findIndex(function(s) { return s === targetSrc; });
        if (currentIndex === -1) currentIndex = 0;

        // slot A 直接显示当前图片（无动画），slot B 清空
        lbActive = 'a';
        lbBusy = false;
        lbImgA.style.transition = 'none';
        lbImgA.style.transform = 'translateX(0)';
        lbImgA.src = currentImages[currentIndex];
        lbImgB.style.transition = 'none';
        lbImgB.style.transform = 'translateX(0)';
        lbImgB.src = '';

        lightbox.style.display = 'flex';
        document.body.style.overflow = 'hidden';
    }

    function closeLightbox() {
        lightbox.style.display = 'none';
        document.body.style.overflow = 'auto';
        lbImgA.src = '';
        lbImgB.src = '';
    }

    // 带动画的滑动切换
    function animateSlide(dir) {
        // dir: 1 = 下一张 (图片向左滑), -1 = 上一张 (图片向右滑)
        var oldSlot = lbActive;
        var newSlot = lbOther(oldSlot);
        var oldImg = lbSlot(oldSlot);
        var newImg = lbSlot(newSlot);

        // 新图放到屏幕外
        newImg.src = currentImages[currentIndex];
        newImg.style.transition = 'none';
        newImg.style.transform = dir > 0 ? 'translateX(100%)' : 'translateX(-100%)';

        // 强制回流
        newImg.offsetHeight;

        // 同时执行动画：旧图滑出，新图滑入
        oldImg.style.transition = 'transform 0.3s ease';
        newImg.style.transition = 'transform 0.3s ease';
        oldImg.style.transform = dir > 0 ? 'translateX(-100%)' : 'translateX(100%)';
        newImg.style.transform = 'translateX(0)';

        lbActive = newSlot;

        // 动画结束后解锁
        var onEnd = function() {
            lbBusy = false;
            newImg.removeEventListener('transitionend', onEnd);
        };
        newImg.addEventListener('transitionend', onEnd);
        // 兜底：300ms 后强制解锁（transitionend 可能不触发）
        setTimeout(function() { lbBusy = false; }, 350);
    }

    async function changeImage(dir) {
        if (lbBusy) return; // 动画/加载中，忽略
        lbBusy = true;

        var newIndex = currentIndex + dir;

        // 超出范围 → 尝试加载更多
        if (newIndex >= currentImages.length) {
            if (hasNext && !isLoading) {
                await loadMore();
                if (newIndex >= currentImages.length) {
                    newIndex = 0;
                }
            } else {
                newIndex = 0;
            }
        } else if (newIndex < 0) {
            newIndex = currentImages.length - 1;
        }

        currentIndex = newIndex;

        // 先预加载原图，避免动画时显示空白
        await preloadImage(currentImages[currentIndex]);
        // 预加载期间可能被 openLightbox 打断，检查状态
        if (!lightbox.style.display || lightbox.style.display === 'none') {
            lbBusy = false;
            return;
        }

        animateSlide(dir);
    }

    // 移动端左右滑动切换图片
    let touchStartX = 0;
    let touchStartY = 0;

    lightbox.addEventListener('touchstart', function(e) {
        touchStartX = e.touches[0].clientX;
        touchStartY = e.touches[0].clientY;
    }, {passive: true});

    lightbox.addEventListener('touchend', function(e) {
        const dx = e.changedTouches[0].clientX - touchStartX;
        const dy = e.changedTouches[0].clientY - touchStartY;
        const minSwipe = 50;

        if (Math.abs(dx) > Math.abs(dy) && Math.abs(dx) > minSwipe) {
            changeImage(dx > 0 ? -1 : 1);
        }
    });

    document.addEventListener('keydown', function(e) {
        if (lightbox.style.display === 'flex') {
            if (e.key === 'Escape') closeLightbox();
            if (e.key === 'ArrowLeft') changeImage(-1);
            if (e.key === 'ArrowRight') changeImage(1);
        }
    });

    updateLightboxList();

</script>

</body>
</html>
`
