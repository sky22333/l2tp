class StateManager {
    constructor() {
        this.state = {
            servers: new Map(),
            trafficStats: {},
            systemStatus: {},
            ui: {
                modals: {},
                operations: new Map()
            }
        };
        this.subscribers = new Map();
        this.version = 0;
        this.operationQueue = new Map();
    }

    subscribe(key, callback) {
        if (!this.subscribers.has(key)) {
            this.subscribers.set(key, new Set());
        }
        this.subscribers.get(key).add(callback);
        return () => this.subscribers.get(key)?.delete(callback);
    }

    // 获取状态
    getState(key = null) {
        return key ? this.state[key] : this.state;
    }

    // 更新状态
    setState(key, value, timestamp = Date.now()) {
        const currentTimestamp = this.state[key]?._timestamp || 0;
        if (timestamp >= currentTimestamp) {
            this.state[key] = { ...value, _timestamp: timestamp };
            this.version++;
            this.notifySubscribers(key, this.state[key]);
        }
    }

    updateServer(serverId, updates, timestamp = Date.now()) {
        const current = this.state.servers.get(serverId) || {};
        const currentTimestamp = current._timestamp || 0;
        
        if (timestamp >= currentTimestamp) {
            const newState = { 
                ...current, 
                ...updates, 
                _timestamp: timestamp 
            };
            this.state.servers.set(serverId, newState);
            this.notifySubscribers('servers', this.state.servers);
            this.notifySubscribers(`server_${serverId}`, newState);
        }
    }

    optimisticUpdate(key, value, rollbackDelay = 10000) {
        const originalValue = this.state[key];
        this.setState(key, { ...value, _optimistic: true });
        
        setTimeout(() => {
            if (this.state[key]?._optimistic) {
                this.setState(key, originalValue);
            }
        }, rollbackDelay);
    }

    confirmOptimistic(key, value) {
        this.setState(key, { ...value, _optimistic: false });
    }

    notifySubscribers(key, value) {
        const subscribers = this.subscribers.get(key);
        if (subscribers) {
            subscribers.forEach(callback => {
                try {
                    callback(value, key);
                } catch (error) {
                    console.error('状态订阅回调错误:', error);
                }
            });
        }
    }

    async enqueueOperation(serverId, operation, operationType = 'default') {
        const key = `${serverId}_${operationType}`;
        
        if (this.operationQueue.has(key)) {
            await this.operationQueue.get(key);
        }

        const operationPromise = this.executeOperation(serverId, operation, operationType);
        this.operationQueue.set(key, operationPromise);

        try {
            const result = await operationPromise;
            return result;
        } finally {
            this.operationQueue.delete(key);
        }
    }

    async executeOperation(serverId, operation, operationType) {
        this.updateServer(serverId, { 
            [`${operationType}_pending`]: true 
        });

        try {
            const result = await operation();
            this.updateServer(serverId, { 
                [`${operationType}_pending`]: false 
            });
            return result;
        } catch (error) {
            this.updateServer(serverId, { 
                [`${operationType}_pending`]: false,
                [`${operationType}_error`]: error.message 
            });
            throw error;
        }
    }
}

class SmartWebSocket {
    constructor(url, stateManager) {
        this.url = url;
        this.stateManager = stateManager;
        this.ws = null;
        this.reconnectAttempts = 0;
        this.maxReconnectAttempts = 10;
        this.reconnectDelay = 1000;
        this.isConnected = false;
        this.messageQueue = [];
        
        this.connect();
    }

    connect() {
        try {
            this.ws = new WebSocket(this.url);
            
            this.ws.onopen = () => {
                console.log('WebSocket连接成功');
                this.isConnected = true;
                this.reconnectAttempts = 0;
                this.processMessageQueue();
                this.stateManager.setState('websocket', { connected: true });
            };

            this.ws.onmessage = (event) => {
                this.handleMessage(JSON.parse(event.data));
            };

            this.ws.onclose = () => {
                console.log('WebSocket连接关闭');
                this.isConnected = false;
                this.stateManager.setState('websocket', { connected: false });
                this.scheduleReconnect();
            };

            this.ws.onerror = (error) => {
                console.error('WebSocket错误:', error);
            };

        } catch (error) {
            console.error('WebSocket连接失败:', error);
            this.scheduleReconnect();
        }
    }

