// ==UserScript==
// @name         MCP Bridge
// @namespace    mcp-bridge
// @version      2.2.2
// @description  Universal MCP bridge for any chat LLM — ChatGPT, DeepSeek, Qwen, Yandex Alice, Z.ai
// @author       admin
// @match        *://chatgpt.com/*
// @match        *://chat.deepseek.com/*
// @match        *://tongyi.aliyun.com/*
// @match        *://qwenlm.github.io/*
// @match        *://chat.qwenlm.ai/*
// @match        *://chat.qwen.ai/*
// @match        *://ya.ru/*
// @match        *://yandex.ru/*
// @match        *://alice.yandex.ru/*
// @match        *://chat.yandex.ru/*
// @grant        GM_xmlhttpRequest
// @grant        GM_setValue
// @grant        GM_getValue
// @grant        GM_registerMenuCommand
// @run-at       document-idle
// ==/UserScript==

(function () {
  'use strict';

  // ═══════════════════════════════════════════════════════════════
  //  CONFIG
  // ═══════════════════════════════════════════════════════════════
  const DEFAULT_BRIDGE = 'https://gptadminmcp.bezrabotnyi.com';

  function bridgeUrl()   { return GM_getValue('bridge_url', DEFAULT_BRIDGE); }
  function bridgeKey()   { return GM_getValue('bridge_key', ''); }
  function autoEnter()   { return GM_getValue('auto_enter', false); }
  function compactMode() { return GM_getValue('compact_prompt', true); }
  function toolbarPosition() { return GM_getValue('toolbar_position', 'top-center'); }

  // ═══════════════════════════════════════════════════════════════
  //  SITE-SPECIFIC INPUT SELECTORS
  // ═══════════════════════════════════════════════════════════════
  const SITE_INPUTS = {
    'chat.qwen.ai':       ['textarea.message-input-textarea', 'textarea[placeholder]', '#prompt-textarea'],
    'chat.deepseek.com':  ['textarea[placeholder="Message DeepSeek"]', 'textarea.ds-scroll-area', 'textarea[placeholder]'],
    'chatgpt.com':        ['#prompt-textarea', 'textarea[placeholder]', '[contenteditable="true"]'],
    'ya.ru':              ['textarea[placeholder]', '[contenteditable="true"]', 'textarea'],
    'chat.yandex.ru':     ['textarea[placeholder]', '[contenteditable="true"]', 'textarea'],
    'alice.yandex.ru':    ['textarea[placeholder]', '[contenteditable="true"]', 'textarea'],
  };

  function findInput() {
    const host = location.hostname;
    const sels = SITE_INPUTS[host] || [];
    for (const s of sels) { const el = document.querySelector(s); if (el) return el; }
    const fallbacks = ['#prompt-textarea', 'textarea[placeholder]', '[contenteditable="true"]', 'textarea'];
    for (const s of fallbacks) { const el = document.querySelector(s); if (el) return el; }
    return null;
  }

  // ═══════════════════════════════════════════════════════════════
  //  REACT-COMPATIBLE INPUT
  // ═══════════════════════════════════════════════════════════════
  function setInputText(text) {
    const el = findInput();
    if (!el) return false;

    if (el.tagName === 'TEXTAREA' || el.tagName === 'INPUT') {
      try {
        const proto = el.tagName === 'TEXTAREA'
          ? HTMLTextAreaElement.prototype
          : HTMLInputElement.prototype;
        const setter = Object.getOwnPropertyDescriptor(proto, 'value').set;
        setter.call(el, text);
      } catch { el.value = text; }
      el.dispatchEvent(new Event('input', { bubbles: true }));
      el.dispatchEvent(new Event('change', { bubbles: true }));
      el.focus();

      if (autoEnter()) {
        setTimeout(() => {
          // Try keyboard events
          el.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', code: 'Enter', keyCode: 13, bubbles: true }));
          el.dispatchEvent(new KeyboardEvent('keypress', { key: 'Enter', code: 'Enter', keyCode: 13, bubbles: true }));
          el.dispatchEvent(new KeyboardEvent('keyup', { key: 'Enter', code: 'Enter', keyCode: 13, bubbles: true }));
          // Also try clicking the send button
          const form = el.closest('form');
          if (form) {
            const sendBtn = form.querySelector('button[type="submit"], button[aria-label*="end"], button[data-testid*="send"]');
            if (sendBtn) sendBtn.click();
          }
        }, 200);
      }
      return true;
    }

    if (el.contentEditable === 'true') {
      el.focus();
      document.execCommand('selectAll', false, null);
      document.execCommand('insertText', false, text);
      return true;
    }
    return false;
  }

  // ═══════════════════════════════════════════════════════════════
  //  CLIPBOARD
  // ═══════════════════════════════════════════════════════════════
  async function copyToClipboard(text) {
    try { await navigator.clipboard.writeText(text); return true; }
    catch {
      const ta = document.createElement('textarea');
      ta.value = text; ta.style.cssText = 'position:fixed;left:-9999px';
      document.body.appendChild(ta); ta.select();
      try { document.execCommand('copy'); return true; } catch { return false; }
      finally { ta.remove(); }
    }
  }

  async function readClipboard() {
    try { return await navigator.clipboard.readText(); }
    catch { return null; }
  }

  // ═══════════════════════════════════════════════════════════════
  //  TOAST
  // ═══════════════════════════════════════════════════════════════
  function toast(msg, ms = 2500) {
    let t = document.getElementById('mcp-toast');
    if (!t) {
      t = document.createElement('div');
      t.id = 'mcp-toast';
      document.body.appendChild(t);
    }
    t.textContent = msg;
    t.style.cssText = `
      position:fixed; bottom:80px; left:50%; transform:translateX(-50%);
      z-index:9999999; padding:8px 18px; border-radius:8px;
      background:#313244; color:#cdd6f4; font:13px/1.4 system-ui,sans-serif;
      box-shadow:0 4px 16px rgba(0,0,0,.4); opacity:1; transition:opacity .3s;
    `;
    clearTimeout(t._tm);
    t._tm = setTimeout(() => { t.style.opacity = '0'; }, ms);
  }

  // ═══════════════════════════════════════════════════════════════
  //  BRIDGE API (GM_xmlhttpRequest — no CORS issues)
  // ═══════════════════════════════════════════════════════════════
  function requireBridgeKey(action = 'MCP data') {
    const key = bridgeKey();
    if (key) return key;
    toast(`${action} is locked: set Bridge Key in ⚙ settings`, 5000);
    openSettings();
    return null;
  }

  function api(method, path, body) {
    return new Promise((resolve, reject) => {
      const url = bridgeUrl() + path;
      GM_xmlhttpRequest({
        method, url,
        headers: { 'Content-Type': 'application/json' },
        data: body ? JSON.stringify(body) : undefined,
        timeout: 40000,
        onload(r) {
          const ct = r.responseHeaders.match(/content-type:\s*([^\r\n]+)/i);
          const isJson = ct && /json/i.test(ct[1]);
          if (isJson) { try { resolve(JSON.parse(r.responseText)); return; } catch {} }
          resolve(r.responseText);
        },
        onerror: reject,
        ontimeout() { reject(new Error('bridge timeout')); },
      });
    });
  }

  // ═══════════════════════════════════════════════════════════════
  //  MCP JSON PARSING
  // ═══════════════════════════════════════════════════════════════
  function extractMcpJson(text) {
    if (!text) return null;
    const trimmed = text.trim();
    // Fast path: pure JSON
    if (trimmed.startsWith('{')) {
      try {
        const o = JSON.parse(trimmed);
        if ((o.target || o.agent) && o.tool) return normalizeMcpJson(o);
      } catch {}
    }
    // Bracket-counting extraction: find each '{' and its matching '}'
    // This handles nested JSON like {"args":{"url":"..."}} which regex can't do
    for (let i = 0; i < text.length; i++) {
      if (text[i] !== '{') continue;
      let depth = 0;
      for (let j = i; j < text.length; j++) {
        if (text[j] === '{') depth++;
        else if (text[j] === '}') depth--;
        if (depth === 0) {
          try {
            const o = JSON.parse(text.substring(i, j + 1));
            if ((o.target || o.agent) && o.tool) return normalizeMcpJson(o);
          } catch {}
          break; // Found matching '}', move to next '{'
        }
      }
    }
    return null;
  }

  function normalizeMcpJson(o) {
    return {
      target: o.target || o.agent,
      tool: o.tool,
      args: o.args || o.arguments || o.params || {},
    };
  }

  // ═══════════════════════════════════════════════════════════════
  //  EXECUTE MCP CALL
  // ═══════════════════════════════════════════════════════════════
  async function execMcp(cmd, inlineBtn) {
    const key = bridgeKey();
    if (!key) {
      toast('No bridge key! Click ⚙ to set it', 4000);
      if (inlineBtn) { flashBtn(inlineBtn, 'error'); }
      return;
    }

    // Animate inline button if present
    if (inlineBtn) setInlineLoading(inlineBtn, true);

    const label = `${cmd.target}/${cmd.tool}`;
    let resultStr, isError = false;

    try {
      const result = await api('POST', `/mcp-prompt/call?key=${encodeURIComponent(key)}`, {
        target: cmd.target,
        tool: cmd.tool,
        args: cmd.args || {},
      });

      if (result && result.error) {
        isError = true;
        resultStr = typeof result.error === 'string' ? result.error : JSON.stringify(result.error, null, 2);
      } else {
        const payload = result.result || result.response || result;
        resultStr = typeof payload === 'string' ? payload : JSON.stringify(payload, null, 2);
      }
    } catch (err) {
      isError = true;
      resultStr = err.message || String(err);
    }

    // Build result text
    const prefix = isError ? `[MCP Error: ${label}]` : `[MCP Result: ${label}]`;
    const bt = '`'.repeat(3);
    const fullText = `${prefix}\n${bt}json\n${resultStr}\n${bt}`;

    // Copy to clipboard + insert into input
    await copyToClipboard(fullText);
    const inserted = setInputText(fullText);

    // Animate inline button
    if (inlineBtn) {
      setInlineLoading(inlineBtn, false);
      flashBtn(inlineBtn, isError ? 'error' : 'success');
    }

    toast(
      (isError ? 'MCP error' : 'MCP done') + ': ' + label +
      (inserted ? ' — pasted' : ' — clipboard only'),
      isError ? 4000 : 2500
    );
  }

  // ═══════════════════════════════════════════════════════════════
  //  INLINE PLAY BUTTONS (per MCP block)
  // ═══════════════════════════════════════════════════════════════
  const processedBlocks = new WeakSet();

  function scanAndInjectPlayButtons() {
    // Strategy 1: ChatGPT — <code> inside <pre>
    document.querySelectorAll('pre > code').forEach(block => {
      // Skip if inside Alisa CodeBlock (Strategy 5 handles it)
      if (block.closest('div.CodeBlock')) return;
      tryInjectPlay(block, () => block.textContent);
    });

    // Strategy 2: DeepSeek — div.md-code-block > pre
    document.querySelectorAll('.md-code-block').forEach(block => {
      const pre = block.querySelector('pre');
      if (pre) tryInjectPlay(block, () => pre.textContent);
    });

    // Strategy 3: Qwen — pre.qwen-markdown-code with Monaco Editor
    // Structure: pre.qwen-markdown-code > .qwen-markdown-code-header + .qwen-markdown-code-body.mcp
    // The code-body contains a Monaco Editor whose textContent is polluted with
    // line numbers, aria labels, etc. We extract clean text from .view-lines instead.
    document.querySelectorAll('pre.qwen-markdown-code').forEach(pre => {
      if (processedBlocks.has(pre)) return;
      const codeBody = pre.querySelector('.qwen-markdown-code-body');
      if (!codeBody) return;

      // Clean text extractor: use Monaco's .view-lines
      // innerText respects CSS visibility (excludes line-number gutters),
      // while textContent includes everything (line numbers, aria labels, etc.)
      const textExtractor = () => {
        const viewLines = codeBody.querySelector('.view-lines');
        if (viewLines) {
          // Try innerText first (CSS-aware, excludes hidden line numbers)
          const text = viewLines.innerText;
          if (text && text.trim()) return text;
        }
        // Fallback: build text from mtk* spans line by line
        const viewLinesEl = codeBody.querySelector('.view-lines');
        if (viewLinesEl) {
          const lines = [];
          viewLinesEl.querySelectorAll('.view-line').forEach(vl => {
            let lineText = '';
            vl.querySelectorAll('span[class*="mtk"]').forEach(span => {
              lineText += span.textContent;
            });
            if (lineText.trim()) lines.push(lineText);
          });
          if (lines.length > 0) return lines.join('\n');
        }
        // Fallback: try textarea.inputarea (Monaco's hidden accessibility textarea)
        const inputTa = codeBody.querySelector('textarea.inputarea, textarea.ime-text-area');
        if (inputTa && inputTa.value) return inputTa.value;
        // Last resort: full code-body textContent (dirty but bracket-counting parser can handle it)
        return codeBody.textContent;
      };

      const cmd = extractMcpJson(textExtractor());
      if (!cmd) return;

      processedBlocks.add(pre);
      if (pre.querySelector('.mcp-inline-play')) return;

      // Create play button
      const btn = document.createElement('button');
      btn.className = 'mcp-inline-play';
      btn.innerHTML = '▶';
      btn.title = `Execute: ${cmd.target}/${cmd.tool}`;
      btn.addEventListener('click', async (e) => {
        e.stopPropagation();
        e.preventDefault();
        const currentCmd = extractMcpJson(textExtractor()) || cmd;
        await execMcp(currentCmd, btn);
      });

      // Inject into Qwen's code header actions (next to copy/download buttons)
      const headerActions = pre.querySelector('.qwen-markdown-code-header-actions');
      if (headerActions) {
        const actionItem = document.createElement('div');
        actionItem.className = 'qwen-markdown-code-header-action-item';
        actionItem.style.cssText = 'display:flex;align-items:center;justify-content:center;';
        actionItem.appendChild(btn);
        btn.style.width = '22px';
        btn.style.height = '22px';
        btn.style.fontSize = '10px';
        headerActions.insertBefore(actionItem, headerActions.firstChild);
      } else {
        // Fallback: absolute position on the pre
        pre.style.position = 'relative';
        btn.style.position = 'absolute';
        btn.style.top = '6px';
        btn.style.right = '40px';
        btn.style.zIndex = '10';
        pre.appendChild(btn);
      }

      // Subtle highlight
      pre.style.outline = '1px solid rgba(137,180,250,.25)';
      pre.style.borderRadius = '6px';
    });

    // Strategy 4: Generic <pre> fallback
    document.querySelectorAll('pre').forEach(block => {
      if (block.closest('.md-code-block') || block.querySelector('code')) return;
      if (block.classList.contains('qwen-markdown-code')) return;  // already handled
      if (block.querySelector('.qwen-markdown-code-body, [class*="markdown-code-body"]')) return;
      if (block.closest('div.CodeBlock')) return;  // Alisa — Strategy 5
      tryInjectPlay(block, () => block.textContent);
    });

    // Strategy 5: Yandex Alisa — div.CodeBlock with language-mcp header
    // Structure: div.CodeBlock > .CodeBlock-Header > .CodeBlock-HeaderTitle="mcp"
    //            + .CodeBlock-HeaderActions (where we inject the play button)
    //            + .CodeBlock-Content > pre.CodeBlock-ContentPre > code.language-mcp
    document.querySelectorAll('div.CodeBlock').forEach(block => {
      if (processedBlocks.has(block)) return;

      // Check language label
      const headerTitle = block.querySelector('.CodeBlock-HeaderTitle');
      if (!headerTitle) return;
      const lang = headerTitle.textContent.trim().toLowerCase();
      if (lang !== 'mcp') return;

      const code = block.querySelector('code');
      if (!code) return;

      const textExtractor = () => code.textContent;
      const cmd = extractMcpJson(textExtractor());
      if (!cmd) return;

      processedBlocks.add(block);
      if (block.querySelector('.mcp-inline-play')) return;

      // Create play button
      const btn = document.createElement('button');
      btn.className = 'mcp-inline-play';
      btn.innerHTML = '▶';
      btn.title = `Execute: ${cmd.target}/${cmd.tool}`;
      btn.addEventListener('click', async (e) => {
        e.stopPropagation();
        e.preventDefault();
        const currentCmd = extractMcpJson(textExtractor()) || cmd;
        await execMcp(currentCmd, btn);
      });

      // Inject into CodeBlock-HeaderActions (next to existing Copy/Collapse buttons)
      const headerActions = block.querySelector('.CodeBlock-HeaderActions');
      if (headerActions) {
        btn.style.position = 'relative';
        btn.style.marginRight = '6px';
        btn.style.flexShrink = '0';
        headerActions.insertBefore(btn, headerActions.firstChild);
      } else {
        // Fallback: absolute position on the block
        block.style.position = 'relative';
        btn.style.position = 'absolute';
        btn.style.top = '6px';
        btn.style.right = '40px';
        btn.style.zIndex = '10';
        block.appendChild(btn);
      }

      // Subtle highlight on the block
      block.style.outline = '1px solid rgba(137,180,250,.25)';
      block.style.borderRadius = '6px';
    });
  }

  function tryInjectPlay(container, textExtractor) {
    if (processedBlocks.has(container)) return;
    const text = textExtractor();
    const cmd = extractMcpJson(text);
    if (!cmd) return;

    processedBlocks.add(container);

    // Skip if already has a play button
    if (container.querySelector('.mcp-inline-play')) return;

    // Create play button
    const btn = document.createElement('button');
    btn.className = 'mcp-inline-play';
    btn.innerHTML = '▶';
    btn.title = `Execute: ${cmd.target}/${cmd.tool}`;
    btn.addEventListener('click', async (e) => {
      e.stopPropagation();
      e.preventDefault();
      // Re-extract in case DOM changed
      const currentCmd = extractMcpJson(textExtractor()) || cmd;
      await execMcp(currentCmd, btn);
    });

    // Find a good insertion point
    const header = container.querySelector('.flex.items-center, .code-header, [class*="header"], [class*="toolbar"]');
    if (header) {
      header.style.display = 'flex';
      header.style.alignItems = 'center';
      header.style.gap = '6px';
      btn.style.position = 'relative';
      header.appendChild(btn);
    } else {
      // Absolute position top-right of the code block
      container.style.position = 'relative';
      btn.style.position = 'absolute';
      btn.style.top = '6px';
      btn.style.right = '40px'; // leave room for copy button
      btn.style.zIndex = '10';
      container.appendChild(btn);
    }

    // Subtle highlight on the block
    container.style.outline = '1px solid rgba(137,180,250,.25)';
    container.style.borderRadius = '6px';
  }

  function setInlineLoading(btn, loading) {
    if (loading) {
      btn.classList.add('loading');
      btn.innerHTML = '';
    } else {
      btn.classList.remove('loading');
      btn.innerHTML = '▶';
    }
  }

  function flashBtn(btn, type) {
    btn.classList.add(type);
    setTimeout(() => btn.classList.remove(type), 1500);
  }

  // ═══════════════════════════════════════════════════════════════
  //  STYLES
  // ═══════════════════════════════════════════════════════════════
  const CSS = `
    /* ── floating toolbar ── */
    #mcp-toolbar {
      position: fixed; z-index: 2147483647;
      display: flex; flex-direction: column; gap: 6px; align-items: flex-end;
    }
    #mcp-toolbar.mcp-pos-top-center {
      top: 16px; left: 50%; right: auto; bottom: auto;
      transform: translateX(-50%); align-items: center;
    }
    #mcp-toolbar.mcp-pos-top-left {
      top: 16px; left: 16px; right: auto; bottom: auto;
      transform: none; align-items: flex-start;
    }
    #mcp-toolbar.mcp-pos-top-right {
      top: 16px; right: 16px; left: auto; bottom: auto;
      transform: none; align-items: flex-end;
    }
    #mcp-toolbar.mcp-pos-bottom-left {
      bottom: 16px; left: 16px; right: auto; top: auto;
      transform: none; align-items: flex-start;
    }
    #mcp-toolbar.mcp-pos-bottom-right {
      bottom: 16px; right: 16px; left: auto; top: auto;
      transform: none; align-items: flex-end;
    }
    #mcp-toolbar.mcp-pos-bottom-center {
      bottom: 16px; left: 50%; right: auto; top: auto;
      transform: translateX(-50%); align-items: center;
    }

    .mcp-btn {
      display: inline-flex; align-items: center; gap: 6px;
      padding: 8px 14px; border-radius: 8px; border: 1px solid rgba(255,255,255,.1);
      background: #1e1e2e; color: #cdd6f4;
      font: 600 12px/1 'Inter', system-ui, sans-serif;
      cursor: pointer; white-space: nowrap; transition: background .15s, transform .1s, opacity .15s;
      box-shadow: 0 2px 8px rgba(0,0,0,.35); user-select: none;
    }
    #mcp-toolbar:not(.mcp-open) .mcp-action { display: none; }
    #mcp-toolbar.mcp-no-key .mcp-secure { display: none; }
    .mcp-btn-main { border-color: rgba(137,180,250,.4); background: #181825; }
    .mcp-btn-main .chev { color: #89b4fa; font-size: 10px; transition: transform .15s; }
    #mcp-toolbar.mcp-open .mcp-btn-main .chev { transform: rotate(180deg); }
    .mcp-btn:hover { background: #313244; }
    .mcp-btn:active { transform: scale(.96); }
    .mcp-btn .icon { font-size: 14px; line-height: 1; }

    /* MCP All — indigo */
    .mcp-btn-all { border-color: rgba(99,102,241,.3); }
    .mcp-btn-all:hover { background: #2a2a40; }

    /* MCP — purple */
    .mcp-btn-agent { border-color: rgba(139,92,246,.3); }
    .mcp-btn-agent:hover { background: #2a2a40; }

    /* Exec clipboard — green, prominent */
    .mcp-btn-exec {
      border-color: rgba(16,185,129,.35); padding: 9px 16px;
    }
    .mcp-btn-exec:hover { background: #2a2a40; }

    /* Settings gear */
    .mcp-btn-gear {
      padding: 6px 10px; border-radius: 6px; font-size: 14px;
      border-color: rgba(255,255,255,.08);
    }

    /* key indicator dot on gear */
    .mcp-btn-gear .key-dot {
      width: 6px; height: 6px; border-radius: 50%;
      display: inline-block; margin-left: 4px;
    }
    .key-dot.set { background: #a6e3a1; }
    .key-dot.unset { background: #f38ba8; animation: mcp-pulse 1.5s ease-in-out infinite; }

    @keyframes mcp-pulse {
      0%, 100% { opacity: 1; }
      50% { opacity: .4; }
    }

    /* ── inline play button (per MCP block) ── */
    .mcp-inline-play {
      display: inline-flex; align-items: center; justify-content: center;
      width: 26px; height: 26px; border-radius: 50%;
      background: #6366f1; color: #fff; border: none; cursor: pointer;
      font-size: 11px; line-height: 1; vertical-align: middle;
      transition: background .15s, transform .1s; flex-shrink: 0;
      box-shadow: 0 1px 4px rgba(0,0,0,.3);
    }
    .mcp-inline-play:hover { background: #818cf8; transform: scale(1.1); }
    .mcp-inline-play:active { transform: scale(.95); }
    .mcp-inline-play.loading {
      background: #45475a; pointer-events: none;
      animation: mcp-spin .8s linear infinite;
    }
    .mcp-inline-play.loading::after {
      content: ''; display: block; width: 16px; height: 16px;
      border: 2px solid rgba(255,255,255,.2); border-top-color: #89b4fa;
      border-radius: 50%;
    }
    .mcp-inline-play.success { background: #10b981; }
    .mcp-inline-play.error { background: #ef4444; }

    @keyframes mcp-spin { to { transform: rotate(360deg); } }

    /* ── settings panel ── */
    #mcp-settings {
      display: none; position: fixed; bottom: 70px; right: 16px; z-index: 2147483646;
      width: 360px; padding: 18px; border-radius: 12px;
      background: #1e1e2e; border: 1px solid rgba(255,255,255,.1);
      box-shadow: 0 8px 32px rgba(0,0,0,.5); color: #cdd6f4;
      font: 13px/1.5 'Inter', system-ui, sans-serif;
    }
    #mcp-settings.open { display: block; }
    #mcp-settings h3 {
      margin: 0 0 14px; font-size: 15px; color: #89b4fa;
      display: flex; align-items: center; gap: 8px;
    }
    .mcp-field { margin-bottom: 12px; }
    .mcp-field label {
      display: block; margin-bottom: 3px; color: #a6adc8; font-size: 11px;
      font-weight: 600; text-transform: uppercase; letter-spacing: .5px;
    }
    .mcp-field input[type=text], .mcp-field input[type=password], .mcp-field select {
      width: 100%; padding: 7px 10px; border-radius: 6px;
      border: 1px solid rgba(255,255,255,.1); background: #181825; color: #cdd6f4;
      font: 13px/1.4 'SF Mono', 'Fira Code', monospace; box-sizing: border-box;
    }
    .mcp-field input:focus, .mcp-field select:focus {
      outline: none; border-color: #89b4fa;
      box-shadow: 0 0 0 2px rgba(137,180,250,.2);
    }
    .mcp-check {
      display: flex; align-items: center; gap: 8px;
      margin-bottom: 10px; cursor: pointer; user-select: none;
    }
    .mcp-check input { margin: 0; accent-color: #89b4fa; }
    .mcp-check span { color: #cdd6f4; font-size: 13px; }
    .mcp-settings-footer {
      margin-top: 14px; padding-top: 12px;
      border-top: 1px solid rgba(255,255,255,.06);
      display: flex; justify-content: space-between; align-items: center;
    }
    .mcp-save-btn {
      padding: 6px 18px; border-radius: 6px; border: none; cursor: pointer;
      font: 600 12px system-ui; color: #1e1e2e; background: #89b4fa;
      transition: background .15s;
    }
    .mcp-save-btn:hover { background: #74c7ec; }
    .mcp-key-status { font-size: 11px; color: #6c7086; }

    /* ── agent dropdown ── */
    .mcp-dropdown {
      display: none; position: fixed; bottom: auto; right: 16px;
      min-width: 280px; max-height: 360px; overflow-y: auto;
      border-radius: 10px; background: #1e1e2e;
      border: 1px solid rgba(255,255,255,.1);
      box-shadow: 0 8px 32px rgba(0,0,0,.5); z-index: 2147483646;
    }
    .mcp-dropdown.open { display: block; }
    .mcp-dropdown-item {
      padding: 10px 14px; cursor: pointer; color: #cdd6f4;
      font: 12px/1.4 system-ui; border-bottom: 1px solid rgba(255,255,255,.04);
      display: flex; align-items: center; gap: 8px;
    }
    .mcp-dropdown-item:hover { background: #313244; }
    .mcp-dropdown-item:last-child { border-bottom: none; }
    .mcp-dropdown-item .agent-id { color: #89b4fa; font-weight: 600; font-family: monospace; }
    .mcp-dropdown-item .agent-arrow { color: #6c7086; margin-left: auto; }
  `;

  // ═══════════════════════════════════════════════════════════════
  //  SETTINGS PANEL
  // ═══════════════════════════════════════════════════════════════
  function createSettingsPanel() {
    const panel = document.createElement('div');
    panel.id = 'mcp-settings';
    renderSettingsContent(panel);
    document.body.appendChild(panel);
    return panel;
  }

  function renderSettingsContent(panel) {
    const key = bridgeKey();
    panel.innerHTML = `
      <h3>⚙ MCP Bridge Settings</h3>

      <div class="mcp-field">
        <label>Bridge URL</label>
        <input type="text" id="mcp-cfg-url" value="${esc(bridgeUrl())}" placeholder="${DEFAULT_BRIDGE}">
      </div>

      <div class="mcp-field">
        <label>Bridge Key <span style="color:${key ? '#a6e3a1' : '#f38ba8'}">(${key ? 'set: ' + key.slice(0, 6) + '...' : 'NOT SET — calls will fail'})</span></label>
        <input type="password" id="mcp-cfg-key" value="${esc(key)}" placeholder="mcpk_...">
      </div>

      <label class="mcp-check">
        <input type="checkbox" id="mcp-cfg-autoenter" ${autoEnter() ? 'checked' : ''}>
        <span>Auto-press <kbd style="background:#313244;padding:1px 5px;border-radius:3px;font-size:11px">Enter</kbd> after insert</span>
      </label>

      <label class="mcp-check">
        <input type="checkbox" id="mcp-cfg-compact" ${compactMode() ? 'checked' : ''}>
        <span>Compact prompt (fewer tokens)</span>
      </label>

      <div class="mcp-field">
        <label>Toolbar position</label>
        <select id="mcp-cfg-position">
          <option value="top-center" ${toolbarPosition() === 'top-center' ? 'selected' : ''}>Top center (default)</option>
          <option value="top-left" ${toolbarPosition() === 'top-left' ? 'selected' : ''}>Top left</option>
          <option value="top-right" ${toolbarPosition() === 'top-right' ? 'selected' : ''}>Top right</option>
          <option value="bottom-left" ${toolbarPosition() === 'bottom-left' ? 'selected' : ''}>Bottom left</option>
          <option value="bottom-center" ${toolbarPosition() === 'bottom-center' ? 'selected' : ''}>Bottom center</option>
          <option value="bottom-right" ${toolbarPosition() === 'bottom-right' ? 'selected' : ''}>Bottom right</option>
        </select>
      </div>

      <div class="mcp-settings-footer">
        <div class="mcp-key-status">
          Hotkeys: <kbd style="background:#313244;padding:1px 5px;border-radius:3px;font-size:10px">Alt+K</kbd> settings
          <kbd style="background:#313244;padding:1px 5px;border-radius:3px;font-size:10px;margin-left:4px">Alt+E</kbd> exec
        </div>
        <button class="mcp-save-btn" id="mcp-cfg-save">Save</button>
      </div>
    `;

    panel.querySelector('#mcp-cfg-save').addEventListener('click', () => {
      const url = document.getElementById('mcp-cfg-url').value.trim().replace(/\/+$/, '');
      const newKey = document.getElementById('mcp-cfg-key').value.trim();
      GM_setValue('bridge_url', url || DEFAULT_BRIDGE);
      GM_setValue('bridge_key', newKey);
      GM_setValue('auto_enter', document.getElementById('mcp-cfg-autoenter').checked);
      GM_setValue('compact_prompt', document.getElementById('mcp-cfg-compact').checked);
      GM_setValue('toolbar_position', document.getElementById('mcp-cfg-position').value);
      applyToolbarPosition();
      refreshToolbarVisibility();
      panel.classList.remove('open');
      toast('Settings saved' + (newKey ? '' : ' — key is empty, MCP actions are hidden'));
    });
  }

  function updateKeyDot() {
    const dot = document.querySelector('.key-dot');
    if (!dot) return;
    const key = bridgeKey();
    dot.className = 'key-dot ' + (key ? 'set' : 'unset');
  }

  function refreshToolbarVisibility() {
    const toolbar = document.getElementById('mcp-toolbar');
    if (!toolbar) return;
    toolbar.classList.toggle('mcp-no-key', !bridgeKey());
    updateKeyDot();
  }

  function applyToolbarPosition() {
    const toolbar = document.getElementById('mcp-toolbar');
    if (!toolbar) return;
    toolbar.classList.remove(
      'mcp-pos-top-left', 'mcp-pos-top-center', 'mcp-pos-top-right',
      'mcp-pos-bottom-left', 'mcp-pos-bottom-center', 'mcp-pos-bottom-right'
    );
    const pos = toolbarPosition();
    const allowed = new Set(['top-left', 'top-center', 'top-right', 'bottom-left', 'bottom-center', 'bottom-right']);
    toolbar.classList.add('mcp-pos-' + (allowed.has(pos) ? pos : 'top-center'));
  }

  // ═══════════════════════════════════════════════════════════════
  //  TOOLBAR
  // ═══════════════════════════════════════════════════════════════
  function buildToolbar() {
    const style = document.createElement('style');
    style.textContent = CSS;
    document.head.appendChild(style);

    const toolbar = document.createElement('div');
    toolbar.id = 'mcp-toolbar';

    // ── Settings panel ──
    createSettingsPanel();

    // ── Folded main button ──
    const btnMain = document.createElement('button');
    btnMain.className = 'mcp-btn mcp-btn-main';
    btnMain.innerHTML = '<span class="icon">🧩</span> MCP <span class="chev">▾</span>';
    btnMain.title = 'Open MCP Bridge menu';
    btnMain.addEventListener('click', (e) => {
      e.stopPropagation();
      toolbar.classList.toggle('mcp-open');
    });

    // ── MCP All ──
    const btnAll = document.createElement('button');
    btnAll.className = 'mcp-btn mcp-btn-all mcp-action mcp-secure';
    btnAll.innerHTML = '<span class="icon">📡</span> MCP All';
    btnAll.title = 'Fetch MCP prompt for all agents (Alt+M)';
    btnAll.addEventListener('click', injectAll);

    // ── MCP (agent select) ──
    const btnAgent = document.createElement('button');
    btnAgent.className = 'mcp-btn mcp-btn-agent mcp-action mcp-secure';
    btnAgent.innerHTML = '<span class="icon">🔌</span> MCP';
    btnAgent.title = 'Select agent & fetch MCP prompt';
    btnAgent.addEventListener('click', toggleAgentDropdown);

    // ── Agent dropdown ──
    const dropdown = document.createElement('div');
    dropdown.className = 'mcp-dropdown';
    document.body.appendChild(dropdown);

    // ── Exec from clipboard ──
    const btnExec = document.createElement('button');
    btnExec.className = 'mcp-btn mcp-btn-exec mcp-action mcp-secure';
    btnExec.innerHTML = '<span class="icon">▶</span> Exec clipboard';
    btnExec.title = 'Execute MCP JSON from clipboard (Alt+E)';
    btnExec.addEventListener('click', execFromClipboard);

    // ── Settings gear ──
    const btnGear = document.createElement('button');
    btnGear.className = 'mcp-btn mcp-btn-gear mcp-action';
    const key = bridgeKey();
    btnGear.innerHTML = `<span class="icon">⚙</span><span class="key-dot ${key ? 'set' : 'unset'}"></span>`;
    btnGear.title = 'MCP Bridge Settings (Alt+K)';
    btnGear.addEventListener('click', () => {
      const panel = document.getElementById('mcp-settings');
      // Re-render settings each time to show current values
      renderSettingsContent(panel);
      panel.classList.toggle('open');
    });

    toolbar.append(btnMain, btnAll, btnAgent, btnExec, btnGear);
    document.body.appendChild(toolbar);
    applyToolbarPosition();
    refreshToolbarVisibility();

    // Close dropdown on outside click
    document.addEventListener('click', (e) => {
      if (!dropdown.contains(e.target) && e.target !== btnAgent) {
        dropdown.classList.remove('open');
      }
      if (!toolbar.contains(e.target) && !dropdown.contains(e.target)) {
        toolbar.classList.remove('mcp-open');
      }
      const panel = document.getElementById('mcp-settings');
      if (panel.classList.contains('open') && !panel.contains(e.target) && e.target !== btnGear && !btnGear.contains(e.target)) {
        panel.classList.remove('open');
      }
    });
  }

  // ═══════════════════════════════════════════════════════════════
  //  MCP ALL
  // ═══════════════════════════════════════════════════════════════
  async function injectAll() {
    const key = requireBridgeKey('MCP All');
    if (!key) return;
    const btn = document.querySelector('.mcp-btn-all');
    const orig = btn.innerHTML;
    btn.innerHTML = '<span class="icon">⏳</span> Loading...';
    btn.disabled = true;
    try {
      const compact = compactMode() ? '1' : '0';
      const prompt = await api('GET', `/mcp-prompt/prompt?target=all&compact=${compact}&key=${encodeURIComponent(key)}`);
      const text = typeof prompt === 'string' ? prompt : JSON.stringify(prompt);
      await copyToClipboard(text);
      const inserted = setInputText(text);
      toast('MCP All' + (inserted ? ' — pasted + clipboard' : ' — clipboard only'));
    } catch (e) {
      toast('Error: ' + e.message, 4000);
    }
    btn.innerHTML = orig;
    btn.disabled = false;
  }

  // ═══════════════════════════════════════════════════════════════
  //  AGENT SELECT
  // ═══════════════════════════════════════════════════════════════
  let dropdownBtn = null;

  function toggleAgentDropdown(e) {
    e.stopPropagation();
    const key = requireBridgeKey('MCP agent list');
    if (!key) return;
    const dropdown = document.querySelector('.mcp-dropdown');
    if (dropdown.classList.contains('open')) {
      dropdown.classList.remove('open');
      return;
    }

    // Position dropdown near the MCP button, clamped to viewport.
    const btn = e.currentTarget;
    const rect = btn.getBoundingClientRect();
    const dropdownWidth = 280;
    const left = Math.max(8, Math.min(rect.left, window.innerWidth - dropdownWidth - 8));
    const below = rect.bottom + 6;
    const above = Math.max(8, rect.top - 366);
    dropdown.style.left = left + 'px';
    dropdown.style.right = 'auto';
    dropdown.style.top = (rect.top < window.innerHeight / 2 ? below : above) + 'px';
    dropdown.style.bottom = 'auto';

    dropdown.innerHTML = '<div class="mcp-dropdown-item" style="color:#6c7086">Loading agents...</div>';
    dropdown.classList.add('open');

    loadAgents(dropdown, key);
  }

  // Parse compact prompt lines — handles agent IDs containing colons
  // Format: "  agent_id: tool1(...), tool2(...)"
  function parseAgentLine(line) {
    const trimmed = line.trim();
    const match = trimmed.match(/^([a-zA-Z0-9_-]+(?::[a-zA-Z0-9_-]+)*?):\s+\S/);
    if (!match) return null;
    return { agent_id: match[1], display: match[1] };
  }

  async function loadAgents(dropdown, key) {
    key = key || requireBridgeKey('MCP agent list');
    if (!key) {
      dropdown.classList.remove('open');
      return;
    }
    try {
      const prompt = await api('GET', `/mcp-prompt/prompt?target=all&compact=0&key=${encodeURIComponent(key)}`);
      const text = typeof prompt === 'string' ? prompt : JSON.stringify(prompt);

      // Parse agent IDs from prompt text
      const agents = [];
      const seen = new Set();
      // Method 1: parse compact lines
      const lines = text.split('\n');
      for (const l of lines) {
        const parsed = parseAgentLine(l);
        if (parsed && !seen.has(parsed.agent_id)) {
          seen.add(parsed.agent_id);
          agents.push(parsed);
        }
      }
      // Method 2: regex for "--- Agent: id" sections
      const sectionRe = /---\s*Agent:\s*([a-zA-Z0-9_-]+(?::[a-zA-Z0-9_-]+)*)/gi;
      let m;
      while ((m = sectionRe.exec(text)) !== null) {
        if (!seen.has(m[1])) { seen.add(m[1]); agents.push({ agent_id: m[1], display: m[1] }); }
      }

      if (!agents.length) {
        dropdown.innerHTML = '<div class="mcp-dropdown-item" style="color:#f38ba8">No agents found</div>';
        return;
      }

      dropdown.innerHTML = '';
      for (const a of agents.sort((a, b) => a.agent_id.localeCompare(b.agent_id))) {
        const item = document.createElement('div');
        item.className = 'mcp-dropdown-item';
        item.innerHTML = `<span class="agent-id">${esc(a.display)}</span><span class="agent-arrow">→</span>`;
        item.addEventListener('click', () => {
          dropdown.classList.remove('open');
          injectAgent(a.agent_id);
        });
        dropdown.appendChild(item);
      }
    } catch (e) {
      dropdown.innerHTML = `<div class="mcp-dropdown-item" style="color:#f38ba8">Error: ${esc(e.message)}</div>`;
    }
  }

  async function injectAgent(agentId) {
    const key = requireBridgeKey(`MCP ${agentId}`);
    if (!key) return;
    toast(`Loading ${agentId}...`);
    try {
      const compact = compactMode() ? '1' : '0';
      const prompt = await api('GET', `/mcp-prompt/prompt?target=${encodeURIComponent(agentId)}&compact=${compact}&key=${encodeURIComponent(key)}`);
      const text = typeof prompt === 'string' ? prompt : JSON.stringify(prompt);
      await copyToClipboard(text);
      const inserted = setInputText(text);
      toast(`MCP ${agentId}` + (inserted ? ' — pasted + clipboard' : ' — clipboard only'));
    } catch (e) {
      toast('Error: ' + e.message, 4000);
    }
  }

  // ═══════════════════════════════════════════════════════════════
  //  EXEC FROM CLIPBOARD
  // ═══════════════════════════════════════════════════════════════
  async function execFromClipboard() {
    const btn = document.querySelector('.mcp-btn-exec');
    const origHtml = btn.innerHTML;

    let clipText = await readClipboard();
    if (!clipText) {
      toast('Cannot read clipboard — copy MCP JSON first', 4000);
      return;
    }

    const cmd = extractMcpJson(clipText);
    if (!cmd) {
      toast('No MCP JSON found in clipboard', 4000);
      return;
    }

    // Show loading state on button
    btn.innerHTML = '<span class="icon">⏳</span> Executing...';
    btn.disabled = true;

    await execMcp(cmd, null);

    btn.innerHTML = origHtml;
    btn.disabled = false;
  }

  // ═══════════════════════════════════════════════════════════════
  //  HOTKEYS
  // ═══════════════════════════════════════════════════════════════
  document.addEventListener('keydown', (e) => {
    if (e.altKey && e.key === 'e') { e.preventDefault(); execFromClipboard(); }
    if (e.altKey && e.key === 'k') { e.preventDefault(); openSettings(); }
    if (e.altKey && e.key === 'm') { e.preventDefault(); injectAll(); }
  });

  function openSettings() {
    const panel = document.getElementById('mcp-settings');
    renderSettingsContent(panel);
    panel.classList.add('open');
    setTimeout(() => {
      const keyInput = document.getElementById('mcp-cfg-key');
      if (keyInput) keyInput.focus();
    }, 50);
  }

  // ═══════════════════════════════════════════════════════════════
  //  UTILS
  // ═══════════════════════════════════════════════════════════════
  function esc(s) {
    const d = document.createElement('div');
    d.textContent = s;
    return d.innerHTML;
  }

  // ═══════════════════════════════════════════════════════════════
  //  OBSERVER
  // ═══════════════════════════════════════════════════════════════
  let _scanTimer = null;
  const observer = new MutationObserver(() => {
    if (_scanTimer) return;
    _scanTimer = setTimeout(() => { scanAndInjectPlayButtons(); _scanTimer = null; }, 500);
  });

  // ═══════════════════════════════════════════════════════════════
  //  MENU COMMANDS
  // ═══════════════════════════════════════════════════════════════
  function setupMenu() {
    try {
      GM_registerMenuCommand('⚙ Settings (Alt+K)', openSettings);
      GM_registerMenuCommand('📡 MCP All (Alt+M)', injectAll);
      GM_registerMenuCommand('▶ Exec Clipboard (Alt+E)', execFromClipboard);
    } catch { /* Safari may not support GM_registerMenuCommand */ }
  }

  // ═══════════════════════════════════════════════════════════════
  //  INIT
  // ═══════════════════════════════════════════════════════════════
  function init() {
    buildToolbar();
    observer.observe(document.body, { childList: true, subtree: true });
    scanAndInjectPlayButtons();
    setupMenu();
    if (!bridgeKey()) {
      setTimeout(() => toast('MCP Bridge: click ⚙ to set key (Alt+K)', 5000), 2000);
    }
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else { init(); }

})();
