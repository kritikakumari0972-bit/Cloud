/*
 * NovaVPS same-origin API proxy for Netlify Edge Functions.
 *
 * Configure NOVAVPS_API_ORIGIN in Netlify's UI/CLI with the Functions scope.
 * Example value: https://novavps-api.onrender.com
 */
export default async (request: Request): Promise<Response> => {
  try {
    const rawOrigin = Netlify.env.get("NOVAVPS_API_ORIGIN");
    if (!rawOrigin) {
      return Response.json(
        { error: "novavps_api_proxy_not_configured", message: "Set NOVAVPS_API_ORIGIN in Netlify environment variables (Functions scope)." },
        { status: 503, headers: { "cache-control": "no-store" } },
      );
    }

    const origin = new URL(rawOrigin);
    if (
      origin.protocol !== "https:" ||
      origin.username ||
      origin.password ||
      origin.pathname !== "/" ||
      origin.search ||
      origin.hash
    ) {
      return Response.json(
        { error: "invalid_api_origin", message: "NOVAVPS_API_ORIGIN must be an HTTPS origin without credentials or a path." },
        { status: 500, headers: { "cache-control": "no-store" } },
      );
    }

    const incoming = new URL(request.url);
    if (!incoming.pathname.startsWith("/api/")) {
      return new Response("Not found", { status: 404 });
    }

    const target = new URL(incoming.pathname + incoming.search, origin.origin);
    const forwardedHeaders = new Headers(request.headers);
    forwardedHeaders.set("x-forwarded-host", incoming.host);
    forwardedHeaders.set("x-forwarded-proto", incoming.protocol.slice(0, -1));
    const upstreamRequest = new Request(target, {
      method: request.method,
      headers: forwardedHeaders,
      body: request.body,
      redirect: "manual",
      duplex: "half",
    } as RequestInit);
    const upstream = await fetch(upstreamRequest, { redirect: "manual" });

    const headers = new Headers(upstream.headers);
    for (const name of ["connection", "content-length", "transfer-encoding", "keep-alive", "upgrade"]) {
      headers.delete(name);
    }
    headers.set("cache-control", "no-store");

    return new Response(upstream.body, {
      status: upstream.status,
      statusText: upstream.statusText,
      headers,
    });
  } catch (error) {
    return Response.json(
      {
        error: "novavps_api_proxy_failed",
        message: error instanceof Error ? error.message : "The NovaVPS API is unavailable.",
      },
      { status: 502, headers: { "cache-control": "no-store" } },
    );
  }
};

export const config = { path: "/api/*" };