    scheduleReconnect() {
        if (this.reconnectAttempts < this.maxReconnectAttempts) {
            this.reconnectAttempts++;
            const delay = this.reconnectDelay * Math.pow(2, this.reconnectAttempts - 1);
            console.log(`${delay}ms后尝试重连 (${this.reconnectAttempts}/${this.maxReconnectAttempts})`);
            
            setTimeout(() => {
                this.connect();
            }, delay);
        }
    }

    handleMessage(data) {
        const timestamp = Date.now();
        
        switch (data.type) {
            case 'server_status':
                this.stateManager.updateServer(data.server_id, {
                    status: data.status,
                    message: data.message
                }, timestamp);
                break;
            case 'server_created':
                if (data.data) {
                    this.stateManager.updateServer(data.data.id, data.data, timestamp);
                }
                break;
            case 'server_updated':
                if (data.data) {
                    this.stateManager.updateServer(data.data.id, data.data, timestamp);
                }
                break;
        }
    }

    processMessageQueue() {
        while (this.messageQueue.length > 0) {
            const message = this.messageQueue.shift();
            this.send(message);
        }
    }

    send(message) {
        if (this.isConnected && this.ws.readyState === WebSocket.OPEN) {
            this.ws.send(JSON.stringify(message));
        } else {
            this.messageQueue.push(message);
        }
    }

    disconnect() {
        if (this.ws) {
            this.ws.close();
        }
    }
}

class UIRenderer {
    constructor(stateManager) {
        this.stateManager = stateManager;
        this.lastRenderState = new Map();
        
        this.stateManager.subscribe('servers', () => this.renderServers());
        this.stateManager.subscribe('trafficStats', () => this.renderTrafficStats());
        this.stateManager.subscribe('systemStatus', () => this.renderSystemStatus());
    }

    shouldRender(key, newState) {
        const lastState = this.lastRenderState.get(key);
        const stateChanged = JSON.stringify(lastState) !== JSON.stringify(newState);
        if (stateChanged) {
            this.lastRenderState.set(key, JSON.parse(JSON.stringify(newState)));
        }
        return stateChanged;
    }

    renderServers() {
        const servers = Array.from(this.stateManager.getState('servers').values())
            .filter(server => !server._timestamp || server._timestamp > 0);
            
        if (!this.shouldRender('servers', servers)) return;

        const tbody = document.getElementById('serversTableBody');
        if (!tbody) return;

        if (servers.length === 0) {
            tbody.innerHTML = '<tr><td colspan="7" class="text-center">暂无服务器</td></tr>';
            return;
        }

        tbody.innerHTML = servers.map(server => this.renderServerRow(server)).join('');
    }

    renderServerRow(server) {
        const isExpired = server.expire_date && new Date(server.expire_date) < new Date();
        const isOperationPending = server.start_pending || server.stop_pending || server.restart_pending;
        
        return `
            <tr data-server-id="${server.id}" ${isExpired ? 'class="expired"' : ''}>
                <td>${server.id}</td>
                <td>${this.escapeHtml(server.name)}</td>
                <td>${this.escapeHtml(server.host)}:${server.port || 22}</td>
                <td>${server.l2tp_port}</td>
                <td>
                    <span class="status status-${server.status}">${this.getStatusText(server.status)}</span>
                    ${server.message ? `<small class="status-message">${this.escapeHtml(server.message)}</small>` : ''}
                </td>
                <td>${server.expire_date ? this.formatDate(server.expire_date) : '无限期'}</td>
                <td>
                    <div class="btn-group">
                        ${this.renderActionButtons(server, isOperationPending)}
                    </div>
                </td>
            </tr>
        `;
    }

    renderActionButtons(server, isPending) {
        const buttons = [];
        
        if (server.status === 'stopped' && !isPending) {
            buttons.push(`<button class="btn btn-success btn-sm" onclick="l2tpManager.startServer(${server.id})">启动</button>`);
        } else if (server.status === 'running' && !isPending) {
            buttons.push(`<button class="btn btn-warning btn-sm" onclick="l2tpManager.stopServer(${server.id})">停止</button>`);
            buttons.push(`<button class="btn btn-info btn-sm" onclick="l2tpManager.restartServer(${server.id})">重启</button>`);
        } else if (isPending) {
            buttons.push(`<button class="btn btn-secondary btn-sm" disabled>操作中...</button>`);
        }
        
        buttons.push(`<button class="btn btn-info btn-sm" onclick="l2tpManager.viewServer(${server.id})">查看</button>`);
        buttons.push(`<button class="btn btn-secondary btn-sm" onclick="l2tpManager.showServerLogs(${server.id})">日志</button>`);
        buttons.push(`<button class="btn btn-danger btn-sm" onclick="l2tpManager.deleteServer(${server.id})">删除</button>`);
        
        return buttons.join('');
    }

