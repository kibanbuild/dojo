import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

// The SPA lives under /app: assets, router and dev server all share this base.
export default defineConfig({
  base: '/app/',
  plugins: [
    react(),
    tailwindcss(),
    {
      // Vite answers the bare base with a 404 hint; send /app to /app/ as the edge does.
      name: 'dojo-bare-base-redirect',
      configureServer(server) {
        server.middlewares.use((req, res, next) => {
          const url = req.url ?? '';
          if (url === '/app' || url.startsWith('/app?')) {
            res.statusCode = 308;
            res.setHeader('Location', `/app/${url.slice('/app'.length)}`);
            res.end();
            return;
          }
          next();
        });
      },
    },
  ],
  server: {
    // Astro's dev server (:4321) proxies /app here; HMR is reached through that same origin.
    port: 5174,
    strictPort: true,
    host: '127.0.0.1',
  },
  test: { include: ['src/**/*.test.{ts,tsx}'] },
});
