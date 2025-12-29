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
)

// ================= 配置区 =================
const (
	PhotoRoot = "/mnt/Fanxiang2T/canon/"
	Port      = ":8092"
	PageSize  = 20
	CertFile  = "cert.pem"
	KeyFile   = "key.pem"
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

	http.HandleFunc("/sw.js", handleServiceWorker)
	http.HandleFunc("/", handleGallery)

	fmt.Printf("\n🚀 无限滚动版相册已启动\n")
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
// 这个 Service Worker 会在浏览器本地缓存图片，缓存1天后自动清理并释放空间
const serviceWorkerScript = `
const CACHE_NAME = 'gallery-photos-v1';
const CACHE_DURATION = 24 * 60 * 60 * 1000; // 1天（毫秒）

// 检查缓存是否过期
function isCacheExpired(cachedTime) {
    if (!cachedTime) return true;
    const age = Date.now() - parseInt(cachedTime);
    return age >= CACHE_DURATION;
}

// 清理过期缓存并释放空间
async function cleanExpiredCache() {
    try {
        const cache = await caches.open(CACHE_NAME);
        const keys = await cache.keys();
        const deletePromises = [];
        
        for (const request of keys) {
            const response = await cache.match(request);
            if (response) {
                const cachedTime = response.headers.get('sw-cached-time');
                if (isCacheExpired(cachedTime)) {
                    // 删除过期缓存，释放空间
                    deletePromises.push(cache.delete(request));
                }
            }
        }
        
        // 等待所有删除操作完成
        await Promise.all(deletePromises);
        console.log('已清理过期缓存，释放空间:', deletePromises.length, '个文件');
    } catch (error) {
        console.error('清理缓存失败:', error);
    }
}

// 安装 Service Worker
self.addEventListener('install', (event) => {
    self.skipWaiting();
});

// 激活 Service Worker，立即清理过期缓存
self.addEventListener('activate', (event) => {
    event.waitUntil(
        Promise.all([
            // 删除旧版本的缓存
            caches.keys().then((cacheNames) => {
                return Promise.all(
                    cacheNames.map((cacheName) => {
                        if (cacheName !== CACHE_NAME) {
                            return caches.delete(cacheName);
                        }
                    })
                );
            }),
            // 清理当前缓存中的过期项
            cleanExpiredCache(),
            // 声明控制权
            self.clients.claim()
        ])
    );
});

// 拦截图片请求，实现浏览器本地缓存
self.addEventListener('fetch', (event) => {
    const url = new URL(event.request.url);
    
    // 只缓存 /raw/ 路径下的图片
    if (url.pathname.startsWith('/raw/')) {
        event.respondWith(
            caches.open(CACHE_NAME).then((cache) => {
                return cache.match(event.request).then((cachedResponse) => {
                    // 如果有缓存，检查是否过期
                    if (cachedResponse) {
                        const cachedTime = cachedResponse.headers.get('sw-cached-time');
                        if (cachedTime && !isCacheExpired(cachedTime)) {
                            // 缓存未过期，直接从浏览器本地缓存返回
                            return cachedResponse;
                        } else {
                            // 缓存已过期，立即删除以释放空间
                            cache.delete(event.request).catch(() => {});
                        }
                    }
                    
                    // 从网络获取新图片
                    return fetch(event.request).then((response) => {
                        // 只缓存成功的响应
                        if (response.status === 200) {
                            // 克隆响应以便缓存
                            const responseClone = response.clone();
                            
                            // 创建带时间戳的响应用于缓存
                            const newHeaders = new Headers(response.headers);
                            newHeaders.set('sw-cached-time', Date.now().toString());
                            
                            const cachedResponse = new Response(responseClone.body, {
                                status: response.status,
                                statusText: response.statusText,
                                headers: newHeaders
                            });
                            
                            // 将图片保存到浏览器本地缓存
                            cache.put(event.request, cachedResponse);
                        }
                        return response;
                    }).catch(() => {
                        // 网络失败时，如果有旧缓存（即使过期），也返回
                        if (cachedResponse) {
                            return cachedResponse;
                        }
                        throw new Error('Network error and no cache');
                    });
                });
            })
        );
    }
});

// 监听消息，支持手动触发清理
self.addEventListener('message', (event) => {
    if (event.data && event.data.type === 'CLEAN_CACHE') {
        cleanExpiredCache().then(() => {
            event.ports[0].postMessage({ success: true });
        });
    }
});

// 定期清理过期缓存（每30分钟检查一次，确保及时释放空间）
// 注意：Service Worker 可能被休眠，但每次激活时会自动清理
setInterval(() => {
    cleanExpiredCache();
}, 30 * 60 * 1000);
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

        #lightbox { display: none; position: fixed; top: 0; left: 0; width: 100%; height: 100%; background: rgba(0,0,0,0.95); z-index: 100; justify-content: center; align-items: center; }
        #lightbox img { max-width: 95%; max-height: 95%; }
        #lb-close { position: absolute; top: 20px; right: 30px; color: #fff; font-size: 40px; cursor: pointer; }
        .lb-nav { position: absolute; top: 50%; color: #fff; font-size: 50px; cursor: pointer; padding: 20px; transform: translateY(-50%); opacity: 0.5; }
        .lb-nav:hover { opacity: 1; }
        .lb-prev { left: 10px; }
        .lb-next { right: 10px; }
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
        <div class="photo-card" onclick="openLightbox('/raw/{{if $curr}}{{$curr}}/{{end}}{{.}}')">
            <img src="/raw/{{if $curr}}{{$curr}}/{{end}}{{.}}" alt="{{.}}">
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
    <img id="lb-img" src="">
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

    // 等待所有图片加载完成
    function waitForImages() {
        return new Promise((resolve) => {
            const imgs = document.querySelectorAll('.photo-card img');
            if (imgs.length === 0) {
                resolve();
                return;
            }
            let loaded = 0;
            const total = imgs.length;
            imgs.forEach(img => {
                if (img.complete) {
                    loaded++;
                    if (loaded === total) resolve();
                } else {
                    img.onload = img.onerror = () => {
                        loaded++;
                        if (loaded === total) resolve();
                    };
                }
            });
            // 超时保护
            setTimeout(resolve, 2000);
        });
    }

    // PC端自动加载更多，直到可以滚动
    async function ensureScrollable() {
        // 等待初始图片加载完成
        await waitForImages();
        
        // 如果无法滚动且还有更多内容，继续加载
        while (!canScroll() && hasNext && !isLoading) {
            await loadMore();
            // 等待新内容渲染和图片加载
            await new Promise(resolve => setTimeout(resolve, 500));
            await waitForImages();
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
                    let src = '/raw/';
                    if (currentPath) src += currentPath + '/';
                    src += photoName;

                    const card = document.createElement('div');
                    card.className = 'photo-card';
                    // FIX: 这里也改成了箭头函数，避免复杂的字符串拼接
                    card.onclick = function() { openLightbox(src); };
                    
                    const img = document.createElement('img');
                    img.loading = "lazy";
                    img.alt = photoName;
                    img.src = src;

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
    const lbImg = document.getElementById('lb-img');
    let currentImages = [];
    let currentIndex = 0;

    function updateLightboxList() {
        const imgs = document.querySelectorAll('.photo-card img');
        currentImages = Array.from(imgs).map(img => img.src);
    }

    function openLightbox(src) {
        const targetSrc = new URL(src, window.location.origin).href;
        currentIndex = currentImages.findIndex(s => s === targetSrc);
        if(currentIndex === -1) currentIndex = 0;
        
        lbImg.src = currentImages[currentIndex];
        lightbox.style.display = 'flex';
        document.body.style.overflow = 'hidden';
    }

    function closeLightbox() {
        lightbox.style.display = 'none';
        document.body.style.overflow = 'auto';
        lbImg.src = "";
    }

    async function changeImage(dir) {
        // 向左切换
        if (dir < 0) {
            currentIndex += dir;
            if (currentIndex < 0) currentIndex = currentImages.length - 1;
            lbImg.src = currentImages[currentIndex];
            return;
        }
        
        // 向右切换
        currentIndex += dir;
        
        // 如果到达最后一张，检查是否需要加载更多
        if (currentIndex >= currentImages.length) {
            // 如果有更多照片且不在加载中，先加载更多
            if (hasNext && !isLoading) {
                await loadMore();
                // loadMore() 已经调用了 updateLightboxList()，现在应该可以继续了
                if (currentIndex < currentImages.length) {
                    lbImg.src = currentImages[currentIndex];
                } else {
                    // 如果加载后还是没有更多，循环回第一张
                    currentIndex = 0;
                    lbImg.src = currentImages[currentIndex];
                }
            } else {
                // 没有更多照片了，循环回第一张
                currentIndex = 0;
                lbImg.src = currentImages[currentIndex];
            }
        } else {
            // 正常切换
            lbImg.src = currentImages[currentIndex];
        }
    }

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
