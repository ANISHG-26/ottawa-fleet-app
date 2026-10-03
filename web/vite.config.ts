import { defineConfig } from 'vite';
export default defineConfig({
  server: { proxy: {
    '/fleet-api': { target: 'http://127.0.0.1:8080', rewrite: path => path.replace(/^\/fleet-api/, '') },
    '/ride-api': { target: 'http://127.0.0.1:8081', rewrite: path => path.replace(/^\/ride-api/, '') }
  } }
});
