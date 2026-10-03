import { defineConfig } from '@playwright/test';
const env = (globalThis as typeof globalThis & { process?: { env?: Record<string, string | undefined> } }).process?.env;
export default defineConfig({
  testDir: './tests',
  use: { baseURL: 'http://127.0.0.1:4173', headless: true, channel: env?.PLAYWRIGHT_CHANNEL },
  webServer: { command: 'node node_modules/vite/bin/vite.js --host 127.0.0.1 --port 4173', port: 4173, reuseExistingServer: true }
});
