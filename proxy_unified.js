/**
 * 统一缓存代理 —— 智能路由版
 *
 * 核心特性：
 *   - 单一端口，所有客户端统一入口
 *   - 根据请求标识自动分配缓存作用域
 *   - 支持 CLI、VS Code、OpenClaw、Agent 等多种客户端
 *
 * 使用方式：
 *   node proxy_unified.js [端口号]
 *
 * 客户端配置：
 *   在请求中添加 header: x-claude-source: <标识>
 *   例如: cli, vscode, openclaw, agent-team 等
 *
 * 缓存策略：
 *   - 不同来源默认隔离缓存
 *   - agent-* 开头的共享一个缓存作用域
 *   - 可通过 CACHE_STRATEGY 配置
 */

const http = require('http');
const https = require('https');
const { exec } = require('child_process');

// ========== 配置 ==========
const TARGET_HOST = 'ai.jiexi6.cn';
const PORT = parseInt(process.argv[2]) || 3456;

// 连接池优化 - 复用连接加速响应
const httpsAgent = new https.Agent({
  keepAlive: true,
  keepAliveMsecs: 30000,
  maxSockets: 50,
  maxFreeSockets: 10,
  timeout: 60000
});

// 模型定价 (USD per 1M tokens)
const MODEL_PRICING = {
  'claude-opus-4-6': { input: 15, output: 75, cache_write: 18.75, cache_read: 1.5 },
  'claude-sonnet-4-6': { input: 3, output: 15, cache_write: 3.75, cache_read: 0.3 },
  'claude-haiku-4-6': { input: 0.8, output: 4, cache_write: 1, cache_read: 0.08 }
};

// 缓存策略配置
const CACHE_STRATEGY = {
  // 共享缓存：所有 agent 使用同一个作用域
  'agent-team': 'scope_agents',
  'agent-task': 'scope_agents',
  'agent-explore': 'scope_agents',

  // 独立缓存：每个来源独立作用域
  'cli': 'scope_cli',
  'vscode': 'scope_vscode',
  'openclaw': 'scope_openclaw',

  // 默认作用域
  'default': 'scope_default'
};
// ==========================

let requestCount = 0;
const cacheStats = {}; // 记录每个 scope 的缓存统计

/**
 * 自动识别客户端类型
 */
function detectClient(req) {
  const ua = req.headers['user-agent'] || '';
  if (ua.includes('claude-cli')) return 'cli';
  if (ua.includes('vscode') || ua.includes('VSCode')) return 'vscode';
  if (ua.includes('cursor')) return 'cursor';
  return null;
}

/**
 * 智能判断缓存作用域
 */
function getScopeId(req) {
  if (req.headers['x-claude-scope']) {
    return req.headers['x-claude-scope'];
  }

  const source = req.headers['x-claude-source'] || detectClient(req) || 'default';

  if (CACHE_STRATEGY[source]) {
    return CACHE_STRATEGY[source];
  }

  if (source.startsWith('agent-')) {
    return CACHE_STRATEGY['agent-team'];
  }

  return `scope_${source}`;
}

/**
 * 计算请求成本
 */
function calculateCost(model, usage) {
  const pricing = MODEL_PRICING[model];
  if (!pricing) return null;

  const inp = usage.input_tokens || 0;
  const out = usage.output_tokens || 0;
  const cr = usage.cache_creation_input_tokens || 0;
  const rd = usage.cache_read_input_tokens || 0;

  const inputCost = (inp * pricing.input) / 1000000;
  const outputCost = (out * pricing.output) / 1000000;
  const cacheWriteCost = (cr * pricing.cache_write) / 1000000;
  const cacheReadCost = (rd * pricing.cache_read) / 1000000;
  const total = inputCost + outputCost + cacheWriteCost + cacheReadCost;

  // 计算无缓存成本（假设缓存读取按正常输入计费）
  const noCacheCost = ((inp + rd) * pricing.input + out * pricing.output + cr * pricing.cache_write) / 1000000;
  const saved = noCacheCost - total;

  return { inputCost, outputCost, cacheWriteCost, cacheReadCost, total, noCacheCost, saved };
}

/**
 * 处理代理请求
 */
