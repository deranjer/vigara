import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  build: { outDir: '../api/web', emptyOutDir: true },
  server: { proxy: { '/healthz': 'http://localhost:8080', '/api': 'http://localhost:8080' } },
})