    renderTrafficStats() {
        const stats = this.stateManager.getState('trafficStats');
        if (!this.shouldRender('trafficStats', stats)) return;

        const container = document.getElementById('trafficStatsContainer');
        if (!container) return;

        if (!stats || Object.keys(stats).length === 0) {
            container.innerHTML = '<p>暂无流量数据</p>';
            return;
        }

        const totalBytes = Object.values(stats).reduce((sum, stat) => sum + (stat.bytes_sent || 0) + (stat.bytes_received || 0), 0);
        
        container.innerHTML = `
            <div class="stats-summary">
                <div class="stat-item">
                    <h4>总流量</h4>
                    <p>${this.formatBytes(totalBytes)}</p>
                </div>
                <div class="stat-item">
                    <h4>活跃连接</h4>
                    <p>${Object.keys(stats).length}</p>
                </div>
            </div>
        `;
    }

    renderSystemStatus() {
        const status = this.stateManager.getState('systemStatus');
        if (!this.shouldRender('systemStatus', status)) return;

        const container = document.getElementById('systemStatusContainer');
        if (!container) return;

        if (!status || Object.keys(status).length === 0) {
            container.innerHTML = '<p>加载中...</p>';
            return;
        }

        container.innerHTML = `
            <div class="system-info">
                <div class="info-item">
                    <strong>系统状态:</strong> ${status.status || '正常'}
                </div>
                <div class="info-item">
                    <strong>活跃服务器:</strong> ${status.running_servers || 0}
                </div>
                <div class="info-item">
                    <strong>总服务器:</strong> ${status.total_servers || 0}
                </div>
                <div class="info-item">
                    <strong>服务器信息:</strong> ${status.ip || '未知'} - ${status.location || '未知'}
                </div>
            </div>
        `;
    }

    getStatusText(status) {
        const statusMap = {
            'running': '运行中',
            'stopped': '已停止',
            'starting': '启动中',
            'stopping': '停止中',
            'error': '错误',
            'restarting': '重启中'
        };
        return statusMap[status] || status;
    }

    formatBytes(bytes) {
        if (bytes === 0) return '0 Bytes';
        const k = 1024;
        const sizes = ['Bytes', 'KB', 'MB', 'GB'];
        const i = Math.floor(Math.log(bytes) / Math.log(k));
        return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
    }

    formatDate(dateString) {
        return new Date(dateString).toLocaleDateString('zh-CN');
    }

    escapeHtml(text) {
        const div = document.createElement('div');
        div.textContent = text;
        return div.innerHTML;
    }
}

class L2TPManager {
    constructor() {
        this.token = localStorage.getItem('l2tp_token') || '';
        this.apiBase = '/api';
        this.isLoggedIn = false;
        
        this.stateManager = new StateManager();
        this.uiRenderer = new UIRenderer(this.stateManager);
        this.smartWebSocket = null;
        this.updateInterval = null;
        
        this.init();
    }

    async init() {
        if (this.token && await this.validateToken()) {
            this.isLoggedIn = true;
            this.showDashboard();
            this.loadData();
            this.startPeriodicUpdates();
        } else {
            this.showLogin();
        }
        
        this.setupEventListeners();
    }

    async validateToken() {
        try {
            const response = await this.apiRequest('/system/status', 'GET');
            return response.success;
        } catch (error) {
            console.error('Token验证失败:', error);
            return false;
        }
    }

