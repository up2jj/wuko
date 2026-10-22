'use strict';

// The only Node-specific part of the SDK: load the neutral descriptor and map
// ordinary JavaScript calls onto the versioned host protocol.
const fs = require('node:fs');
const http = require('node:http');
const maxBytes = 10 * 1024 * 1024;
const marker = Symbol.for('wuko.sdk.v1');

if (!globalThis[marker]) {
  if (Number(process.versions.node.split('.')[0]) < 22) throw new Error('Wuko SDK requires Node.js 22 or newer');
  const descriptor = parse(fs.readFileSync(process.env.WUKO_SDK_DESCRIPTOR, 'utf8'));
  if (descriptor.version !== 1) throw new Error('Unsupported Wuko SDK bridge version');
  if (Object.hasOwn(globalThis, 'wuko')) throw new Error('globalThis.wuko is already defined');

  function call(method, params) {
    // Validation runs inside the Promise so all public methods reject uniformly.
    return new Promise((resolve, reject) => {
      validate(params);
      const body = JSON.stringify({ version: 1, method, params });
      if (Buffer.byteLength(body) > maxBytes) throw new Error('Wuko SDK request exceeds 10 MiB');
      const request = http.request({
        socketPath: descriptor.socket,
        path: '/v1/call', method: 'POST', agent: false,
        headers: {
          Authorization: `Bearer ${descriptor.token}`,
          'Content-Type': 'application/json',
          'Content-Length': Buffer.byteLength(body),
        },
      }, response => {
        const chunks = [];
        let length = 0;
        response.on('data', chunk => {
          length += chunk.length;
          if (length > maxBytes) {
            response.destroy(new Error('Wuko SDK response exceeds 10 MiB'));
            return;
          }
          chunks.push(chunk);
        });
        response.on('error', reject);
        response.on('end', () => {
          try {
            if (response.statusCode !== 200) throw new Error(`Wuko SDK bridge HTTP ${response.statusCode}`);
            const message = parse(Buffer.concat(chunks).toString('utf8'));
            if (message.version !== 1) throw new Error('Unsupported Wuko SDK response version');
            if (message.error) {
              const error = new Error(message.error.message);
              error.code = message.error.code;
              throw error;
            }
            resolve(message.result ?? null);
          } catch (error) { reject(error); }
        });
      });
      request.on('error', reject);
      request.end(body);
    });
  }

  const sdk = {
    ...descriptor.context,
    output: (name, value) => call('output', { name, value }),
    setVar: (name, value) => call('set_var', { name, value }),
    secret: reference => call('secret', { reference }),
    render: (template, extra = {}) => call('render', { template, extra }),
    step: (type, config = {}) => call('step', { type, config }),
    runWorkflow: (name, { target = '', vars = {} } = {}) => call('run_workflow', { name, target, vars }),
    helpers: new Proxy(Object.create(null), {
      get: (_, name) => typeof name === 'string' && name !== 'then'
        ? (...args) => call('helper', { name, args }) : undefined,
    }),
  };
  freeze(sdk);
  Object.defineProperty(globalThis, 'wuko', { value: sdk, enumerable: true });
  Object.defineProperty(globalThis, marker, { value: true });
}

function parse(text) {
  return JSON.parse(text, (_, value) => {
    if (typeof value === 'number' && (!Number.isFinite(value) || (Number.isInteger(value) && !Number.isSafeInteger(value)))) {
      throw new TypeError('Wuko SDK numbers must be finite and integers must be safe; encode large integers as strings');
    }
    return value;
  });
}

function validate(value, seen = new Set()) {
  if (value === null || typeof value === 'string' || typeof value === 'boolean') return;
  if (typeof value === 'number') {
    if (!Number.isFinite(value) || (Number.isInteger(value) && !Number.isSafeInteger(value))) {
      throw new TypeError('Wuko SDK numbers must be finite and integers must be safe');
    }
    return;
  }
  if (typeof value !== 'object') throw new TypeError('Wuko SDK values must be JSON-compatible');
  if (!Array.isArray(value) && Object.getPrototypeOf(value) !== Object.prototype && Object.getPrototypeOf(value) !== null) {
    throw new TypeError('Wuko SDK objects must be plain objects');
  }
  if (seen.has(value)) throw new TypeError('Wuko SDK values must not contain cycles');
  seen.add(value);
  if (Array.isArray(value)) {
    for (let i = 0; i < value.length; i++) validate(value[i], seen);
  } else {
    if (Object.getOwnPropertySymbols(value).length) throw new TypeError('Wuko SDK values must not contain symbol keys');
    for (const key of Object.keys(value)) validate(value[key], seen);
  }
  seen.delete(value);
}

function freeze(value) {
  if (!value || typeof value !== 'object' || Object.isFrozen(value)) return value;
  for (const item of Object.values(value)) freeze(item);
  return Object.freeze(value);
}
