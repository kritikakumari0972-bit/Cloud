import { type FormEvent, type ReactNode, useCallback, useEffect, useMemo, useState } from 'react'
import {
  Activity, ArrowUpRight, Check, ChevronRight, Cpu, Database, Globe2,
  HardDrive, KeyRound, LayoutDashboard, LockKeyhole, LogOut, Menu,
  Network, Plus, RefreshCw, Server, ShieldCheck, TerminalSquare, Trash2,
  X, Zap,
} from 'lucide-react'

type ServerRecord = {
  id: string; name: string; host: string; port: number; username: string
  authMethod: string; fingerprint: string; addedAt: string
}
type Pool = { id: string; name: string; cidr: string; gateway?: string; family: string; createdAt: string }
type Allocation = { id: string; poolId: string; address: string; targetType: string; targetId: string; createdAt: string }
type Metrics = Record<string, string>
type CommandRun = {
  id: string; serverName: string; command: string; output: string
  exitCode: number; timedOut: boolean; truncated: boolean; durationMs: number; ranAt: string
}
type ApiError = Error & { status?: number; data?: Record<string, unknown> }
type ConnectValues = {
  name: string; host: string; port: number; username: string; authMethod: string
  password: string; privateKey: string; keyPassphrase: string
}
type Page = 'overview' | 'servers' | 'network' | 'terminal'

async function api<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers)
  if (init.body && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')
  const response = await fetch(path, { ...init, headers, credentials: 'same-origin' })
  const data = response.status === 204 ? {} : await response.json().catch(() => ({}))
  if (!response.ok) {
    const error = new Error((data as Record<string, string>).message || (data as Record<string, string>).error || 'Request failed') as ApiError
    error.status = response.status
    error.data = data as Record<string, unknown>
    throw error
  }
  return data as T
}

