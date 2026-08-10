<section id="modd-status" aria-live="polite">
    <p><strong>Hosting status:</strong> <span data-field="phase">Loading…</span></p>
    <p data-field="message"></p>
    <p data-field="warning" class="alert alert-danger" hidden>No healthy instance is receiving traffic.</p>
    <table class="table table-striped">
        <thead><tr><th>Deployment</th><th>Version</th><th>Runtime</th><th>Health</th><th>Traffic</th></tr></thead>
        <tbody></tbody>
    </table>
    <small>Live monitor: <span data-field="connection">connecting</span></small>
</section>
<script type="application/json" id="modd-initial">{$controllerStatusJSON nofilter}</script>
<script type="application/json" id="modd-token">{$monitorTokenJSON nofilter}</script>
<script type="application/json" id="modd-expiry">{$monitorExpiryJSON nofilter}</script>
<script type="application/json" id="modd-ws-url">{$monitorUrlJSON nofilter}</script>
{literal}<script>
(() => {
    const root = document.getElementById('modd-status');
    const read = id => JSON.parse(document.getElementById(id).textContent);
    const initial = read('modd-initial'), token = read('modd-token'), expires = Date.parse(read('modd-expiry')), url = read('modd-ws-url');
    let delay = 1000, finished = false;
    const set = (name, value) => { root.querySelector(`[data-field="${name}"]`).textContent = value || 'unknown'; };
    function render(service) {
        set('phase', service.phase || service.state);
        set('message', service.message);
        const body = root.querySelector('tbody');
        body.replaceChildren();
        let healthyTraffic = false;
        for (const slot of ['blue', 'green']) {
            const deploy = (service.deployments || {})[slot] || {};
            healthyTraffic ||= deploy.receives_traffic === true && deploy.health === 'healthy';
            const row = document.createElement('tr');
            for (const value of [slot, deploy.version || '—', deploy.runtime || 'absent', deploy.health || 'unknown', deploy.receives_traffic ? 'yes' : 'no']) {
                const cell = document.createElement('td'); cell.textContent = value; row.appendChild(cell);
            }
            body.appendChild(row);
        }
        root.querySelector('[data-field="warning"]').hidden = service.state !== 'active' || healthyTraffic;
        finished = service.phase === 'deleted';
    }
    function connect() {
        if (!token || !url || !Number.isFinite(expires) || Date.now() >= expires) { set('connection', 'refresh required'); return; }
        set('connection', delay === 1000 ? 'connecting' : 'reconnecting');
        const socket = new WebSocket(url, ['modd-monitor', token]);
        socket.onopen = () => { delay = 1000; set('connection', 'live'); };
        socket.onmessage = event => { const snapshot = JSON.parse(event.data); if (snapshot.type === 'status') render(snapshot.service); };
        socket.onclose = () => {
            if (finished) { set('connection', 'complete'); return; }
            if (Date.now() >= expires) { set('connection', 'refresh required'); return; }
            set('connection', 'reconnecting'); setTimeout(connect, delay); delay = Math.min(delay * 2, 30000);
        };
    }
    render(initial); connect();
})();
</script>{/literal}
