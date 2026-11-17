# NovaVPS

NovaVPS is an open-source, self-hosted control panel for connecting and managing Linux servers you already own or are authorized to administer. It provides agentless SSH enrollment, encrypted credential storage, host-key pinning, basic server metrics, and an IPv4/IPv6 IPAM inventory for provider-authorized address ranges.

> Early development build. This is not yet a production multi-tenant cloud platform. Browser terminal, SFTP file manager, outbound agent, provider APIs, KVM/libvirt provisioning, routed public IP attachment, user registration, RBAC, and audit history remain future milestones.

## Deployment targets\n\nNovaVPS includes deployment profiles for Vercel, Netlify, and Render. Vercel and Netlify host the web dashboard and proxy `/api/*` to a separately hosted Go API; Render can host the Go API and dashboard together. See [DEPLOYMENT.md](DEPLOYMENT.md) for setup instructions and free-tier limitations.\n\n## Cloud-style installation

This release adopts the interactive one-command installation workflow from the owner's `Cloud` repository, but installs NovaVPS itself rather than Pterodactyl. It does not reuse Cloud's external scripts or their credentials/configuration.

1. Download and extract the complete NovaVPS release ZIP onto an Ubuntu or Debian machine.
2. Run the installer from the extracted project directory:

```bash
sudo bash ./install.sh
```

The installer checks root permissions and OS support, installs Docker Engine and the official Docker Compose plugin if needed, copies NovaVPS to `/opt/novavps`, creates a command named `novavps`, builds the application, and starts it.

Open the local panel at `http://127.0.0.1:8080`. On a remote host, use an SSH tunnel:

```bash
ssh -L 8080:127.0.0.1:8080 USER@YOUR_SERVER
```

Then open `http://127.0.0.1:8080` on your own device. On first launch, open the panel and create the administrator username and password yourself. The account-creation screen is available only through localhost. For a remote Ubuntu/Debian machine, create the SSH tunnel shown above and open the local URL in your browser. The first account is the single owner account for that installation; separate user accounts and roles are not implemented yet.

### Optional public HTTPS

Use a domain whose DNS A/AAAA records point to this machine, and ensure inbound ports 80 and 443 are reachable:

```bash
sudo bash ./install.sh --public --domain panel.example.com
```

Caddy is enabled only for this opt-in profile. Do not expose the backend API port `8080` directly to the public Internet.

### Interactive management menu

After installation:

```bash
sudo novavps
```

Available commands:

```text
novavps                 Interactive manager
novavps start           Build (if needed) and start services
novavps stop            Stop services; keep data
novavps restart         Restart services
novavps status          Show service and API status
novavps logs            Follow service logs
novavps url             Show panel URL / SSH tunnel hint
novavps backup          Back up persistent application data
novavps update          Update a Git checkout, if present
novavps doctor          Print diagnostics
novavps uninstall       Remove containers; preserve data
novavps purge           Destructively delete containers and volumes
novavps help            Show help
```

`uninstall` preserves the Docker data volume. `purge` permanently removes it and requires typing `DELETE NOVAVPS DATA`.

**One-line remote installer note:** A canonical public GitHub repository URL has not yet been established for NovaVPS, so this release does not publish a fake `curl | bash` URL. Once the project is pushed to its permanent repository, the equivalent raw installer command can be added.

## Current capabilities

- Private administrator sign-in (single-owner bootstrap).
- Add multiple Linux servers through SSH.
- Password or SSH private-key authentication.
- First-connection SSH host-key fingerprint confirmation.
- Credentials encrypted at rest using AES-256-GCM.
- Server inventory and remove-from-panel action.
- Read-only metrics from a fixed set of remote commands.
- Responsive React/TypeScript dashboard.
- Docker Compose deployment and optional Caddy HTTPS.
- IPv4/IPv6 address-pool inventory with CIDR validation and overlap detection.
- IP reservations for server, VM, container, and other named targets.
- Persistent IPAM state and reservation release.
- Cloud-style installer and terminal manager.

No server is provisioned by this build. It connects to existing machines. The host must allow SSH from the NovaVPS host, and the SSH account must have permission to read the requested metrics.

## IP address inventory (Phase 2)

