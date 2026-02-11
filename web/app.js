'use strict';

let term = null;
let fitAddon = null;
let ws = null;
let currentId = null;

const loginScreen = document.getElementById('login-screen');
const appScreen = document.getElementById('app-screen');
const loginForm = document.getElementById('login-form');
const loginError = document.getElementById('login-error');
const sidebarItems = document.getElementById('sidebar-items');
const terminalTitle = document.getElementById('terminal-title');
const terminalContainer = document.getElementById('terminal-container');
const btnLogout = document.getElementById('btn-logout');

// ─── Init ────────────────────────────────────────────────────────────────────

async function init() {
    // Check if already authenticated
    try {
        const res = await fetch('/api/containers');
        if (res.ok) {
            const containers = await res.json();
            showApp(containers);
            return;
        }
    } catch (_) {}
    showLogin();
}

// ─── Login ───────────────────────────────────────────────────────────────────

function showLogin() {
    loginScreen.style.display = 'flex';
    appScreen.classList.remove('visible');
}

loginForm.addEventListener('submit', async (e) => {
    e.preventDefault();
    loginError.textContent = '';

    const password = document.getElementById('password').value;
    const totp_code = document.getElementById('totp').value;

    try {
        const res = await fetch('/api/login', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ password, totp_code }),
        });
        if (!res.ok) {
            loginError.textContent = 'Invalid password or authenticator code.';
            return;
        }
        const containersRes = await fetch('/api/containers');
        const containers = containersRes.ok ? await containersRes.json() : [];
        showApp(containers);
    } catch (err) {
        loginError.textContent = 'Connection error. Please try again.';
    }
});

// ─── Logout ──────────────────────────────────────────────────────────────────

btnLogout.addEventListener('click', async () => {
    disconnectTerminal();
    await fetch('/api/logout', { method: 'POST' });
    showLogin();
});

// ─── App ─────────────────────────────────────────────────────────────────────

function showApp(containers) {
    loginScreen.style.display = 'none';
    appScreen.classList.add('visible');

    renderSidebar(containers);
    initTerminal();
    connectTerminal('host');
}

function renderSidebar(items) {
    sidebarItems.innerHTML = '';

    // Host entry (always first)
    const hostEl = makeSidebarItem('host', 'Proxmox Host', 'running', null);
    sidebarItems.appendChild(hostEl);

    const nodes = (items || []).filter(c => c.type === 'node');
    const lxcs  = (items || []).filter(c => c.type === 'lxc');
    const vms   = (items || []).filter(c => c.type === 'qemu');

    function appendSection(label, list) {
        const labelEl = document.createElement('div');
        labelEl.className = 'sidebar-section-label';
        labelEl.textContent = label;
        sidebarItems.appendChild(labelEl);
        list.forEach(c => sidebarItems.appendChild(c));
    }

    if (nodes.length > 0) {
        appendSection('Nodes', nodes.map(c =>
            makeSidebarItem(c.ctid, c.name || c.ctid, c.status, null)
        ));
    }
    if (lxcs.length > 0) {
        appendSection('Containers', lxcs.map(c =>
            makeSidebarItem(c.ctid, c.name || c.vmid || c.ctid, c.status, c.vmid || c.ctid)
        ));
    }
    if (vms.length > 0) {
        appendSection('Virtual Machines', vms.map(c =>
            makeSidebarItem(c.ctid, c.name || c.vmid || c.ctid, c.status, c.vmid || c.ctid)
        ));
    }
}

function makeSidebarItem(id, name, status, ctid) {
    const isActive = status === 'running' || status === 'online';
    const el = document.createElement('div');
    el.className = 'sidebar-item' + (isActive ? '' : ' stopped');
    el.dataset.id = id;

    const dot = document.createElement('span');
    dot.className = 'status-dot ' + (isActive ? 'running' : 'stopped');
    el.appendChild(dot);

    const nameEl = document.createElement('span');
    nameEl.className = 'item-name';
    nameEl.textContent = name;
    el.appendChild(nameEl);

    if (ctid) {
        const ctidEl = document.createElement('span');
        ctidEl.className = 'item-ctid';
        ctidEl.textContent = ctid;
        el.appendChild(ctidEl);
    }

    el.addEventListener('click', () => connectTerminal(id));
    return el;
}

function setActiveItem(id) {
    document.querySelectorAll('.sidebar-item').forEach(el => {
        el.classList.toggle('active', el.dataset.id === id);
    });
}

// ─── Terminal ────────────────────────────────────────────────────────────────

