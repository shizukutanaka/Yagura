// pwa.go: dashboard を「インストール可能なデスクトップアプリ」にする PWA レイヤー。
//
// 動機: CLI に馴染みのない人でも Yagura を扱えるよう、ブラウザの「インストール」から
// スタンドアロンのデスクトップウィンドウ(アプリ枠)として起動できるようにする。
//
// 根幹は崩さない(additive only):
//   - 新規ルート(/dashboard/manifest.webmanifest, /dashboard/sw.js, /dashboard/icon.svg)は
//     既存 dashboard.Handler の ServeHTTP 内で path 分岐するだけ。main.go のルーティング、
//     daemon、MCP は一切触らない。
//   - 外部依存ゼロ(ADR-0001): manifest / service worker / icon は静的文字列、Go の
//     net/http stdlib で配信。GUI フレームワークは使わない(Web 標準のみ)。
//   - 既存 /dashboard の HTML レンダリングは不変(head にタグを足すのみ)。
package dashboard

import "net/http"

// 注: PWA の <head> メタと service-worker 登録スクリプトは dashboard.go の HTML
// テンプレートに直書きしている(単一ソース)。本ファイルは配信アセット側を担う。

// webManifest は PWA インストール用の manifest(W3C Web App Manifest)。
const webManifest = `{
  "name": "Yagura Portfolio Dashboard",
  "short_name": "Yagura",
  "description": "Zero-dependency local orchestrator for your project portfolio.",
  "start_url": "/dashboard",
  "scope": "/dashboard",
  "display": "standalone",
  "orientation": "any",
  "background_color": "#0d1117",
  "theme_color": "#0d1117",
  "lang": "ja",
  "icons": [
    { "src": "/dashboard/icon.svg", "sizes": "any", "type": "image/svg+xml", "purpose": "any maskable" }
  ]
}`

// serviceWorker は最小の network-first SW(オフライン耐性 + インストール可能化)。
const serviceWorker = `const C = 'yagura-shell-v1';
self.addEventListener('install', function(e){ self.skipWaiting(); });
self.addEventListener('activate', function(e){ self.clients.claim(); });
self.addEventListener('fetch', function(e){
  if (e.request.method !== 'GET') return;
  e.respondWith(
    fetch(e.request).then(function(r){
      var c = r.clone();
      caches.open(C).then(function(cache){ cache.put(e.request, c); }).catch(function(){});
      return r;
    }).catch(function(){ return caches.match(e.request); })
  );
});`

// appIcon は maskable な watchtower(櫓)アイコン(SVG, 依存なし)。
const appIcon = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512" role="img" aria-label="Yagura">
<rect width="512" height="512" rx="96" fill="#0d1117"/>
<g fill="none" stroke="#58a6ff" stroke-width="20" stroke-linejoin="round" stroke-linecap="round">
<path d="M256 96 L150 184 L362 184 Z"/>
<path d="M176 184 L176 416 M336 184 L336 416 M150 416 L362 416"/>
<path d="M176 260 L336 260 M176 336 L336 336"/>
<path d="M226 184 L256 416 L286 184"/>
</g>
</svg>`

// serveAsset は PWA 静的アセットを配信する。dashboard 配下の既知 path のみ true を返す。
// 未知 path は false を返し、呼び出し側(ServeHTTP)が通常の HTML を描画する。
func serveAsset(w http.ResponseWriter, path string) bool {
	switch path {
	case "/dashboard/manifest.webmanifest":
		w.Header().Set("Content-Type", "application/manifest+json; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Write([]byte(webManifest))
	case "/dashboard/sw.js":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Service-Worker-Allowed", "/dashboard")
		w.Write([]byte(serviceWorker))
	case "/dashboard/icon.svg":
		w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write([]byte(appIcon))
	default:
		return false
	}
	return true
}