Open **Networking** in the dashboard to register a public IPv4/IPv6 CIDR prefix that has been assigned to you or your organization by an upstream provider or registry. NovaVPS rejects common private, documentation, link-local, multicast, and reserved ranges, and rejects overlapping pools. You can then reserve the next available address for a named server, VM, or container. Reservations are stored in `network.json` inside the persistent data directory.

Authenticated API endpoints:

- `GET /api/network/overview` — IPAM counts and capability status.
- `GET /api/network/pools` / `POST /api/network/pools` — list or add an authorized address pool.
- `DELETE /api/network/pools/{id}` — remove an unused pool.
- `GET /api/network/allocations` / `POST /api/network/allocations` — list or reserve an address.
- `DELETE /api/network/allocations/{id}` — release a reservation.

Pool creation requires an explicit ownership/authorization attestation. This is an administrative safeguard, not registry verification. Never register address space that is not assigned to you. A recorded reservation is **not yet a working public IP on a guest**. Real assignment requires a later privileged node agent plus provider-supported routing, a valid gateway, host firewall rules, and an end-to-end reachability check. Shared IPv4 NAT provides outbound connectivity but does not give every guest a unique public IPv4.

## SSH host-key verification

On first connection, NovaVPS displays the SSH server's SHA256 host-key fingerprint and requires explicit confirmation. Verify it independently from the server's provider console or another trusted channel before accepting it. Do not blindly approve a fingerprint on an untrusted network. For OpenSSH servers, a privileged console can show the fingerprint of a host public key with:

```bash
for key in /etc/ssh/ssh_host_*_key.pub; do
  [ -f "$key" ] && ssh-keygen -lf "$key"
done
```

The fingerprint displayed by NovaVPS must match the independently obtained fingerprint. A changed fingerprint is rejected.

## Requirements

- Ubuntu or Debian with systemd and APT for the automated installer.
- Root access for initial Docker installation and setup.
- Internet access to install Docker packages and build containers.
- A Linux host to run the panel.
- SSH reachability from the panel host to each server. IPv4, IPv6, and DNS names are accepted.
- The user must own the server or have explicit authorization to administer it.

## Local development

Terminal 1:

```bash
cd backend
go run .
```

Terminal 2:

```bash
cd web
npm install
npm run dev
```

The Vite development server proxies `/api` to `http://localhost:8080`.

## Security notes and limitations

- Credentials are encrypted with an AES-256-GCM key stored in the data volume with restrictive file permissions. Back up the entire data volume securely; losing `encryption.key` makes saved credentials unrecoverable.
- The bootstrap administrator password is randomly generated and printed once; only its bcrypt hash is stored in the data volume with mode `0600`. The current build is single-administrator, not public multi-user authentication.
- The browser never stores SSH credentials in local storage. Secrets are submitted over the panel origin and encrypted before persistence.
- The current SSH metrics endpoint runs only fixed, application-defined read-only commands. An interactive shell is not implemented yet.
- Avoid using the root account when a restricted sudo-capable account is sufficient. Use dedicated SSH keys and revoke keys when access is no longer needed.
- NovaVPS does not allocate public IP addresses, bypass provider limits, or turn a restricted VM into a KVM host. Those capabilities depend on the connected provider, machine, and network.
- Do not paste installer commands from untrusted sources into a root shell. Inspect scripts before running them.

## Roadmap

1. SSH connection manager and server inventory.
2. IPv4/IPv6 IPAM pools and address reservations (implemented as inventory only).
3. Authenticated browser terminal over WebSocket with session auditing and idle timeouts.
4. SFTP file browser and safe file operations.
5. Optional outbound NovaVPS agent for servers not reachable by inbound SSH.
6. KVM/libvirt host discovery and VM lifecycle management for authorized hosts.
7. Routed IPv6 and provider-supported dedicated IPv4 attachment through the node agent, with verified connectivity checks.
8. Shared IPv4 NAT gateway and optional relay connectivity, clearly distinguished from dedicated public addresses.
9. Provider API integrations with user-supplied credentials and least-privilege scopes.
10. Multi-user accounts, roles, project isolation, and audit logs.

## License

MIT. Third-party dependencies retain their respective licenses.