function initTerminal() {
    if (term) {
        term.dispose();
    }

    term = new Terminal({
        fontFamily: '"Cascadia Code", "Fira Code", "JetBrains Mono", "Courier New", monospace',
        fontSize: 14,
        theme: {
            background: '#000000',
            foreground: '#e0e0e0',
            cursor: '#00ff88',
            selectionBackground: 'rgba(0,255,136,0.3)',
            black: '#000000',
            brightBlack: '#555555',
            red: '#ff5555',
            brightRed: '#ff6e6e',
            green: '#50fa7b',
            brightGreen: '#69ff94',
            yellow: '#f1fa8c',
            brightYellow: '#ffffa5',
            blue: '#6272a4',
            brightBlue: '#d6acff',
            magenta: '#ff79c6',
            brightMagenta: '#ff92df',
            cyan: '#8be9fd',
            brightCyan: '#a4ffff',
            white: '#bfbfbf',
            brightWhite: '#ffffff',
        },
        cursorBlink: true,
        scrollback: 5000,
        allowTransparency: false,
    });

    fitAddon = new FitAddon.FitAddon();
    term.loadAddon(fitAddon);
    term.open(terminalContainer);
    fitAddon.fit();

    // Register input handler once — ws is a module-level variable so it always
    // refers to the current connection without accumulating extra listeners.
    term.onData(data => {
        if (ws && ws.readyState === WebSocket.OPEN) {
            // Filter terminal query responses (DA1/DA2) that xterm.js emits
            // in response to escape sequences from the server. Without this,
            // responses like ESC[?0;276;0c leak back to the PTY as garbage.
            const filtered = data.replace(/\x1b\[[\?>\d;]*c/g, '');
            if (filtered) {
                console.log(`[INPUT] sending ${filtered.length} byte(s) via WS#${ws._seq} to ${currentId}`);
                ws.send(new TextEncoder().encode(filtered));
            }
        } else {
            console.warn(`[INPUT] dropped ${data.length} byte(s): ws=${ws ? 'exists' : 'null'} readyState=${ws ? ws.readyState : 'N/A'}`);
        }
    });

    // Remove any previously registered listener to prevent accumulation
    // across multiple initTerminal() calls (e.g. login → logout → login).
    window.removeEventListener('resize', onWindowResize);
    window.addEventListener('resize', onWindowResize);
}

function onWindowResize() {
    if (!fitAddon || !term) return;
    fitAddon.fit();
    sendResize();
}

function sendResize() {
    if (ws && ws.readyState === WebSocket.OPEN && term) {
        ws.send(JSON.stringify({
            type: 'resize',
            cols: term.cols,
            rows: term.rows,
        }));
    }
}

let wsSeq = 0; // client-side WebSocket sequence counter

function disconnectTerminal() {
    if (ws) {
        console.log(`[WS] disconnectTerminal: closing WS#${ws._seq} for ${currentId}`);
        ws.onmessage = null;
        ws.onclose = null;
        ws.onerror = null;
        ws.close();
        ws = null;
    }
}

function connectTerminal(id) {
    console.log(`[WS] connectTerminal(${id}): starting`);
    disconnectTerminal();
    currentId = id;
    setActiveItem(id);

    let label;
    if (id === 'host') {
        label = 'Proxmox Host';
    } else if (id.startsWith('node:')) {
        label = 'Node ' + id.slice(5);
    } else {
        const item = document.querySelector('.sidebar-item[data-id="' + id + '"] .item-name');
        label = item ? item.textContent : id;
    }
    terminalTitle.textContent = label;

    if (term) {
        term.reset();
        term.write('\x1b[?25h'); // show cursor
    }

    wsSeq++;
    const mySeq = wsSeq;
    const proto = location.protocol === 'https:' ? 'wss' : 'ws';
    const url = `${proto}://${location.host}/ws/terminal/${id}`;
    console.log(`[WS] connectTerminal(${id}): creating WS#${mySeq} → ${url}`);
    ws = new WebSocket(url);
    ws._seq = mySeq;
    ws.binaryType = 'arraybuffer';

    ws.onopen = () => {
        console.log(`[WS] WS#${mySeq} (${id}): onopen`);
        sendResize();
    };

    ws.onmessage = (event) => {
        if (!term) return;
        if (event.data instanceof ArrayBuffer) {
            term.write(new Uint8Array(event.data));
        } else {
            term.write(event.data);
        }
    };

    ws.onerror = (e) => {
        console.error(`[WS] WS#${mySeq} (${id}): onerror`, e);
        if (term) {
            term.write('\r\n\x1b[31mConnection error.\x1b[0m\r\n');
        }
    };

    ws.onclose = (e) => {
        console.log(`[WS] WS#${mySeq} (${id}): onclose code=${e.code} reason=${e.reason} currentId=${currentId}`);
        if (term && currentId === id) {
            term.write('\r\n\x1b[33m[disconnected]\x1b[0m\r\n');
        }
    };
}

// ─── Start ───────────────────────────────────────────────────────────────────
init();