    async login(username, password) {
        try {
            const response = await fetch(`${this.apiBase}/auth/login`, {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json'
                },
                body: JSON.stringify({ username, password })
            });
            
            const data = await response.json();
            
            if (data.success) {
                this.token = data.token;
                localStorage.setItem('l2tp_token', this.token);
                this.isLoggedIn = true;
                this.showDashboard();
                this.loadData();
                this.startPeriodicUpdates();
                return { success: true };
            } else {
                return { success: false, message: data.message };
            }
        } catch (error) {
            console.error('登录错误:', error);
            return { success: false, message: '登录失败，请检查网络连接' };
        }
    }

    logout() {
        this.token = '';
        localStorage.removeItem('l2tp_token');
        this.isLoggedIn = false;
        this.showLogin();
        
        this.disconnectWebSocket();
        
        if (this.updateInterval) {
            clearInterval(this.updateInterval);
            this.updateInterval = null;
        }
    }

    async apiRequest(endpoint, method = 'GET', data = null) {
        const config = {
            method,
            headers: {
                'Content-Type': 'application/json',
                'Authorization': `Bearer ${this.token}`
            }
        };

        if (data) {
            config.body = JSON.stringify(data);
        }

        const response = await fetch(`${this.apiBase}${endpoint}`, config);
        
        if (response.status === 401) {
            this.logout();
            throw new Error('未授权访问');
        }
        
        return await response.json();
    }

    // 界面显示控制
    showLogin() {
        document.getElementById('loginSection').style.display = 'block';
        document.getElementById('dashboardSection').style.display = 'none';
        document.getElementById('loginError').textContent = '';
    }

    showDashboard() {
        document.getElementById('loginSection').style.display = 'none';
        document.getElementById('dashboardSection').style.display = 'block';
        
        this.connectWebSocket();
    }

    async loadData() {
        try {
            await Promise.all([
                this.loadServers(),
                this.loadTrafficStats(),
                this.loadSystemStatus()
            ]);
        } catch (error) {
            console.error('加载数据失败:', error);
            this.showMessage('加载数据失败: ' + error.message, 'error');
        }
    }

    async loadServers() {
        try {
            const response = await this.apiRequest('/servers');
            if (response.success && response.data) {
                const timestamp = Date.now();
                response.data.forEach(server => {
                    this.stateManager.updateServer(server.id, server, timestamp);
                });
            }
        } catch (error) {
            console.error('加载服务器列表失败:', error);
        }
    }

    async loadTrafficStats() {
        try {
            const response = await this.apiRequest('/traffic/stats');
            if (response.success) {
                this.stateManager.setState('trafficStats', response.data);
            }
        } catch (error) {
            console.error('加载流量统计失败:', error);
        }
    }

    async loadSystemStatus() {
        try {
            const response = await this.apiRequest('/system/status');
            if (response.success) {
                this.stateManager.setState('systemStatus', response.data);
            }
        } catch (error) {
            console.error('加载系统状态失败:', error);
        }
    }

    async startServer(id) {
        if (!confirm('确定要启动这个服务器吗？')) return;
        
        try {
            await this.stateManager.enqueueOperation(id, async () => {
                this.stateManager.updateServer(id, { status: 'starting' });
                this.showMessage('正在启动服务器...', 'info');
                
            const response = await this.apiRequest(`/servers/${id}/start`, 'POST');
            if (response.success) {
                    this.showMessage('启动请求已发送，状态将实时更新', 'success');
                    return response;
            } else {
                    this.stateManager.updateServer(id, { status: 'stopped' });
                    throw new Error(response.message);
            }
            }, 'start');
        } catch (error) {
            this.showMessage('启动失败: ' + error.message, 'error');
        }
    }

    async stopServer(id) {
        if (!confirm('确定要停止这个服务器吗？')) return;
        
        try {
            await this.stateManager.enqueueOperation(id, async () => {
                this.stateManager.updateServer(id, { status: 'stopping' });
                this.showMessage('正在停止服务器...', 'info');
                
            const response = await this.apiRequest(`/servers/${id}/stop`, 'POST');
            if (response.success) {
                    this.showMessage('停止请求已发送，状态将实时更新', 'success');
                    return response;
            } else {
                    this.stateManager.updateServer(id, { status: 'running' });
                    throw new Error(response.message);
            }
            }, 'stop');
        } catch (error) {
            this.showMessage('停止失败: ' + error.message, 'error');
        }
    }

    async restartServer(id) {
        if (!confirm('确定要重启这个服务器吗？')) return;
        
        try {
            await this.stateManager.enqueueOperation(id, async () => {
                this.stateManager.updateServer(id, { status: 'restarting' });
                this.showMessage('正在重启服务器...', 'info');
                
                const response = await this.apiRequest(`/servers/${id}/restart`, 'POST');
            if (response.success) {
                    this.showMessage('重启请求已发送，状态将实时更新', 'success');
                    return response;
            } else {
                    this.stateManager.updateServer(id, { status: 'running' });
                    throw new Error(response.message);
            }
            }, 'restart');
        } catch (error) {
            this.showMessage('重启失败: ' + error.message, 'error');
        }
    }

    async deleteServer(id) {
        if (!confirm('确定要删除这个服务器吗？此操作不可撤销！')) return;
        
        try {
            await this.stateManager.enqueueOperation(id, async () => {
                const response = await this.apiRequest(`/servers/${id}`, 'DELETE');
                if (response.success) {
                    this.stateManager.state.servers.delete(id);
                    this.stateManager.notifySubscribers('servers', this.stateManager.state.servers);
                    this.showMessage('服务器删除成功', 'success');
                    return response;
                } else {
                    throw new Error(response.message);
                }
            }, 'delete');
        } catch (error) {
            this.showMessage('删除失败: ' + error.message, 'error');
        }
    }

    connectWebSocket() {
        if (this.smartWebSocket) {
            return; // 已经连接
        }

        try {
            const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
            const wsUrl = `${protocol}//${window.location.host}/ws/status`;
            
            this.smartWebSocket = new SmartWebSocket(wsUrl, this.stateManager);
            
            this.stateManager.subscribe('websocket', (state) => {
                if (state.connected) {
                } else {
                }
            });
            
        } catch (error) {
            console.error('创建智能WebSocket连接失败:', error);
        }
    }

    disconnectWebSocket() {
        if (this.smartWebSocket) {
            this.smartWebSocket.disconnect();
            this.smartWebSocket = null;
        }
    }





    viewServer(id) {
        const server = this.stateManager.state.servers.get(id);
        if (!server) return;

        this.stateManager.setState('ui', { 
            modals: { serverModal: { type: 'view', serverId: id } } 
        });

        document.getElementById('serverId').value = server.id;
        document.getElementById('serverName').value = server.name;
        document.getElementById('serverHost').value = server.host;
        document.getElementById('serverPort').value = server.port;
        document.getElementById('serverUsername').value = server.username;
        document.getElementById('serverPassword').value = server.password;
        document.getElementById('serverL2TPPort').value = server.l2tp_port;
        document.getElementById('serverPSK').value = server.psk;
        document.getElementById('serverExpireDate').value = new Date(server.expire_date).toISOString().split('T')[0];

        this.loadUsersConfig(server.users);

        // 设置所有表单字段为只读
        this.setFormReadOnly(true);

        document.getElementById('serverModalTitle').textContent = '查看服务器详情';
        document.getElementById('serverModal').style.display = 'block';
    }

    // 设置表单只读状态
    setFormReadOnly(readonly) {
        const formFields = [
            'serverName', 'serverHost', 'serverPort', 'serverUsername', 
            'serverPassword', 'serverL2TPPort', 'serverPSK', 'serverExpireDate'
        ];

        formFields.forEach(fieldId => {
            const field = document.getElementById(fieldId);
            if (field) {
                field.readOnly = readonly;
                if (readonly) {
                    field.style.backgroundColor = '#f8f9fa';
                    } else {
                    field.style.backgroundColor = '';
                }
            }
        });

        const userInputs = document.querySelectorAll('.user-username, .user-password');
        userInputs.forEach(input => {
            input.readOnly = readonly;
            if (readonly) {
                input.style.backgroundColor = '#f8f9fa';
            } else {
                input.style.backgroundColor = '';
            }
        });

        const addUserBtn = document.querySelector('button[onclick="l2tpManager.addUser()"]');
        const removeUserBtns = document.querySelectorAll('.remove-user-btn');
        
        if (addUserBtn) {
            addUserBtn.style.display = readonly ? 'none' : 'inline-block';
        }
        
        removeUserBtns.forEach(btn => {
            btn.style.display = readonly ? 'none' : 'inline-block';
        });

        const saveBtn = document.querySelector('#serverForm button[type="submit"]');
        if (saveBtn) {
            if (readonly) {
                saveBtn.style.display = 'none';
            } else {
                saveBtn.style.display = 'inline-block';
            }
        }
    }

    addServer() {
        this.stateManager.setState('ui', { 
            modals: { serverModal: { type: 'add', serverId: null } } 
        });

        document.getElementById('serverId').value = '';
        document.getElementById('serverName').value = '';
        document.getElementById('serverHost').value = '';
        document.getElementById('serverPort').value = '22';
        document.getElementById('serverUsername').value = '';
        document.getElementById('serverPassword').value = '';
        document.getElementById('serverL2TPPort').value = '';
        document.getElementById('serverPSK').value = '';
        document.getElementById('serverExpireDate').value = '';

        this.loadUsersConfig('[]');

        this.setFormReadOnly(false);

        document.getElementById('serverModalTitle').textContent = '新建服务器';
        document.getElementById('serverModal').style.display = 'block';
    }

    loadUsersConfig(usersData) {
        const container = document.getElementById('usersContainer');
        container.innerHTML = '';

        let users = [];
        if (usersData) {
            try {
                users = JSON.parse(usersData);
            } catch (e) {
                console.error('解析用户配置失败:', e);
                users = [];
            }
        }

        if (users.length === 0) {
            users = [{ username: '', password: '' }];
        }

        users.forEach(user => {
            this.addUserConfigItem(user.username, user.password);
        });
    }

    addUserConfigItem(username = '', password = '') {
        const container = document.getElementById('usersContainer');
        const userItem = document.createElement('div');
        userItem.className = 'user-config-item';
        
        userItem.innerHTML = `
            <div class="form-row">
                <div class="form-group">
                    <label>用户名</label>
                    <input type="text" class="user-username" value="${username}" placeholder="输入用户名" required>
                </div>
                <div class="form-group">
                    <label>密码</label>
                    <input type="text" class="user-password" value="${password}" placeholder="输入密码" required>
                </div>
            </div>
            <button type="button" class="btn btn-danger btn-sm remove-user-btn" onclick="l2tpManager.removeUser(this)">删除</button>
        `;
        
        container.appendChild(userItem);
    }

    addUser() {
        this.addUserConfigItem();
    }

    removeUser(button) {
        const container = document.getElementById('usersContainer');
        if (container.children.length > 1) {
            button.closest('.user-config-item').remove();
        } else {
            this.showMessage('至少需要保留一个用户配置', 'warning');
        }
    }

    collectUsersConfig() {
        const users = [];
        const userItems = document.querySelectorAll('.user-config-item');
        
        userItems.forEach(item => {
            const username = item.querySelector('.user-username').value.trim();
            const password = item.querySelector('.user-password').value.trim();
            
            if (username && password) {
                users.push({ username, password });
            }
        });

        return JSON.stringify(users);
    }



    showExportModal() {
        // 重置表单和预览
        document.getElementById('exportForm').reset();
        document.getElementById('exportPreview').style.display = 'none';
        document.getElementById('filterOptions').style.display = 'none';
        
        document.getElementById('exportModal').style.display = 'block';
    }

    // 预览导出
    previewExport() {
        const formData = new FormData(document.getElementById('exportForm'));
        const exportRange = formData.get('exportRange');
        const exportFormat = formData.get('exportFormat') || 'simple';
        
        let serversToExport = [];
        const allServers = Array.from(this.stateManager.getState('servers').values());
        
        if (exportRange === 'all') {
            serversToExport = allServers;
        } else {
            serversToExport = this.filterServers(formData, allServers);
        }

        if (serversToExport.length === 0) {
            this.showMessage('没有符合条件的服务器', 'warning');
            return;
        }

        const isDetailed = exportFormat === 'detailed';
        const previewContent = this.formatServerConfigToTxt(serversToExport.slice(0, 3), isDetailed);
        
        // 显示预览
        document.getElementById('previewCount').textContent = serversToExport.length;
        document.getElementById('previewContent').textContent = previewContent + 
            (serversToExport.length > 3 ? '\n...\n（仅显示前3个服务器的预览）' : '');
        document.getElementById('exportPreview').style.display = 'block';
        
        // 滚动到预览区域
        document.getElementById('exportPreview').scrollIntoView({ behavior: 'smooth' });
    }

    hidePreview() {
        document.getElementById('exportPreview').style.display = 'none';
    }

    async processBatchExport() {
        const formData = new FormData(document.getElementById('exportForm'));
        const exportRange = formData.get('exportRange');
        const exportFormat = formData.get('exportFormat') || 'simple';
        
        let serversToExport = [];
        const allServers = Array.from(this.stateManager.getState('servers').values());
        
        if (exportRange === 'all') {
            serversToExport = allServers;
        } else {
            serversToExport = this.filterServers(formData, allServers);
        }

        if (serversToExport.length === 0) {
            this.showMessage('没有符合条件的服务器', 'warning');
            return;
        }

        const isDetailed = exportFormat === 'detailed';
        const configStr = this.formatServerConfigToTxt(serversToExport, isDetailed);
        const formatSuffix = isDetailed ? 'detailed' : 'simple';
        const filename = `l2tp-servers-${formatSuffix}-${new Date().toISOString().split('T')[0]}.txt`;
        
        this.downloadTxtFile(configStr, filename);
        this.closeModal('exportModal');
        this.showMessage(`已导出 ${serversToExport.length} 个服务器配置 (${isDetailed ? '详细' : '简化'}格式)`, 'success');
    }

    filterServers(formData, servers = null) {
        const serversToFilter = servers || Array.from(this.stateManager.getState('servers').values());
        return serversToFilter.filter(server => {
            // 日期筛选
            const dateFrom = formData.get('dateFrom');
            const dateTo = formData.get('dateTo');
            if (dateFrom && new Date(server.created_at) < new Date(dateFrom)) return false;
            if (dateTo && new Date(server.created_at) > new Date(dateTo)) return false;

            // 到期日期筛选
            const expireFrom = formData.get('expireFrom');
            const expireTo = formData.get('expireTo');
            if (expireFrom && new Date(server.expire_date) < new Date(expireFrom)) return false;
            if (expireTo && new Date(server.expire_date) > new Date(expireTo)) return false;

            // 端口范围筛选
            const portFrom = formData.get('portFrom');
            const portTo = formData.get('portTo');
            if (portFrom && server.l2tp_port < parseInt(portFrom)) return false;
            if (portTo && server.l2tp_port > parseInt(portTo)) return false;

            // 状态筛选
            const statusFilter = formData.get('statusFilter');
            if (statusFilter && server.status !== statusFilter) return false;

            // 名称筛选
            const nameFilter = formData.get('nameFilter');
            if (nameFilter && !server.name.toLowerCase().includes(nameFilter.toLowerCase())) return false;

            return true;
        });
    }



    // txt格式
    formatServerConfigToTxt(servers, isDetailed = false) {
        const timestamp = new Date().toLocaleString('zh-CN');
        let content = `导出数量：${servers.length}
导出时间: ${timestamp}

`;

        servers.forEach((server, index) => {
            content += `[${index + 1}] ${server.name}\n`;
            content += `${'-'.repeat(50)}\n`;
            
            content += `服务器地址: ${server.host}\n`;
            content += `中转端口: ${server.l2tp_port}\n`;
            content += `预共享密钥: ${server.psk || '未设置'}\n`;
            content += `到期时间: ${this.formatDate(server.expire_date)}\n`;
                
                let users = [];
                try {
                    users = JSON.parse(server.users || '[]');
                } catch (e) {
                    users = [];
                }
                
                if (users.length > 0) {
                    content += `L2TP用户配置:\n`;
                    users.forEach((user, idx) => {
                        content += `  用户${idx + 1}: ${user.username} / ${user.password}\n`;
                    });
                } else {
                    content += `L2TP用户配置: 未配置\n`;
            }
            
            if (isDetailed) {
                content += `SSH端口: ${server.port}\n`;
                content += `状态: ${server.status === 'running' ? '运行中' : '已停止'}\n`;
                
                if (server.created_at) {
                    content += `创建时间: ${this.formatDate(server.created_at)}\n`;
                }
                
                if (server.description) {
                    content += `备注: ${server.description}\n`;
                }
            }
            
            content += `\n`;
        });

        return content;
    }

    // 格式化日期
    formatDate(dateString) {
        if (!dateString) return '未设置';
        const date = new Date(dateString);
        return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')} ${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`;
    }

    downloadTxtFile(content, filename) {
        const blob = new Blob([content], { type: 'text/plain;charset=utf-8' });
        const url = URL.createObjectURL(blob);
        
        const a = document.createElement('a');
        a.href = url;
        a.download = filename;
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        URL.revokeObjectURL(url);
    }

    async showServerLogs(id) {
        try {
            const response = await this.apiRequest(`/servers/${id}/logs?lines=50`);
            if (response.success) {
                document.getElementById('logsContent').textContent = response.data.logs;
                document.getElementById('logsModal').style.display = 'block';
            } else {
                this.showMessage('获取日志失败: ' + response.message, 'error');
            }
        } catch (error) {
            this.showMessage('获取日志失败: ' + error.message, 'error');
        }
    }

    async saveServer() {
        const serverId = document.getElementById('serverId').value;
        const serverData = {
            name: document.getElementById('serverName').value,
            host: document.getElementById('serverHost').value,
            port: parseInt(document.getElementById('serverPort').value),
            username: document.getElementById('serverUsername').value,
            password: document.getElementById('serverPassword').value,
            l2tp_port: parseInt(document.getElementById('serverL2TPPort').value),
            psk: document.getElementById('serverPSK').value,
            users: this.collectUsersConfig(),
            expire_date: new Date(document.getElementById('serverExpireDate').value).toISOString()
        };

        try {
            let response;
            if (serverId) {
                response = await this.apiRequest(`/servers/${serverId}`, 'PUT', serverData);
            } else {
                response = await this.apiRequest('/servers', 'POST', serverData);
            }

            if (response.success) {
                if (response.data) {
                    this.stateManager.updateServer(response.data.id, response.data);
                }
                
                this.closeModal('serverModal');
                this.showMessage(serverId ? '服务器更新成功' : '服务器创建成功', 'success');
            } else {
                this.showMessage('保存失败: ' + response.message, 'error');
            }
        } catch (error) {
            this.showMessage('保存失败: ' + error.message, 'error');
        }
    }


    showMessage(message, type = 'info', duration = 5000) {
        const messageDiv = document.createElement('div');
        messageDiv.className = `message message-${type}`;
        messageDiv.textContent = message;
        
        document.body.appendChild(messageDiv);
        
        setTimeout(() => {
            messageDiv.remove();
        }, duration);
    }

    closeModal(modalId) {
        document.getElementById(modalId).style.display = 'none';
        
        // 清理模态框状态
        if (modalId === 'serverModal') {
            // 重置表单为可编辑状态，清除样式
            this.setFormReadOnly(false);
            
            // 清理状态管理器中的模态框状态
            this.stateManager.setState('ui', { 
                modals: { serverModal: { type: null, serverId: null } } 
            });
            
            document.getElementById('serverForm').reset();
            document.getElementById('serverId').value = '';
            // 重置用户配置
            this.loadUsersConfig('');
        } else if (modalId === 'exportModal') {
            // 重置导出表单和预览
            document.getElementById('exportForm').reset();
            document.getElementById('exportPreview').style.display = 'none';
            document.getElementById('filterOptions').style.display = 'none';
        }
    }

    // 事件监听器设置
    setupEventListeners() {
        // 登录表单
        document.getElementById('loginForm').addEventListener('submit', async (e) => {
            e.preventDefault();
            const username = document.getElementById('username').value;
            const password = document.getElementById('password').value;
            
            const result = await this.login(username, password);
            if (!result.success) {
                document.getElementById('loginError').textContent = result.message;
            }
        });

        // 退出登录
        document.getElementById('logoutBtn').addEventListener('click', () => {
            this.logout();
        });

        // 新建服务器按钮
        document.getElementById('addServerBtn').addEventListener('click', () => {
            this.addServer();
        });

        // 批量导出按钮
        document.getElementById('exportServersBtn').addEventListener('click', () => {
            this.showExportModal();
        });

        // 导出表单提交
        document.getElementById('exportForm').addEventListener('submit', (e) => {
            e.preventDefault();
            this.processBatchExport();
        });

        // 导出范围选择变化
        document.querySelectorAll('input[name="exportRange"]').forEach(radio => {
            radio.addEventListener('change', (e) => {
                const filterOptions = document.getElementById('filterOptions');
                if (e.target.value === 'filtered') {
                    filterOptions.style.display = 'block';
                } else {
                    filterOptions.style.display = 'none';
                }
            });
        });

        // 服务器表单提交
        document.getElementById('serverForm').addEventListener('submit', (e) => {
            e.preventDefault();
            this.saveServer();
        });

        // 模态框关闭按钮
        document.querySelectorAll('.close').forEach(closeBtn => {
            closeBtn.addEventListener('click', (e) => {
                const modal = e.target.closest('.modal');
                if (modal) {
                    this.closeModal(modal.id);
                }
            });
        });

        // 模态框外部点击关闭
        window.addEventListener('click', (e) => {
            if (e.target.classList.contains('modal')) {
                this.closeModal(e.target.id);
            }
        });
    }

    startPeriodicUpdates() {
        this.updateInterval = setInterval(() => {
            const isWebSocketConnected = this.stateManager.getState('websocket')?.connected;
            
            if (!isWebSocketConnected) {
                this.loadData();
            } else {
            this.loadTrafficStats();
            this.loadSystemStatus();
            }
        }, 30000); // 每30秒检查一次
    }
}

document.addEventListener('DOMContentLoaded', () => {
    window.l2tpManager = new L2TPManager();
});