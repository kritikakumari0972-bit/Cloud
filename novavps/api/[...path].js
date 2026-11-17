/*
 * NovaVPS same-origin API proxy for Vercel.
 *
 * Set NOVAVPS_API_ORIGIN to the HTTPS origin of a separately deployed
 * NovaVPS Go API, for example https://novavps-api.onrender.com.
 * The browser talks only to /api/* on this deployment, so session cookies
 * remain first-party and the API origin is not embedded in the frontend.
 */
const HOP_BY_HOP = new Set([
  "connection",
  "content-length",
  "host",
  "keep-alive",
  "proxy-authenticate",
  "proxy-authorization",
  "te",
  "trailer",
  "transfer-encoding",
  "upgrade",
]);

const MAX_BODY_BYTES = 8 * 1024 * 1024;

function safeApiOrigin() {
  const raw = process.env.NOVAVPS_API_ORIGIN;
  if (!raw) throw new Error("NOVAVPS_API_ORIGIN is not configured");
  const url = new URL(raw);
  if (url.protocol !== "https:" || url.username || url.password || url.pathname !== "/" || url.search || url.hash) {
    throw new Error("NOVAVPS_API_ORIGIN must be an HTTPS origin without credentials, path, query, or fragment");
  }
  return url.origin;
}

async function readRequestBody(req) {
  const chunks = [];
  let size = 0;
  for await (const chunk of req) {
    const buffer = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk);
    size += buffer.length;
    if (size > MAX_BODY_BYTES) {
      const error = new Error("Request body exceeds the 8 MiB proxy limit");
      error.statusCode = 413;
      throw error;
    }
    chunks.push(buffer);
  }
  return Buffer.concat(chunks);
}

module.exports = async function novavpsApiProxy(req, res) {
  try {
    const origin = safeApiOrigin();
    const incoming = new URL(req.url || "/", `https://${req.headers.host || "vercel.invalid"}`);
    if (!incoming.pathname.startsWith("/api/")) {
      res.statusCode = 404;
      res.end("Not found");
      return;
    }

    const target = new URL(incoming.pathname + incoming.search, origin);
    const headers = new Headers();
    for (const [name, value] of Object.entries(req.headers)) {
      const key = name.toLowerCase();
      if (HOP_BY_HOP.has(key) || value === undefined) continue;
      if (Array.isArray(value)) value.forEach((part) => headers.append(name, part));
      else headers.set(name, String(value));
    }
    headers.set("x-forwarded-host", String(req.headers.host || ""));
    headers.set("x-forwarded-proto", "https");

    const method = String(req.method || "GET").toUpperCase();
    const body = method === "GET" || method === "HEAD" ? undefined : await readRequestBody(req);
    const upstream = await fetch(target, {
      method,
      headers,
      body: body && body.length ? body : undefined,
      redirect: "manual",
    });

    res.statusCode = upstream.status;
    for (const [name, value] of upstream.headers.entries()) {
      const key = name.toLowerCase();
      if (HOP_BY_HOP.has(key) || key === "set-cookie") continue;
      res.setHeader(name, value);
    }

    const cookies = typeof upstream.headers.getSetCookie === "function"
      ? upstream.headers.getSetCookie()
      : (upstream.headers.get("set-cookie") ? [upstream.headers.get("set-cookie")] : []);
    if (cookies.length) res.setHeader("set-cookie", cookies);

    res.setHeader("cache-control", "no-store");
    if (method === "HEAD") {
      res.end();
      return;
    }
    const payload = Buffer.from(await upstream.arrayBuffer());
    res.end(payload);
  } catch (error) {
    const status = Number(error && error.statusCode) || 503;
    res.statusCode = status;
    res.setHeader("content-type", "application/json; charset=utf-8");
    res.setHeader("cache-control", "no-store");
    res.end(JSON.stringify({
      error: "novavps_api_proxy_failed",
      message: error && error.message ? error.message : "The NovaVPS API is unavailable",
    }));
  }
};


// Keep the raw request stream available for the proxy instead of parsing JSON first.
module.exports.config = { api: { bodyParser: false } };
