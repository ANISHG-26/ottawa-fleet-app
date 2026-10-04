import { defineConfig } from 'vite';
export default defineConfig({
  server: { proxy: {
    '/fleet-api': { target: 'http://127.0.0.1:8080', rewrite: path => path.replace(/^\/fleet-api/, ''),
      bypass: req => req.method !== 'GET' && req.method !== 'HEAD' ? false : undefined },
    '/ride-api': { target: 'http://127.0.0.1:8081', rewrite: path => path.replace(/^\/ride-api/, ''),
      bypass: req => req.url?.startsWith('/ride-api/v2/') && req.method !== 'GET' && req.method !== 'HEAD' ? false : undefined },
    '/simulation-api': { target: 'http://127.0.0.1:8083', rewrite: path => path.replace(/^\/simulation-api/, '') }
  } }
});