function handleRequest(req, res) {
  const reqId = ++requestCount;
  const scopeId = getScopeId(req);
  const detectedClient = detectClient(req);
  const source = req.headers['x-claude-source'] || detectedClient || 'unknown';
  const reqStartTime = new Date().toLocaleTimeString('zh-CN', { hour12: false });

  console.log(`[${source}#${reqId}] ${reqStartTime} 收到请求: ${req.method} ${req.url}`);

  // 非 POST 请求直接透传
  if (req.method !== 'POST') {
    const options = {
      hostname: TARGET_HOST,
      port: 443,
      path: req.url,
      method: req.method,
      headers: { ...req.headers, host: TARGET_HOST },
      agent: httpsAgent
    };
    const proxyReq = https.request(options, proxyRes => {
      res.writeHead(proxyRes.statusCode, proxyRes.headers);
      proxyRes.pipe(res);
    });
    proxyReq.on('error', e => {
      res.writeHead(502);
      res.end(e.message);
    });
    req.pipe(proxyReq);
    return;
  }

  // 处理 POST 请求
  let body = [];
  req.on('data', chunk => body.push(chunk));
  req.on('end', () => {
    const rawBody = Buffer.concat(body).toString();
    let finalBody = rawBody;

    try {
      const obj = JSON.parse(rawBody);

      // 注入 billing header
      if (Array.isArray(obj.system)) {
        if (!obj.system.some(s => s.text?.includes('x-anthropic-billing-header'))) {
          obj.system.unshift({
            text: "x-anthropic-billing-header: cc_version=2.1.42.812; cc_entrypoint=cli; cch=4564e;",
            type: "text"
          });
        }
        // 优化：只在最后一个 system message 加缓存
        if (obj.system.length > 1) {
          const lastIdx = obj.system.length - 1;
          if (!obj.system[lastIdx].cache_control) {
            obj.system[lastIdx].cache_control = { type: "ephemeral" };
          }
        }
      }

      // 设置缓存作用域
      obj.metadata = { user_id: scopeId };

      // 优化：分层缓存策略
      if (Array.isArray(obj.messages) && obj.messages.length > 0) {
        const msgLen = obj.messages.length;

        // 长对话：在中间位置设置缓存点
        if (msgLen > 10) {
          const midPoint = Math.floor(msgLen / 2);
          const midMsg = obj.messages[midPoint];
          if (Array.isArray(midMsg.content) && midMsg.content.length > 0) {
            const last = midMsg.content[midMsg.content.length - 1];
            if (!last.cache_control) last.cache_control = { type: "ephemeral" };
          }
        }

        // 在倒数第2条消息设置缓存
        if (msgLen >= 2) {
          const secondLast = obj.messages[msgLen - 2];
          if (Array.isArray(secondLast.content) && secondLast.content.length > 0) {
            const last = secondLast.content[secondLast.content.length - 1];
            if (!last.cache_control) last.cache_control = { type: "ephemeral" };
          }
        }
      }

      finalBody = JSON.stringify(obj);
    } catch (e) {
      console.error(`[${source}#${reqId}] Body解析失败，原样透传`);
    }

    // 修改请求头
    const headers = { ...req.headers, host: TARGET_HOST };
    headers['user-agent'] = 'claude-cli/2.1.42 (external, claude-vscode, agent-sdk/0.2.42)';
    headers['anthropic-beta'] = 'claude-code-20250219,interleaved-thinking-2025-05-14,prompt-caching-scope-2026-01-05';
    headers['x-app'] = 'cli';
    headers['accept-encoding'] = 'identity';
    headers['connection'] = 'keep-alive';
    headers['anthropic-dangerous-direct-browser-access'] = 'true';
    headers['x-stainless-package-version'] = '0.73.0';
    headers['x-stainless-timeout'] = '600';
    if (headers['x-api-key'] && !headers['authorization']) {
      headers['authorization'] = 'Bearer ' + headers['x-api-key'];
    }
    headers['content-length'] = Buffer.byteLength(finalBody);

    let targetPath = req.url;
    if (targetPath.includes('/messages') && !targetPath.includes('beta=true')) {
      targetPath += targetPath.includes('?') ? '&beta=true' : '?beta=true';
    }

    // 转发请求
    const startTime = Date.now();
    let firstTokenTime = null;

    const proxyReq = https.request({
      hostname: TARGET_HOST,
      port: 443,
      path: targetPath,
      method: 'POST',
      headers,
      agent: httpsAgent
    }, proxyRes => {
      res.writeHead(proxyRes.statusCode, proxyRes.headers);
      let chunks = [];
      proxyRes.on('data', chunk => {
        if (!firstTokenTime) {
          firstTokenTime = Date.now();
          const ttft = firstTokenTime - startTime;
          console.log(`[${source}#${reqId}] ⚡ 首token: ${ttft}ms`);
        }
        chunks.push(chunk);
        res.write(chunk);
      });
      proxyRes.on('end', () => {
        res.end();
        const totalTime = Date.now() - startTime;
        const data = Buffer.concat(chunks).toString();
        console.log(`[${source}#${reqId}] ✓ 完成 总耗时:${totalTime}ms 数据:${data.length}字节`);

        // 解析缓存统计
        let u = null;
        try {
          const json = JSON.parse(data);
          if (json.usage) u = json.usage;
        } catch (e) {
          for (const line of data.split('\n')) {
            if (line.startsWith('data:') && line.includes('"usage"')) {
              try {
                const parsed = JSON.parse(line.slice(5).trim());
                if (parsed.usage) { u = parsed.usage; break; }
              } catch (e2) {}
            }
          }
        }

        if (u) {
          const cr = u.cache_creation_input_tokens || 0;
          const rd = u.cache_read_input_tokens || 0;
          const inp = u.input_tokens || 0;
          const out = u.output_tokens || 0;

          if (!cacheStats[scopeId]) {
            cacheStats[scopeId] = { requests: 0, totalCreated: 0, totalRead: 0, totalInput: 0, totalSaved: 0, totalCost: 0, totalSavedCost: 0 };
          }
          cacheStats[scopeId].requests++;
          cacheStats[scopeId].totalCreated += cr;
          cacheStats[scopeId].totalRead += rd;
          cacheStats[scopeId].totalInput += inp;
          cacheStats[scopeId].totalSaved += rd;

          const total = inp + cr + rd;
          const hitRate = total > 0 ? ((rd / total) * 100).toFixed(1) : '0.0';
          const status = rd > 0 ? '🟢 HIT' : cr > 0 ? '🟡 CREATED' : '🔴 NONE';
          const totalSaved = cacheStats[scopeId].totalSaved;

          // 计算成本
          let costStr = '';
          try {
            const modelMatch = data.match(/"model":"([^"]+)"/);
            const model = modelMatch ? modelMatch[1] : null;
            if (model) {
              const cost = calculateCost(model, u);
              if (cost) {
                cacheStats[scopeId].totalCost += cost.total;
                cacheStats[scopeId].totalSavedCost += cost.saved;
                const savedStr = cost.saved > 0 ? ` 💚省:$${cost.saved.toFixed(6)}` : '';
                costStr = ` 💰$${cost.total.toFixed(6)}${savedStr}`;
                console.log(`[${source}#${reqId}] ${status} 输入:${inp} 输出:${out} 创建:${cr} 读取:${rd} 命中率:${hitRate}%${costStr}`);
                console.log(`[${source}#${reqId}] 💵 成本 实际:$${cost.total.toFixed(6)} 无缓存:$${cost.noCacheCost.toFixed(6)} 节省:$${cost.saved.toFixed(6)}`);
                console.log(`[${source}#${reqId}] 📊 累计 scope:${scopeId} 请求:${cacheStats[scopeId].requests}次 总花费:$${cacheStats[scopeId].totalCost.toFixed(4)} 总节省:$${cacheStats[scopeId].totalSavedCost.toFixed(4)}`);
                return;
              }
            }
          } catch (e) {}

          console.log(`[${source}#${reqId}] ${status} 输入:${inp} 输出:${out} 创建:${cr} 读取:${rd} 命中率:${hitRate}%${costStr}`);
          console.log(`[${source}#${reqId}] 📊 累计 scope:${scopeId} 请求:${cacheStats[scopeId].requests}次 节省:${totalSaved}tokens 总花费:$${cacheStats[scopeId].totalCost.toFixed(4)}`);
        }
      });
    });

    proxyReq.on('error', e => {
      console.error(`[${source}#${reqId}] ❌ ${e.message}`);
      res.writeHead(502);
      res.end('Proxy Error');
    });

    proxyReq.write(finalBody);
    proxyReq.end();
  });
}

/**
 * 关闭占用端口的进程
 */
function killPort(port) {
  return new Promise((resolve) => {
    exec(`lsof -ti:${port} | xargs kill -9`, () => {
      resolve(); // 忽略错误，继续启动
    });
  });
}

/**
 * 启动服务器
 */
async function startServer() {
  console.log(`正在检查端口 ${PORT}...`);
  await killPort(PORT);

  const server = http.createServer(handleRequest);
  server.listen(PORT, () => {
    console.log(`\n========== 统一缓存代理已启动 ==========`);
    console.log(`监听端口: ${PORT}`);
    console.log(`目标服务: ${TARGET_HOST}`);
    console.log(`\n缓存策略:`);
    for (const [source, scope] of Object.entries(CACHE_STRATEGY)) {
      console.log(`  ${source.padEnd(15)} → ${scope}`);
    }
    console.log(`\n客户端配置示例:`);
    console.log(`  Base URL: http://localhost:${PORT}`);
    console.log(`  Header: x-claude-source: <cli|vscode|openclaw|agent-team>`);
    console.log(`\n等待请求...\n`);
  });
}

startServer();
