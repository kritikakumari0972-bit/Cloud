# NovaVPS deployment guide

NovaVPS can be deployed to Vercel, Netlify, or Render. The Vercel and Netlify profiles host the Vite dashboard and proxy same-origin `/api/*` requests to a separately running NovaVPS Go API. The Render profile deploys the existing Go API and built dashboard together as one Docker web service.

## Choose a deployment profile

| Platform | What it runs | API configuration |
| --- | --- | --- |
| Vercel | Static Vite dashboard + Node.js API proxy function | `NOVAVPS_API_ORIGIN` |
| Netlify | Static Vite dashboard + Edge Function API proxy | `NOVAVPS_API_ORIGIN` |
| Render | Full Go API + built dashboard in one Docker service | No separate API origin needed |

All three platforms can host the web control plane. They do not grant the application host privileged access to a connected customer's hypervisor. KVM/libvirt operations must run through an authorized node/host that actually has KVM, libvirt permissions, and suitable network allocations.

## 1. Render: deploy the complete application

A Render Blueprint is provided at the repository root in `render.yaml`. It uses the monorepo service root `novavps/` and the existing `novavps/Dockerfile`.

1. In Render, create a new Blueprint instance and connect this repository.
2. Select the repository's root `render.yaml` if Render does not select it automatically.
3. Review the service named `novavps-control-panel` and deploy it.
4. Open the service URL after the `/api/health` health check passes.
5. In the Render service's **Environment** settings, open `NOVAVPS_ADMIN_PASSWORD` and copy its generated value. NovaVPS does not print an explicitly configured environment secret in logs. To choose your own password, replace this variable with a value of at least 12 characters and deploy again; the application will securely update its stored bcrypt hash at startup, even when a previous hash already exists. Keep this variable private.

The blueprint selects Render's Free web-service plan to make evaluation possible without a required paid plan. Free services sleep after 15 minutes without inbound traffic and wake on the next request. **Render Free has an ephemeral filesystem: the current NovaVPS JSON data store, encryption key, saved SSH credentials, and local application state can be lost on a spin-down, restart, or redeploy. Do not use this free profile for important server credentials or production management.** Render documents these Free plan limitations at https://render.com/docs/free and https://render.com/docs/faq.

For durable use, run the Go API on a machine with persistent storage, or move NovaVPS persistence to a separately configured durable datastore before relying on a hosted free instance. Do not assume that a free hosted filesystem is persistent.

## 2. Vercel: dashboard + API proxy

Set the Vercel project **Root Directory** to `novavps`.

The included `vercel.json` builds the Vite app from `web/`, publishes `web/dist`, and serves the catch-all function in `api/[...path].js` for `/api/*`.

1. Import `kritikakumari0972-bit/Cloud` into Vercel.
2. Set Root Directory to `novavps`.
3. Keep the build and output settings from `vercel.json`.
4. Add the environment variable `NOVAVPS_API_ORIGIN` in the Vercel project settings for the deployments you use.
5. Set it to the HTTPS origin of the Go API, for example `https://novavps-control-panel.onrender.com` (no trailing path, credentials, query, or slash is required).
6. Redeploy.

The proxy forwards the incoming HTTP method, path, query, request body, and session cookies to the trusted origin configured by the project owner. The browser continues to call the same-origin `/api/*` routes. The proxy is intentionally limited to 8 MiB request bodies because the current API accepts small JSON requests; large file transfer endpoints are not implemented yet.

Vercel's WebSocket support is available for Functions, but WebSocket connections remain associated with Function lifecycle and platform limits. NovaVPS currently provides a single-line SSH command runner over ordinary HTTP requests; it is not an interactive terminal or persistent shell/WebSocket session. See https://vercel.com/docs/limits.

## 3. Netlify: dashboard + Edge Function API proxy

Set the Netlify site's **Base directory** to `novavps`. The included `netlify.toml` runs the Vite build, publishes `web/dist`, registers the Edge Function directory, routes `/api/*` through the proxy, and provides the SPA fallback.

1. Import `kritikakumari0972-bit/Cloud` into Netlify.
2. Set Base directory to `novavps`.
3. Keep the build command and publish directory from `netlify.toml`.
4. In Netlify's environment variable settings, add `NOVAVPS_API_ORIGIN` with a value such as `https://novavps-control-panel.onrender.com`.
5. Ensure the variable's scope includes **Functions** (Edge Functions use the `Netlify.env` runtime API).
6. Redeploy.

Do not put API credentials or SSH keys in `netlify.toml` or frontend build variables. Only the public API origin belongs in `NOVAVPS_API_ORIGIN`. See https://docs.netlify.com/build/edge-functions/environment-variables/.

## 4. Shared configuration and security

- Use an HTTPS Go API origin. The Vercel and Netlify proxies reject non-HTTPS origins.
- Keep `NOVAVPS_API_ORIGIN` as an environment variable; do not hard-code it into the frontend bundle.
- Configure `NOVAVPS_COOKIE_SECURE=true` on the Go API when it is served behind HTTPS.
- Verify SSH host-key fingerprints independently before accepting them.
- Use a restricted SSH account instead of root where possible.
- Do not expose the Go API's data directory or encryption key.
- Do not use Render Free's ephemeral local storage for saved SSH credentials.
- Keep the repository's existing Pterodactyl/Cloud installer separate; the Render blueprint uses `novavps/` as its service root.

## 5. Single-line SSH command runner

The dashboard includes a **Command runner** for connected servers. Choose a saved server and submit one command line; NovaVPS opens a fresh SSH exec session using that server's saved SSH credentials and runs the command as the configured SSH user.

- Multiline input and NUL bytes are rejected; the command limit is 2,048 bytes.
- Each request has a 20-second server-side time limit. At timeout, NovaVPS closes the SSH session; whether the remote process fully stops depends on the SSH server and process behavior.
- Combined standard output and error capture is limited to 48 KiB. Larger output is truncated.
- The latest 10 results remain only in the current browser session. NovaVPS does not persist command text or output.
- This is not an interactive terminal: it has no persistent shell state, TTY, streaming output, or interactive prompts. Every submission starts a new SSH exec request.

Commands execute with the permissions of the saved SSH account and may change the remote machine. Run commands only on systems you own or are authorized to administer. Use a restricted SSH account with only the permissions needed for your tasks.

## 6. Free-tier and feature boundaries

The deployment files and NovaVPS software are free/open source. Hosting providers can change free quotas and may suspend or sleep free services. Vercel and Netlify deployments in this configuration are dashboard/API-proxy deployments; the Go management API must be reachable at `NOVAVPS_API_ORIGIN`. Render can run both the dashboard and Go API, but its Free service is for evaluation and does not provide persistent local storage.

This deployment work does not make Vercel, Netlify, or Render into KVM hypervisors and does not allocate public IP addresses. Connected VPS/KVM machines remain the user's own infrastructure. Dedicated public IPv4/IPv6 assignment still requires provider-assigned addresses or a routed prefix and a host/network agent with the required permissions.

## Local deployment-config checks

From `novavps/`:

```bash
# Frontend production build
cd web
npm install
npm run build
cd ..

# Validate JSON config
python3 -m json.tool vercel.json >/dev/null

# Validate YAML if PyYAML is available
python3 - <<'PY'
import yaml
for file in ("../render.yaml",):
    with open(file, encoding="utf-8") as stream:
        yaml.safe_load(stream)
print("deployment YAML parsed")
PY
```

The checks above validate configuration syntax and the frontend build; they do not deploy to provider accounts or verify a live remote API.
