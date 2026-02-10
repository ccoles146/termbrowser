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
    const ctrs = (items || []).filter(c => c.type !== 'node');

    // Nodes section
    if (nodes.length > 0) {
        const label = document.createElement('div');
        label.className = 'sidebar-section-label';
        label.textContent = 'Nodes';
        sidebarItems.appendChild(label);
        nodes.forEach(c => {
            const el = makeSidebarItem(c.ctid, c.name || c.ctid, c.status, null);
            sidebarItems.appendChild(el);
        });
    }

    // Containers section
    if (ctrs.length > 0) {
        const label = document.createElement('div');
        label.className = 'sidebar-section-label';
        label.textContent = 'Containers';
        sidebarItems.appendChild(label);
        ctrs.forEach(c => {
            const el = makeSidebarItem(c.ctid, c.name || c.ctid, c.status, c.ctid);
            sidebarItems.appendChild(el);
        });
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

function disconnectTerminal() {
    if (ws) {
        ws.onclose = null;
        ws.close();
        ws = null;
    }
}

function connectTerminal(id) {
    disconnectTerminal();
    currentId = id;
    setActiveItem(id);

    const label = id === 'host' ? 'Proxmox Host' : 'Container ' + id;
    terminalTitle.textContent = label;

    if (term) {
        term.reset();
        term.write('\x1b[?25h'); // show cursor
    }

    const proto = location.protocol === 'https:' ? 'wss' : 'ws';
    const url = `${proto}://${location.host}/ws/terminal/${id}`;
    ws = new WebSocket(url);
    ws.binaryType = 'arraybuffer';

    ws.onopen = () => {
        sendResize();
        if (term) {
            term.onData(data => {
                if (ws && ws.readyState === WebSocket.OPEN) {
                    // Send keyboard input as binary
                    const encoded = new TextEncoder().encode(data);
                    ws.send(encoded);
                }
            });
        }
    };

    ws.onmessage = (event) => {
        if (!term) return;
        if (event.data instanceof ArrayBuffer) {
            term.write(new Uint8Array(event.data));
        } else {
            term.write(event.data);
        }
    };

    ws.onerror = () => {
        if (term) {
            term.write('\r\n\x1b[31mConnection error.\x1b[0m\r\n');
        }
    };

    ws.onclose = () => {
        if (term && currentId === id) {
            term.write('\r\n\x1b[33m[disconnected]\x1b[0m\r\n');
        }
    };
}

// ─── Start ───────────────────────────────────────────────────────────────────
init();