function bytes(value?: string) {
  const n = Number(value)
  if (!Number.isFinite(n) || n <= 0) return '—'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let size = n, unit = 0
  while (size >= 1024 && unit < units.length - 1) { size /= 1024; unit++ }
  return `${size.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`
}
function timeAgo(value: string) {
  const seconds = Math.max(0, Math.floor((Date.now() - new Date(value).getTime()) / 1000))
  if (seconds < 60) return 'Just now'
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`
  return `${Math.floor(seconds / 86400)}d ago`
}

function App() {
  const [authenticated, setAuthenticated] = useState<boolean | null>(null)
  const [password, setPassword] = useState('')
  const [page, setPage] = useState<Page>('overview')
  const [servers, setServers] = useState<ServerRecord[]>([])
  const [pools, setPools] = useState<Pool[]>([])
  const [allocations, setAllocations] = useState<Allocation[]>([])
  const [metrics, setMetrics] = useState<Record<string, Metrics>>({})
  const [loading, setLoading] = useState(false)
  const [busy, setBusy] = useState(false)
  const [notice, setNotice] = useState('')
  const [error, setError] = useState('')
  const [connectOpen, setConnectOpen] = useState(false)
  const [poolOpen, setPoolOpen] = useState(false)
  const [mobileMenu, setMobileMenu] = useState(false)
  const [pendingConnect, setPendingConnect] = useState<{ values: ConnectValues; fingerprint: string } | null>(null)
  const [fingerprintVerified, setFingerprintVerified] = useState(false)
  const [connectValues, setConnectValues] = useState<ConnectValues>({
    name: '', host: '', port: 22, username: 'root', authMethod: 'privateKey',
    password: '', privateKey: '', keyPassphrase: '',
  })
  const [poolValues, setPoolValues] = useState({ name: '', cidr: '', gateway: '', authorized: false })
  const [allocationValues, setAllocationValues] = useState({ poolId: '', targetType: 'server', targetId: '' })
  const [commandServerId, setCommandServerId] = useState('')
  const [commandText, setCommandText] = useState('')
  const [commandBusy, setCommandBusy] = useState(false)
  const [commandRuns, setCommandRuns] = useState<CommandRun[]>([])

  const loadData = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const [serverData, poolData, allocationData] = await Promise.all([
        api<{ servers: ServerRecord[] }>('/api/servers'),
        api<{ pools: Pool[] }>('/api/network/pools'),
        api<{ allocations: Allocation[] }>('/api/network/allocations'),
      ])
      setServers(serverData.servers || [])
      setCommandServerId(current => serverData.servers?.some(server => server.id === current) ? current : serverData.servers?.[0]?.id || '')
      setPools(poolData.pools || [])
      setAllocations(allocationData.allocations || [])
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Could not load NovaVPS data')
    } finally { setLoading(false) }
  }, [])

  useEffect(() => {
    api<{ authenticated: boolean }>('/api/auth/me')
      .then(() => { setAuthenticated(true); void loadData() })
      .catch((e: ApiError) => setAuthenticated(e.status === 401 ? false : false))
  }, [loadData])

  const refreshMetrics = useCallback(async (serverId: string) => {
    setBusy(true)
    setError('')
    try {
      const result = await api<{ metrics: Metrics }>(`/api/servers/${serverId}/metrics`)
      setMetrics(current => ({ ...current, [serverId]: result.metrics }))
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Metrics request failed')
    } finally { setBusy(false) }
  }, [])

  const totals = useMemo(() => ({
    servers: servers.length,
    ipv4: allocations.filter(a => a.address.includes('.')).length,
    ipv6: allocations.filter(a => a.address.includes(':')).length,
    pools: pools.length,
  }), [servers, pools, allocations])

  async function handleLogin(event: FormEvent) {
    event.preventDefault()
    setBusy(true); setError('')
    try {
      await api('/api/auth/login', { method: 'POST', body: JSON.stringify({ username: 'admin', password }) })
      setAuthenticated(true); setPassword(''); await loadData()
    } catch (e) { setError(e instanceof Error ? e.message : 'Sign-in failed') }
    finally { setBusy(false) }
  }

  function closeConnect() {
    setConnectOpen(false); setPendingConnect(null); setFingerprintVerified(false)
    setConnectValues({ name: '', host: '', port: 22, username: 'root', authMethod: 'privateKey', password: '', privateKey: '', keyPassphrase: '' })
  }

  async function submitConnect(event?: FormEvent, confirmed = false) {
    event?.preventDefault()
    setBusy(true); setError(''); setNotice('')
    const values = pendingConnect?.values || connectValues
    const body = {
      ...values,
      expectedHostKey: confirmed && pendingConnect ? pendingConnect.fingerprint : '',
    }
    try {
      await api('/api/servers/connect', { method: 'POST', body: JSON.stringify(body) })
      setNotice('Server connected securely and added to your inventory.')
      closeConnect(); await loadData()
    } catch (e) {
      const err = e as ApiError
      if (err.status === 428 && typeof err.data?.fingerprint === 'string') {
        setPendingConnect({ values, fingerprint: err.data.fingerprint })
        setFingerprintVerified(false)
      } else setError(err.message)
    } finally { setBusy(false) }
  }

  async function executeSingleLineCommand(event: FormEvent) {
    event.preventDefault()
    const command = commandText.trim()
    if (!command || !commandServerId) return
    if (/[\r\n\u0000]/.test(command)) { setError('Enter one command on a single line.'); return }
    setCommandBusy(true); setError(''); setNotice('')
    try {
      const result = await api<{ ok: boolean; output: string; exitCode: number; timedOut: boolean; truncated: boolean; durationMs: number }>('/api/servers/' + encodeURIComponent(commandServerId) + '/command', {
        method: 'POST', body: JSON.stringify({ command }),
      })
      const server = servers.find(item => item.id === commandServerId)
      setCommandRuns(current => [{
        id: String(Date.now()) + '-' + current.length, serverName: server?.name || 'Server', command,
        output: result.output || '(no output)', exitCode: result.exitCode, timedOut: result.timedOut,
        truncated: result.truncated, durationMs: result.durationMs, ranAt: new Date().toISOString(),
      }, ...current].slice(0, 10))
      setCommandText('')
      setNotice(result.timedOut ? 'Command stopped after the 20-second limit.' : result.exitCode === 0 ? 'Command completed successfully.' : 'Command exited with code ' + result.exitCode + '.')
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Command execution failed')
    } finally { setCommandBusy(false) }
  }

  async function addPool(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError('')
    try {
      await api('/api/network/pools', { method: 'POST', body: JSON.stringify(poolValues) })
      setPoolValues({ name: '', cidr: '', gateway: '', authorized: false })
      setPoolOpen(false); setNotice('Address pool added to inventory. No guest routing has been changed.')
      await loadData()
    } catch (e) { setError(e instanceof Error ? e.message : 'Could not add pool') }
    finally { setBusy(false) }
  }

  async function reserveAddress(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError('')
    try {
      await api('/api/network/allocations', { method: 'POST', body: JSON.stringify(allocationValues) })
      setAllocationValues(v => ({ ...v, targetId: '' }))
      setNotice('Address reserved in IPAM. This does not yet configure it on the target machine.')
      await loadData()
    } catch (e) { setError(e instanceof Error ? e.message : 'Could not reserve address') }
    finally { setBusy(false) }
  }

  async function removeServer(id: string) {
    if (!window.confirm('Remove this server from NovaVPS? The remote machine will not be changed.')) return
    setBusy(true); setError('')
    try {
      await api(`/api/servers/${id}`, { method: 'DELETE' })
      setNotice('Server removed from the NovaVPS inventory.')
      await loadData()
    } catch (e) { setError(e instanceof Error ? e.message : 'Could not remove server') }
    finally { setBusy(false) }
  }

  async function releaseAllocation(id: string) {
    if (!window.confirm('Release this IP reservation?')) return
    setBusy(true)
    try { await api(`/api/network/allocations/${id}`, { method: 'DELETE' }); await loadData(); setNotice('Reservation released.') }
    catch (e) { setError(e instanceof Error ? e.message : 'Could not release reservation') }
    finally { setBusy(false) }
  }

  async function logout() {
    await api('/api/auth/logout', { method: 'POST', body: '{}' }).catch(() => undefined)
    setAuthenticated(false); setServers([]); setPools([]); setAllocations([])
  }

  if (authenticated === null) return <div className="boot-screen"><div className="brand-mark">N</div><span>Loading NovaVPS…</span></div>

  if (!authenticated) return (
    <main className="login-shell">
      <div className="login-glow login-glow-a" /><div className="login-glow login-glow-b" />
      <section className="login-card">
        <div className="brand-row"><div className="brand-mark">N</div><div><b>NovaVPS</b><span>UNIVERSAL SERVER CONTROL</span></div></div>
        <div className="login-kicker"><ShieldCheck size={15} /> PRIVATE ADMIN ACCESS</div>
        <h1>Your servers.<br /><em>One control plane.</em></h1>
        <p className="muted">Sign in to manage the machines you own or are authorized to administer.</p>
        <form onSubmit={handleLogin} className="login-form">
          <label>Administrator password</label>
          <div className="input-icon"><LockKeyhole size={17} /><input autoFocus type="password" value={password} onChange={e => setPassword(e.target.value)} placeholder="Enter your admin password" required /></div>
          {error && <div className="inline-error">{error}</div>}
          <button className="primary-button full-button" disabled={busy || !password}>{busy ? 'Signing in…' : 'Sign in'} <ChevronRight size={17} /></button>
        </form>
        <div className="login-foot"><LockKeyhole size={14} /> Your credentials stay on your NovaVPS backend.</div>
      </section>
      <div className="login-aside"><div className="orbit orbit-one" /><div className="orbit orbit-two" /><div className="aside-code">SSH <span>•</span> KVM <span>•</span> IPv4 <span>•</span> IPv6</div><div className="aside-title">One place to<br /><strong>run your fleet.</strong></div><div className="aside-caption">Connect third-party VPS, Linux servers and authorized hypervisors.</div></div>
    </main>
  )

  return (
    <div className="app-shell">
      <aside className={`sidebar ${mobileMenu ? 'sidebar-open' : ''}`}>
        <div className="brand-row sidebar-brand"><div className="brand-mark">N</div><div><b>NovaVPS</b><span>CONTROL CENTER</span></div><button className="icon-button mobile-close" onClick={() => setMobileMenu(false)} aria-label="Close menu"><X size={18} /></button></div>
        <div className="workspace-label">WORKSPACE</div>
        <div className="workspace-card"><div className="workspace-avatar">A</div><div><b>Administrator</b><span>Personal workspace</span></div><span className="online-dot" /></div>
        <div className="nav-label">MANAGE</div>
        <button className={`nav-item ${page === 'overview' ? 'active' : ''}`} onClick={() => { setPage('overview'); setMobileMenu(false) }}><LayoutDashboard size={18} /> Overview</button>
        <button className={`nav-item ${page === 'servers' ? 'active' : ''}`} onClick={() => { setPage('servers'); setMobileMenu(false) }}><Server size={18} /> Servers <span className="nav-count">{servers.length}</span></button>
        <button className={`nav-item ${page === 'network' ? 'active' : ''}`} onClick={() => { setPage('network'); setMobileMenu(false) }}><Network size={18} /> Networking</button>
        <div className="nav-label nav-label-lower">TOOLS</div>
        <button className={page === 'terminal' ? 'nav-item active' : 'nav-item'} onClick={() => { setPage('terminal'); setMobileMenu(false) }}><TerminalSquare size={18} /> Command runner</button>
        <div className="nav-disabled"><HardDrive size={18} /> File manager <span>SOON</span></div>
        <div className="sidebar-bottom">
          <div className="security-note"><ShieldCheck size={17} /><div><b>Encrypted credentials</b><span>SSH secrets are encrypted at rest.</span></div></div>
          <button className="nav-item logout-item" onClick={logout}><LogOut size={18} /> Sign out</button>
          <div className="version-row"><span>NovaVPS</span><span>v0.4.0-dev</span></div>
        </div>
      </aside>

      {mobileMenu && <button className="mobile-scrim" onClick={() => setMobileMenu(false)} aria-label="Close navigation" />}
      <main className="main-area">
        <header className="topbar">
          <div className="topbar-left"><button className="icon-button menu-toggle" onClick={() => setMobileMenu(true)} aria-label="Open menu"><Menu size={20} /></button><div className="breadcrumb">NovaVPS <ChevronRight size={14} /> <b>{page === 'overview' ? 'Overview' : page === 'servers' ? 'Servers' : page === 'network' ? 'Networking' : 'Command runner'}</b></div></div>
          <div className="topbar-actions"><div className="connection-status"><span className="online-dot" /> API connected</div><button className="icon-button" title="Refresh data" onClick={() => void loadData()}><RefreshCw size={17} className={loading ? 'spin' : ''} /></button><div className="avatar-small">A</div></div>
        </header>

        <div className="page-wrap">
          {(error || notice) && <div className={`toast ${error ? 'toast-error' : 'toast-success'}`}>{error ? <X size={16} /> : <Check size={16} />}{error || notice}<button onClick={() => { setError(''); setNotice('') }} aria-label="Dismiss"><X size={15} /></button></div>}

          {page === 'overview' && <>
            <div className="page-heading"><div><div className="eyebrow">YOUR INFRASTRUCTURE</div><h1>Good to see you, Admin.</h1><p>Here’s what’s happening across your connected machines.</p></div><button className="primary-button" onClick={() => setConnectOpen(true)}><Plus size={17} /> Connect server</button></div>
            <section className="stat-grid">
              <StatCard icon={<Server />} label="Connected servers" value={String(totals.servers)} note="Across all providers" accent="blue" />
              <StatCard icon={<Globe2 />} label="IPv4 reservations" value={String(totals.ipv4)} note="Inventory records only" accent="purple" />
              <StatCard icon={<Network />} label="IPv6 reservations" value={String(totals.ipv6)} note="Inventory records only" accent="green" />
              <StatCard icon={<Database />} label="Address pools" value={String(totals.pools)} note="Provider-authorized prefixes" accent="orange" />
            </section>
            <section className="content-grid">
              <div className="panel-card server-panel"><div className="card-heading"><div><h2>Connected servers</h2><p>Recently added infrastructure</p></div><button className="text-button" onClick={() => setPage('servers')}>View all <ArrowUpRight size={15} /></button></div>
                {servers.length === 0 ? <EmptyState icon={<Server size={24} />} title="No servers connected yet" description="Connect a VPS or Linux machine using SSH. No NovaVPS agent is required for the basic connection." action={<button className="secondary-button" onClick={() => setConnectOpen(true)}><Plus size={15} /> Add your first server</button>} /> :
                  <div className="server-list">{servers.slice(0, 5).map(server => <ServerRow key={server.id} server={server} onMetrics={() => void refreshMetrics(server.id)} metrics={metrics[server.id]} onRemove={() => void removeServer(server.id)} />)}</div>}
              </div>
              <div className="panel-card quick-panel"><div className="card-heading"><div><h2>Quick actions</h2><p>Common management tasks</p></div><Zap size={18} className="muted-icon" /></div>
                <button className="quick-action" onClick={() => setConnectOpen(true)}><span className="quick-icon quick-blue"><Plus size={18} /></span><span><b>Connect a server</b><small>Add a VPS over SSH</small></span><ChevronRight size={17} /></button>
                <button className="quick-action" onClick={() => { setPage('network'); setPoolOpen(true) }}><span className="quick-icon quick-purple"><Globe2 size={18} /></span><span><b>Add IP address pool</b><small>Register an authorized CIDR</small></span><ChevronRight size={17} /></button>
                <div className="notice-box"><ShieldCheck size={17} /><div><b>Host-key verification</b><p>NovaVPS pins each SSH host fingerprint after you verify it independently.</p></div></div>
              </div>
            </section>
            <section className="panel-card deployment-card"><div className="deployment-icon"><Activity size={20} /></div><div><b>Designed for your existing infrastructure</b><p>Connect free-tier or paid VPS, cloud VMs, Linux servers and KVM hosts. Provisioning features appear only when the connected machine has the required permissions.</p></div><span className="deployment-tag">MULTI-PROVIDER</span></section>
          </>}

          {page === 'servers' && <>
            <div className="page-heading"><div><div className="eyebrow">INFRASTRUCTURE</div><h1>Your servers</h1><p>Manage all connected VPS and Linux machines from one place.</p></div><button className="primary-button" onClick={() => setConnectOpen(true)}><Plus size={17} /> Connect server</button></div>
            <div className="filter-row"><div className="filter-search"><Server size={16} /><span>All connected servers</span><b>{servers.length}</b></div><button className="secondary-button" onClick={() => void loadData()}><RefreshCw size={15} /> Refresh</button></div>
            <div className="panel-card full-table">
              {servers.length === 0 ? <EmptyState icon={<Server size={24} />} title="Your server list is empty" description="Connect a Linux server with its SSH host, username and authentication method." action={<button className="primary-button" onClick={() => setConnectOpen(true)}><Plus size={15} /> Connect server</button>} /> :
                <div className="server-list">{servers.map(server => <ServerRow key={server.id} server={server} onMetrics={() => void refreshMetrics(server.id)} metrics={metrics[server.id]} onRemove={() => void removeServer(server.id)} />)}</div>}
            </div>
            <p className="page-footnote"><ShieldCheck size={14} /> SSH host keys are checked on every connection. Removing a server only deletes its NovaVPS record; it does not modify the remote machine.</p>
          </>}

          {page === 'terminal' && <>
            <div className="page-heading"><div><div className="eyebrow">REMOTE SERVER TOOLS</div><h1>Single-line command</h1><p>Run one SSH command on a connected server and view its output.</p></div></div>
            {servers.length === 0 ? <div className="panel-card"><EmptyState icon={<TerminalSquare size={23} />} title="Connect a server first" description="The command runner uses the SSH credentials saved for a server in your inventory." action={<button className="primary-button" onClick={() => setConnectOpen(true)}><Plus size={15} /> Connect server</button>} /></div> : <>
              <section className="panel-card command-panel">
                <div className="card-heading"><div><h2>Command input</h2><p>Runs as the SSH user configured for the selected server.</p></div><span className="count-pill">SSH EXEC</span></div>
                <form className="command-form" onSubmit={e => void executeSingleLineCommand(e)}>
                  <label>Target server<select value={commandServerId} onChange={e => setCommandServerId(e.target.value)} required>{servers.map(server => <option key={server.id} value={server.id}>{server.name} · {server.username}@{server.host}</option>)}</select></label>
                  <label>Command<input type="text" value={commandText} onChange={e => setCommandText(e.target.value)} placeholder="Enter a single-line command…" maxLength={2048} autoComplete="off" spellCheck={false} required /></label>
                  <div className="command-submit-row"><span>One line · 20s timeout · output capped at 48 KiB</span><button className="primary-button" disabled={commandBusy || !commandText.trim()}><TerminalSquare size={16} />{commandBusy ? 'Running…' : 'Run command'}</button></div>
                </form>
                <div className="modal-security command-security"><ShieldCheck size={16} /><span>Only run commands on machines you own or are authorized to administer. Commands execute remotely as the saved SSH user. They are not saved by NovaVPS.</span></div>
              </section>
              <section className="panel-card command-history-panel">
                <div className="card-heading"><div><h2>Recent output</h2><p>Latest 10 commands from this browser session.</p></div><button className="text-button" onClick={() => setCommandRuns([])} disabled={!commandRuns.length}>Clear history</button></div>
                {!commandRuns.length ? <div className="empty-inline">No commands have been run in this session.</div> : <div className="command-history">{commandRuns.map(run => <article className="command-result" key={run.id}>
                  <div className="command-result-head"><span className={run.timedOut || run.exitCode !== 0 ? 'command-failed' : 'command-ok'}>{run.timedOut ? 'TIMEOUT' : 'EXIT ' + run.exitCode}</span><small>{run.serverName} · {run.durationMs} ms · {timeAgo(run.ranAt)}</small></div>
                  <code className="command-echo">$ {run.command}</code>
                  <pre>{run.output}{run.truncated ? '\n… output truncated at 48 KiB' : ''}</pre>
                </article>)}</div>}
              </section>
            </>}
          </>}

          {page === 'network' && <>
            <div className="page-heading"><div><div className="eyebrow">NETWORK CONTROL</div><h1>IP address manager</h1><p>Track provider-assigned IPv4 and IPv6 prefixes and reserve addresses for your resources.</p></div><button className="primary-button" onClick={() => setPoolOpen(true)}><Plus size={17} /> Add address pool</button></div>
            <div className="network-alert"><ShieldCheck size={18} /><div><b>IPAM inventory, not automatic public routing</b><p>A reservation records ownership and intent. It does not configure an IP on a guest. Real public connectivity needs upstream allocation, routes and host-level networking access.</p></div></div>
            <div className="network-grid">
              <div className="panel-card"><div className="card-heading"><div><h2>Address pools</h2><p>Authorized public CIDR ranges</p></div><span className="count-pill">{pools.length} pools</span></div>
                {pools.length === 0 ? <EmptyState icon={<Globe2 size={22} />} title="No address pools" description="Add a CIDR block that your provider or network operator has assigned to you." /> :
                  <div className="pool-list">{pools.map(pool => <div className="pool-item" key={pool.id}><div className="pool-family">{pool.family === 'IPv4' ? '4' : '6'}</div><div className="pool-main"><b>{pool.name}</b><code>{pool.cidr}</code><small>{pool.gateway ? `Gateway: ${pool.gateway}` : 'Gateway not specified'}</small></div><span className="family-tag">{pool.family}</span></div>)}</div>}
              </div>
              <div className="panel-card"><div className="card-heading"><div><h2>Reserve an address</h2><p>Assign an inventory record to a target</p></div><Network size={18} className="muted-icon" /></div>
                <form className="stack-form" onSubmit={reserveAddress}>
                  <label>Address pool<select value={allocationValues.poolId} onChange={e => setAllocationValues(v => ({ ...v, poolId: e.target.value }))} required><option value="">Select a pool</option>{pools.map(pool => <option value={pool.id} key={pool.id}>{pool.name} · {pool.cidr}</option>)}</select></label>
                  <label>Target type<select value={allocationValues.targetType} onChange={e => setAllocationValues(v => ({ ...v, targetType: e.target.value }))}><option value="server">Server</option><option value="vm">Virtual machine</option><option value="container">Container</option><option value="other">Other</option></select></label>
                  <label>Target name or ID<input value={allocationValues.targetId} onChange={e => setAllocationValues(v => ({ ...v, targetId: e.target.value }))} placeholder="e.g. web-vm-01" required /></label>
                  <button className="primary-button full-button" disabled={busy || !pools.length}><Plus size={16} /> Reserve next available IP</button>
                </form>
              </div>
            </div>
            <div className="panel-card allocations-card"><div className="card-heading"><div><h2>IP reservations</h2><p>All addresses reserved from your pools</p></div><span className="count-pill">{allocations.length} records</span></div>
              {allocations.length === 0 ? <div className="empty-inline">No addresses reserved yet.</div> : <div className="table-wrap"><table><thead><tr><th>ADDRESS</th><th>RESOURCE</th><th>TARGET</th><th>CREATED</th><th /></tr></thead><tbody>{allocations.map(item => <tr key={item.id}><td><code className="address-code">{item.address}</code></td><td><span className="resource-pill">{item.targetType}</span></td><td>{item.targetId}</td><td>{timeAgo(item.createdAt)}</td><td><button className="icon-button danger-icon" onClick={() => void releaseAllocation(item.id)} title="Release reservation"><Trash2 size={15} /></button></td></tr>)}</tbody></table></div>}
            </div>
          </>}
        </div>
      </main>

      {connectOpen && <div className="modal-backdrop" onMouseDown={e => { if (e.target === e.currentTarget) closeConnect() }}><section className="modal-card">
        <div className="modal-heading"><div><div className="eyebrow">SSH ENROLLMENT</div><h2>{pendingConnect ? 'Verify SSH host key' : 'Connect a server'}</h2><p>{pendingConnect ? 'Confirm the server identity before NovaVPS sends your credentials.' : 'Connect a machine you own or are authorized to administer.'}</p></div><button className="icon-button" onClick={closeConnect} aria-label="Close"><X size={19} /></button></div>
        {!pendingConnect ? <form className="stack-form" onSubmit={e => void submitConnect(e)}>
          <div className="form-two"><label>Server name<input value={connectValues.name} onChange={e => setConnectValues(v => ({ ...v, name: e.target.value }))} placeholder="Production VPS" required /></label><label>SSH host (IPv4, IPv6 or DNS)<input value={connectValues.host} onChange={e => setConnectValues(v => ({ ...v, host: e.target.value }))} placeholder="server.example.com" required /></label></div>
          <div className="form-two"><label>SSH port<input type="number" min="1" max="65535" value={connectValues.port} onChange={e => setConnectValues(v => ({ ...v, port: Number(e.target.value) }))} required /></label><label>SSH username<input value={connectValues.username} onChange={e => setConnectValues(v => ({ ...v, username: e.target.value }))} placeholder="ubuntu" required /></label></div>
          <label>Authentication method<select value={connectValues.authMethod} onChange={e => setConnectValues(v => ({ ...v, authMethod: e.target.value }))}><option value="privateKey">SSH private key (recommended)</option><option value="password">SSH password</option></select></label>
          {connectValues.authMethod === 'privateKey' ? <><label>Private key<textarea rows={5} value={connectValues.privateKey} onChange={e => setConnectValues(v => ({ ...v, privateKey: e.target.value }))} placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" required /></label><label>Key passphrase (if required)<input type="password" value={connectValues.keyPassphrase} onChange={e => setConnectValues(v => ({ ...v, keyPassphrase: e.target.value }))} /></label></> : <label>SSH password<input type="password" value={connectValues.password} onChange={e => setConnectValues(v => ({ ...v, password: e.target.value }))} required /></label>}
          <div className="modal-security"><KeyRound size={16} /><span>Credentials are encrypted on the backend. The browser does not save them in local storage.</span></div>
          <div className="modal-actions"><button type="button" className="secondary-button" onClick={closeConnect}>Cancel</button><button className="primary-button" disabled={busy}>{busy ? 'Checking SSH…' : 'Verify server identity'} <ChevronRight size={16} /></button></div>
        </form> : <div className="fingerprint-step">
          <div className="fingerprint-icon"><ShieldCheck size={25} /></div>
          <div className="fingerprint-warning"><b>Verify this fingerprint independently</b><p>Compare it with the SSH host-key fingerprint shown by your VPS provider console or another trusted channel. Do not approve a fingerprint you cannot verify.</p></div>
          <code className="fingerprint-code">{pendingConnect.fingerprint}</code>
          <label className="check-row"><input type="checkbox" checked={fingerprintVerified} onChange={e => setFingerprintVerified(e.target.checked)} /><span>I verified this exact fingerprint using a trusted source.</span></label>
          <div className="modal-actions"><button className="secondary-button" onClick={() => { setPendingConnect(null); setFingerprintVerified(false) }}>Back</button><button className="primary-button" disabled={busy || !fingerprintVerified} onClick={() => void submitConnect(undefined, true)}>{busy ? 'Connecting…' : 'Confirm and connect'} <Check size={16} /></button></div>
        </div>}
      </section></div>}

      {poolOpen && <div className="modal-backdrop" onMouseDown={e => { if (e.target === e.currentTarget) setPoolOpen(false) }}><section className="modal-card modal-small">
        <div className="modal-heading"><div><div className="eyebrow">IP ADDRESS MANAGEMENT</div><h2>Add an address pool</h2><p>Only register public ranges allocated to you.</p></div><button className="icon-button" onClick={() => setPoolOpen(false)} aria-label="Close"><X size={19} /></button></div>
        <form className="stack-form" onSubmit={addPool}>
          <label>Pool name<input value={poolValues.name} onChange={e => setPoolValues(v => ({ ...v, name: e.target.value }))} placeholder="Provider IPv6 range" required /></label>
          <label>CIDR prefix<input value={poolValues.cidr} onChange={e => setPoolValues(v => ({ ...v, cidr: e.target.value }))} placeholder="2001:db8:1234::/48" required /></label>
          <label>Gateway (optional)<input value={poolValues.gateway} onChange={e => setPoolValues(v => ({ ...v, gateway: e.target.value }))} placeholder="2001:db8:1234::1" /></label>
          <label className="check-row"><input type="checkbox" checked={poolValues.authorized} onChange={e => setPoolValues(v => ({ ...v, authorized: e.target.checked }))} /><span>I confirm this address range is assigned to me and I am authorized to manage it.</span></label>
          <div className="modal-security"><Globe2 size={16} /><span>Address registration creates an inventory pool. It does not configure upstream routing or guest networking.</span></div>
          <div className="modal-actions"><button type="button" className="secondary-button" onClick={() => setPoolOpen(false)}>Cancel</button><button className="primary-button" disabled={busy || !poolValues.authorized}>{busy ? 'Saving…' : 'Add pool'}</button></div>
        </form>
      </section></div>}
    </div>
  )
}

function StatCard({ icon, label, value, note, accent }: { icon: ReactNode; label: string; value: string; note: string; accent: string }) {
  return <div className="stat-card"><div className={`stat-icon ${accent}`}>{icon}</div><div className="stat-label">{label}</div><div className="stat-value">{value}</div><div className="stat-note">{note}</div></div>
}
function EmptyState({ icon, title, description, action }: { icon: React.ReactNode; title: string; description: string; action?: ReactNode }) {
  return <div className="empty-state"><div className="empty-icon">{icon}</div><b>{title}</b><p>{description}</p>{action}</div>
}
function ServerRow({ server, metrics, onMetrics, onRemove }: { server: ServerRecord; metrics?: Metrics; onMetrics: () => void; onRemove: () => void }) {
  return <div className="server-row"><div className="server-avatar"><Server size={19} /></div><div className="server-info"><b>{server.name}</b><span>{server.username}@{server.host}:{server.port}</span><small><span className="online-dot" /> SSH enrolled · {timeAgo(server.addedAt)}</small>{metrics && <div className="metrics-strip"><span><Cpu size={13} /> {metrics.cpu_cores || '—'} cores</span><span><Activity size={13} /> RAM {bytes(metrics.mem_available)} free</span><span><HardDrive size={13} /> {bytes(metrics.disk_used)} used</span><span>Up {metrics.uptime ? `${Math.floor(Number(metrics.uptime) / 3600)}h` : '—'}</span></div>}</div><div className="server-actions"><span className="status-pill"><span className="online-dot" /> Saved</span><button className="secondary-button compact-button" onClick={onMetrics}><Activity size={14} /> Check metrics</button><button className="icon-button danger-icon" onClick={onRemove} title="Remove server"><Trash2 size={15} /></button></div></div>
}
export default App
